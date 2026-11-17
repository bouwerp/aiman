package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Source says where a run's harness came from.
type Source string

// Sources: the repository file, or repos.<slug>.harness in operator config.
const (
	SourceFile   Source = "file"
	SourceConfig Source = "config"
)

// Loaded is a harness file and what a run records about where it came from.
// SHA is the commit (HEAD for a dirty load). Dirty and DiffSHA256 are set only
// by LoadDirty: DiffSHA256 is the hex sha256 of `git diff HEAD` and the
// untracked files.
type Loaded struct {
	File       *File
	Source     Source
	SHA        string
	Dirty      bool
	DiffSHA256 string
}

// OverrideBytes turns a decoded repos.<slug>.harness value into the YAML that
// LoadAtSHA and LoadDirty take. A nil value is no override.
func OverrideBytes(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	b, err := yaml.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode harness override: %w", err)
	}
	return b, nil
}

// LoadAtSHA loads the harness for a run at commit sha of repoDir. A non-empty
// override (repos.<slug>.harness) wins over the file and is reported as
// SourceConfig; otherwise the file is read as committed, never from the
// working tree. Paths the harness names must not lie in a submodule of that
// commit.
func LoadAtSHA(ctx context.Context, repoDir, sha string, override []byte) (*Loaded, error) {
	full, err := revParse(ctx, repoDir, sha)
	if err != nil {
		return nil, err
	}
	l := &Loaded{SHA: full}
	if len(override) > 0 {
		l.Source = SourceConfig
		if l.File, err = parseOverride(override); err != nil {
			return nil, err
		}
	} else {
		body, err := committedFile(ctx, repoDir, full)
		if err != nil {
			return nil, err
		}
		l.Source = SourceFile
		if l.File, err = Parse(body); err != nil {
			return nil, err
		}
	}
	subs, err := committedSubmodules(ctx, repoDir, full)
	if err != nil {
		return nil, err
	}
	if err := checkSubmodules(l.File, subs); err != nil {
		return nil, err
	}
	return l, nil
}

// LoadDirty loads the harness from repoDir's working tree for a --dirty run.
// It records HEAD and the hash of the uncommitted changes. A non-empty override
// wins over the file, as for LoadAtSHA.
func LoadDirty(ctx context.Context, repoDir string, override []byte) (*Loaded, error) {
	head, err := revParse(ctx, repoDir, "HEAD")
	if err != nil {
		return nil, err
	}
	diff, err := DiffSHA256(ctx, repoDir)
	if err != nil {
		return nil, err
	}
	l := &Loaded{SHA: head, Dirty: true, DiffSHA256: diff}
	if len(override) > 0 {
		l.Source = SourceConfig
		if l.File, err = parseOverride(override); err != nil {
			return nil, err
		}
	} else {
		body, err := workingFile(repoDir)
		if err != nil {
			return nil, err
		}
		l.Source = SourceFile
		if l.File, err = Parse(body); err != nil {
			return nil, err
		}
	}
	subs, err := workingSubmodules(ctx, repoDir)
	if err != nil {
		return nil, err
	}
	if err := checkSubmodules(l.File, subs); err != nil {
		return nil, err
	}
	return l, nil
}

func parseOverride(b []byte) (*File, error) {
	f, err := Parse(b)
	if err != nil {
		var he *Error
		if errors.As(err, &he) && he.Code == CodeInvalid {
			for i, e := range he.Errors {
				he.Errors[i] = "repos.<slug>.harness: " + e
			}
		}
		return nil, err
	}
	return f, nil
}

func revParse(ctx context.Context, dir, rev string) (string, error) {
	out, err := gitOut(ctx, dir, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", rev, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// committedFile reads FilePath at commit: CodeNoFile when absent, CodeInvalid
// when it is not a regular file or is too large.
func committedFile(ctx context.Context, dir, commit string) ([]byte, error) {
	out, err := gitOut(ctx, dir, "ls-tree", commit, "--", FilePath)
	if err != nil {
		return nil, err
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return nil, noFile(commit[:min(12, len(commit))])
	}
	// <mode> SP <type> SP <object> TAB <path>
	fields := strings.Fields(line)
	if len(fields) < 3 || (fields[0] != "100644" && fields[0] != "100755") {
		return nil, invalid(FilePath + " must be a regular file")
	}
	obj := fields[2]
	size, err := gitOut(ctx, dir, "cat-file", "-s", obj)
	if err != nil {
		return nil, err
	}
	if n, err := strconv.Atoi(strings.TrimSpace(string(size))); err != nil || n > MaxFileBytes {
		return nil, invalid(fmt.Sprintf("the file is larger than %d bytes", MaxFileBytes))
	}
	return gitOut(ctx, dir, "cat-file", "blob", obj)
}

func workingFile(dir string) ([]byte, error) {
	p := filepath.Join(dir, FilePath)
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, noFile("the working tree")
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", FilePath, err)
	}
	if !fi.Mode().IsRegular() {
		return nil, invalid(FilePath + " must be a regular file")
	}
	if fi.Size() > MaxFileBytes {
		return nil, invalid(fmt.Sprintf("the file is larger than %d bytes", MaxFileBytes))
	}
	return os.ReadFile(p) //nolint:gosec // G304: fixed name inside the session's checkout
}

func noFile(where string) *Error {
	return &Error{Code: CodeNoFile, Msg: fmt.Sprintf("no %s in %s", FilePath, where)}
}

// DiffSHA256 is the hex sha256 of repoDir's uncommitted changes: the output of
// `git diff HEAD --binary`, then for each untracked file that is not ignored,
// in name order, its name and its content (a symlink's target, never followed).
// A tree with no changes hashes to the sha256 of nothing.
func DiffSHA256(ctx context.Context, repoDir string) (string, error) {
	h := sha256.New()
	if err := gitTo(ctx, repoDir, h, "diff", "HEAD", "--binary", "--no-color", "--no-ext-diff", "--no-textconv"); err != nil {
		return "", err
	}
	out, err := gitOut(ctx, repoDir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return "", err
	}
	var names []string
	for _, n := range strings.Split(string(out), "\x00") {
		if n != "" {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		if err := hashUntracked(h, repoDir, n); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashUntracked(h io.Writer, dir, name string) error {
	_, _ = io.WriteString(h, "\x00untracked\x00"+name+"\x00")
	p := filepath.Join(dir, name)
	fi, err := os.Lstat(p)
	if err != nil {
		return fmt.Errorf("hash untracked %s: %w", name, err)
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(p)
		if err != nil {
			return fmt.Errorf("hash untracked %s: %w", name, err)
		}
		_, _ = io.WriteString(h, "symlink\x00"+target)
	case fi.Mode().IsRegular():
		f, err := os.Open(p) //nolint:gosec // G304: a path git listed inside the checkout
		if err != nil {
			return fmt.Errorf("hash untracked %s: %w", name, err)
		}
		defer f.Close()
		if _, err := io.Copy(h, f); err != nil {
			return fmt.Errorf("hash untracked %s: %w", name, err)
		}
	default:
		// A nested repository or other non-file counts by name only.
	}
	return nil
}

func committedSubmodules(ctx context.Context, dir, commit string) ([]string, error) {
	out, err := gitOut(ctx, dir, "ls-tree", "--name-only", commit, "--", ".gitmodules")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(out)) == "" {
		return nil, nil
	}
	return submodulePaths(ctx, dir, "--blob", commit+":.gitmodules")
}

func workingSubmodules(ctx context.Context, dir string) ([]string, error) {
	p := filepath.Join(dir, ".gitmodules")
	if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
		return nil, nil // no readable .gitmodules is no submodule
	}
	return submodulePaths(ctx, dir, "-f", p)
}

// submodulePaths lists submodule.<name>.path values from a .gitmodules source.
func submodulePaths(ctx context.Context, dir, srcFlag, src string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "config", srcFlag, src, "--get-regexp", `^submodule\..*\.path$`) //nolint:gosec // G204: fixed git subcommand
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 && stderr.Len() == 0 {
		return nil, nil // no path entries
	}
	if err != nil {
		return nil, gitError("config", stderr.String(), err)
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if _, p, ok := strings.Cut(line, " "); ok && strings.TrimSpace(p) != "" {
			paths = append(paths, filepath.Clean(strings.TrimSpace(p)))
		}
	}
	return paths, nil
}

func checkSubmodules(f *File, subs []string) error {
	if len(subs) == 0 {
		return nil
	}
	for _, p := range f.Paths() {
		if sub, in := InSubmodule(p, subs); in {
			return &Error{Code: CodeSubmodule, Path: p, Msg: fmt.Sprintf("%s lies in submodule %s; submodules are not part of a harness run", p, sub)}
		}
	}
	return nil
}

// InSubmodule reports whether the repository path p is a submodule path in
// subs or lies under one, and names that submodule.
func InSubmodule(p string, subs []string) (string, bool) {
	p = filepath.Clean(p)
	for _, s := range subs {
		s = filepath.Clean(s)
		if p == s || strings.HasPrefix(p, s+string(filepath.Separator)) {
			return s, true
		}
	}
	return "", false
}

func gitOut(ctx context.Context, dir string, args ...string) ([]byte, error) {
	var out bytes.Buffer
	if err := gitTo(ctx, dir, &out, args...); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func gitTo(ctx context.Context, dir string, w io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...) //nolint:gosec // G204: fixed git subcommands; dir is a session's checkout
	var stderr bytes.Buffer
	cmd.Stdout = w
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return gitError(args[0], stderr.String(), err)
	}
	return nil
}

func gitError(sub, stderr string, err error) error {
	msg := strings.TrimSpace(stderr)
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("git %s: %s", sub, msg)
}
