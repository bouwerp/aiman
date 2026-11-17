package harness

import (
	"archive/tar"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// maxDirtyPaths caps the paths a harness_dirty refusal names.
const maxDirtyPaths = 20

// dirtyPaths lists the tracked and untracked changes of repoDir, as
// `git status --porcelain` names them, at most maxDirtyPaths.
func dirtyPaths(ctx context.Context, repoDir string) ([]string, error) {
	out, err := gitOut(ctx, repoDir, "status", "--porcelain", "--untracked-files=all", "-z")
	if err != nil {
		return nil, err
	}
	var paths []string
	entries := strings.Split(string(out), "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		paths = append(paths, e[3:])
		if e[0] == 'R' || e[0] == 'C' {
			i++ // the rename's source path follows
		}
		if len(paths) == maxDirtyPaths {
			break
		}
	}
	return paths, nil
}

// exportCommit writes the tree of sha into dst with git archive. Entries that
// would land outside dst are refused; submodules are not in the archive.
func exportCommit(ctx context.Context, repoDir, sha, dst string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", repoDir, "archive", "--format=tar", sha) //nolint:gosec // G204: fixed git subcommand; sha was resolved by rev-parse
	var stderr strings.Builder
	cmd.Stderr = &stderr
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	xerr := extractTar(pipe, dst)
	_, _ = io.Copy(io.Discard, pipe)
	werr := cmd.Wait()
	if werr != nil {
		return gitError("archive", stderr.String(), werr)
	}
	return xerr
}

func extractTar(r io.Reader, dst string) error {
	tr := tar.NewReader(bufio.NewReader(r))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		target, err := insideDir(dst, h.Name)
		if err != nil {
			return err
		}
		if err := extractEntry(tr, h, target); err != nil {
			return err
		}
	}
}

func insideDir(dst, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("archive entry %q leaves the tree", name)
	}
	return filepath.Join(dst, clean), nil
}

func extractEntry(tr *tar.Reader, h *tar.Header, target string) error {
	switch h.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(target, 0o755) //nolint:gosec // G301: the app container reads the tree as the same uid
	case tar.TypeReg:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil { //nolint:gosec // G301: as above
			return err
		}
		mode := os.FileMode(0o644)
		if h.Mode&0o111 != 0 {
			mode = 0o755
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode) //nolint:gosec // G304: target was checked by insideDir
		if err != nil {
			return err
		}
		_, cerr := io.Copy(f, tr) //nolint:gosec // G110: a commit of the session's own repository
		if err := f.Close(); cerr == nil {
			cerr = err
		}
		return cerr
	case tar.TypeSymlink:
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil { //nolint:gosec // G301: as above
			return err
		}
		return os.Symlink(h.Linkname, target)
	default:
		return nil // pax headers and anything else git does not write
	}
}

// exportWorkingTree copies repoDir's tracked and untracked files that are not
// ignored into dst. A symlink is copied as a symlink, never followed.
func exportWorkingTree(ctx context.Context, repoDir, dst string) error {
	out, err := gitOut(ctx, repoDir, "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, name := range strings.Split(string(out), "\x00") {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		target, err := insideDir(dst, name)
		if err != nil {
			return err
		}
		if err := copyEntry(filepath.Join(repoDir, name), target); err != nil {
			return err
		}
	}
	return nil
}

func copyEntry(src, dst string) error {
	fi, err := os.Lstat(src)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // deleted in the working tree
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil { //nolint:gosec // G301: the app container reads the tree as the same uid
		return err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		link, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(link, dst)
	case fi.Mode().IsRegular():
		in, err := os.Open(src) //nolint:gosec // G304: a path git listed inside the checkout
		if err != nil {
			return err
		}
		defer in.Close()
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm()|0o600) //nolint:gosec // G304: dst was checked by insideDir
		if err != nil {
			return err
		}
		_, cerr := io.Copy(f, in)
		if err := f.Close(); cerr == nil {
			cerr = err
		}
		return cerr
	default:
		return nil // a nested repository or another non-file
	}
}
