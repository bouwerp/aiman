// Package tools keeps the debug tools of harness runs on the host: pinned
// release archives, downloaded once, checked against a sha256 in code before
// anything is unpacked, and mounted read-only into containers. Nothing is
// installed inside a container and no package manager runs.
package tools

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// Limits on what one tool may be.
const (
	maxArchive  = 512 << 20
	maxUnpacked = 1 << 30
	maxEntries  = 100_000
	// fetchTimeout bounds one download.
	fetchTimeout = 10 * time.Minute
	manifestName = "tool.json"
)

// Spec is one pinned tool archive (a .tar.gz).
type Spec struct {
	Name    string
	Version string
	URL     string
	// SHA256 is the archive's hex sha256; a download with another is refused
	// before it is unpacked.
	SHA256 string
	// Strip drops leading path components of every entry.
	Strip int
	// Keep picks the entries to unpack by their stripped, slash-separated
	// path; nil keeps every regular file.
	Keep func(rel string) bool
}

// ErrChecksum is a downloaded archive whose sha256 is not the pin.
var ErrChecksum = errors.New("tool checksum mismatch")

// ChecksumError names the tool whose download did not match.
type ChecksumError struct {
	Tool, Version, Want, Got string
}

func (e *ChecksumError) Error() string {
	return fmt.Sprintf("%s %s: download sha256 %s, the pin is %s", e.Tool, e.Version, e.Got, e.Want)
}

// Is matches ErrChecksum.
func (e *ChecksumError) Is(target error) bool { return target == ErrChecksum }

// Store keeps tools under Dir (~/.aiman/harness/tools), one directory per
// tool, version and architecture.
type Store struct {
	Dir string
	// Client fetches archives; nil is a client with fetchTimeout.
	Client *http.Client

	mu sync.Mutex
}

// manifest is tool.json beside an unpacked tool: the archive pin it came
// from and a digest of every file, checked on each Ensure.
type manifest struct {
	Name     string    `json:"name"`
	Version  string    `json:"version"`
	URL      string    `json:"url"`
	SHA256   string    `json:"sha256"`
	Tree     string    `json:"tree_sha256"`
	Unpacked time.Time `json:"unpacked"`
}

// Path is where spec unpacks.
func (s *Store) Path(spec Spec) string {
	return filepath.Join(s.Dir, fmt.Sprintf("%s-%s-linux-%s", spec.Name, spec.Version, runtime.GOARCH))
}

// Ensure returns the directory of spec's unpacked tool, downloading and
// unpacking it when it is missing, came from another pin, or any file
// changed since it was unpacked.
func (s *Store) Ensure(ctx context.Context, spec Spec) (string, error) {
	if spec.Name == "" || spec.Version == "" || spec.URL == "" || len(spec.SHA256) != 64 {
		return "", fmt.Errorf("tool %q: name, version, url and a sha256 are required", spec.Name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.Path(spec)
	if s.intact(dir, spec) {
		return dir, nil
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return "", err
	}
	archive, err := s.fetch(ctx, spec)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(archive) }()
	scratch, err := os.MkdirTemp(s.Dir, ".unpack-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	if err := unpack(archive, scratch, spec); err != nil {
		return "", fmt.Errorf("%s %s: %w", spec.Name, spec.Version, err)
	}
	tree, err := treeDigest(scratch)
	if err != nil {
		return "", err
	}
	m := manifest{Name: spec.Name, Version: spec.Version, URL: spec.URL, SHA256: spec.SHA256, Tree: tree, Unpacked: time.Now().UTC()}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(scratch, manifestName), b, 0o600); err != nil {
		return "", err
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.Rename(scratch, dir); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // G302: containers run as the host uid and read it
		return "", err
	}
	return dir, nil
}

// intact is a tool unpacked from spec's pin whose files still hash to the
// manifest's digest.
func (s *Store) intact(dir string, spec Spec) bool {
	b, err := os.ReadFile(filepath.Join(dir, manifestName)) //nolint:gosec // G304: serve's own tools directory
	if err != nil {
		return false
	}
	var m manifest
	if json.Unmarshal(b, &m) != nil || m.SHA256 != spec.SHA256 || m.Version != spec.Version {
		return false
	}
	tree, err := treeDigest(dir)
	return err == nil && tree == m.Tree
}

// fetch downloads spec into a temporary file under Dir and checks its sha256.
func (s *Store) fetch(ctx context.Context, spec Spec) (string, error) {
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spec.URL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s %s: download: %w", spec.Name, spec.Version, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s %s: download %s: %s", spec.Name, spec.Version, spec.URL, resp.Status)
	}
	f, err := os.CreateTemp(s.Dir, ".dl-")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, maxArchive+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxArchive {
		err = fmt.Errorf("%s %s: the archive is over %d bytes", spec.Name, spec.Version, maxArchive)
	}
	if err == nil {
		if got := hex.EncodeToString(h.Sum(nil)); got != spec.SHA256 {
			err = &ChecksumError{Tool: spec.Name, Version: spec.Version, Want: spec.SHA256, Got: got}
		}
	}
	if err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// unpack writes the kept regular files and directories of the archive under
// dst. Links, devices and entries that would leave dst are skipped or
// refused; nothing the archive names is followed.
func unpack(archive, dst string, spec Spec) error {
	f, err := os.Open(archive) //nolint:gosec // G304: the file fetch just wrote
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var total int64
	for n := 0; ; n++ {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if n >= maxEntries {
			return fmt.Errorf("more than %d entries", maxEntries)
		}
		rel, err := entryPath(h.Name, spec.Strip)
		if err != nil {
			return err
		}
		if rel == "" || (h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir) {
			continue
		}
		if h.Typeflag == tar.TypeReg && spec.Keep != nil && !spec.Keep(rel) {
			continue
		}
		if total += h.Size; total > maxUnpacked {
			return fmt.Errorf("over %d bytes unpacked", maxUnpacked)
		}
		if err := writeEntry(dst, rel, h, tr); err != nil {
			return err
		}
	}
}

// entryPath is name with strip components dropped, or an error for a name
// that is absolute or climbs out.
func entryPath(name string, strip int) (string, error) {
	if strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return "", fmt.Errorf("archive entry %q is absolute", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("archive entry %q climbs out", name)
		}
	}
	clean := path.Clean(name)
	parts := strings.Split(clean, "/")
	if clean == "." || len(parts) <= strip {
		return "", nil
	}
	return strings.Join(parts[strip:], "/"), nil
}

func writeEntry(dst, rel string, h *tar.Header, r io.Reader) error {
	target := filepath.Join(dst, filepath.FromSlash(rel))
	if h.Typeflag == tar.TypeDir {
		return os.MkdirAll(target, 0o755) //nolint:gosec // G301: containers run as the host uid and read it
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil { //nolint:gosec // G301: as above
		return err
	}
	mode := os.FileMode(0o644)
	if h.Mode&0o111 != 0 {
		mode = 0o755
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode) //nolint:gosec // G304: a checked path under the scratch directory
	if err != nil {
		return err
	}
	_, err = io.CopyN(out, r, h.Size)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// treeDigest hashes every regular file's path, mode and contents under dir,
// leaving out the manifest.
func treeDigest(dir string) (string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && p != filepath.Join(dir, manifestName) {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	for _, p := range files {
		fi, err := os.Stat(p)
		if err != nil {
			return "", err
		}
		rel, _ := filepath.Rel(dir, p)
		fmt.Fprintf(h, "%s\x00%o\x00", filepath.ToSlash(rel), fi.Mode().Perm())
		if err := hashFile(h, p); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashFile(w io.Writer, p string) error {
	f, err := os.Open(p) //nolint:gosec // G304: a file of serve's own tools directory
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(w, f)
	return err
}
