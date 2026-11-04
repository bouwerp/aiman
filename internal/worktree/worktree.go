// Package worktree inspects and removes linked git worktrees for teardown.
// Everything runs git directly with no shell, and nothing here deletes a
// directory itself: git decides what a linked worktree is and removes it.
package worktree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/aimanskill"
	"github.com/bouwerp/aiman/internal/domain"
)

// Kind is what a session's worktree path turned out to be.
type Kind int

const (
	// KindNone is a missing directory, or one no git repository owns.
	KindNone Kind = iota
	// KindMain is a repository's main checkout. It is never removed.
	KindMain
	// KindOther is a directory inside a checkout, not the top of one.
	KindOther
	// KindLinked is a linked worktree, the only kind Remove accepts.
	KindLinked
)

// Info is the git state of one path.
type Info struct {
	Kind Kind
	// Root is the checkout's top level with symlinks resolved.
	Root string
	// CommonDir is the repository's shared git directory.
	CommonDir string
	// Branch is the checked-out branch; empty on a detached HEAD.
	Branch string
}

// Loss is what removing a worktree would destroy. aiman's own scaffolding does
// not count.
type Loss struct {
	ChangedFiles int
	// Paths holds at most MaxLossPaths of the changed files.
	Paths           []string
	UnpushedCommits int
}

// Empty reports that nothing would be lost.
func (l Loss) Empty() bool { return l.ChangedFiles == 0 && l.UnpushedCommits == 0 }

// MaxLossPaths bounds Loss.Paths.
const MaxLossPaths = 20

const gitTimeout = 30 * time.Second

// git runs one git command with the caller's GIT_* variables removed, so a
// serve started from inside a hook cannot point it at another repository.
func git(ctx context.Context, dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", full...) // #nosec G204 -- fixed git subcommands; no shell
	cmd.WaitDelay = time.Second
	cmd.Env = append(slices.DeleteFunc(os.Environ(), func(kv string) bool {
		return strings.HasPrefix(kv, "GIT_")
	}), "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", err
		}
		return "", fmt.Errorf("%s: %w", msg, err)
	}
	return stdout.String(), nil
}

// Inspect classifies p. A path git cannot read is KindNone, which removes
// nothing.
func Inspect(ctx context.Context, p string) Info {
	p = strings.TrimSpace(p)
	if p == "" {
		return Info{}
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return Info{}
	}
	out, err := git(ctx, real, "rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir", "--show-toplevel")
	if err != nil {
		return Info{}
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		return Info{}
	}
	gitDir, commonDir := filepath.Clean(lines[0]), filepath.Clean(lines[1])
	root, err := filepath.EvalSymlinks(lines[2])
	if err != nil {
		return Info{}
	}
	info := Info{Root: root, CommonDir: commonDir, Branch: currentBranch(ctx, root)}
	switch {
	case root != real:
		info.Kind = KindOther
	case gitDir == commonDir:
		info.Kind = KindMain
	case listedAsLinked(ctx, commonDir, root):
		info.Kind = KindLinked
	default:
		info.Kind = KindOther
	}
	return info
}

func currentBranch(ctx context.Context, root string) string {
	out, err := git(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// listedAsLinked asks the repository itself: `git worktree list` names the
// main checkout first, so root must be a later entry.
func listedAsLinked(ctx context.Context, commonDir, root string) bool {
	out, err := git(ctx, commonDir, "worktree", "list", "--porcelain")
	if err != nil {
		return false
	}
	first := true
	for line := range strings.SplitSeq(out, "\n") {
		listed, ok := strings.CutPrefix(line, "worktree ")
		if !ok {
			continue
		}
		if first {
			first = false
			continue
		}
		if real, err := filepath.EvalSymlinks(listed); err == nil && real == root {
			return true
		}
	}
	return false
}

// entry is one `git status --porcelain=v1` record.
type entry struct{ status, path string }

func status(ctx context.Context, root string) ([]entry, error) {
	out, err := git(ctx, root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	return parseStatus(out), nil
}

// parseStatus reads NUL-separated records. A rename or copy carries its source
// path as an extra field.
func parseStatus(out string) []entry {
	var entries []entry
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		e := entry{status: f[:2], path: f[3:]}
		if e.status[0] == 'R' || e.status[0] == 'C' {
			i++
		}
		entries = append(entries, e)
	}
	return entries
}

// isScaffold reports a file aiman or its graph tool generated in a worktree:
// .aiman_* anywhere, the bundled skill in an agent's directory, and
// graphify-out. A real worktree holds these even when it has no work in it, so
// they must not read as dirty.
func isScaffold(rel string) bool {
	rel = filepath.ToSlash(rel)
	base := path.Base(rel)
	if ok, err := path.Match(domain.AimanGeneratedFileGlob, base); err == nil && ok {
		return true
	}
	if rel == graphifyDir || strings.HasPrefix(rel, graphifyDir+"/") {
		return true
	}
	return aimanskill.IsProjectSkillPath(rel)
}

const graphifyDir = "graphify-out"

// Pending reports what removing the worktree would lose. An error means the
// worktree could not be read, which the caller must treat as not clean.
func Pending(ctx context.Context, info Info) (Loss, error) {
	entries, err := status(ctx, info.Root)
	if err != nil {
		return Loss{}, err
	}
	var loss Loss
	for _, e := range entries {
		if e.status == "??" && isScaffold(e.path) {
			continue
		}
		if e.path == ".gitignore" && gitignoreIsScaffold(ctx, info.Root, e.status) {
			continue
		}
		loss.ChangedFiles++
		if len(loss.Paths) < MaxLossPaths {
			loss.Paths = append(loss.Paths, e.path)
		}
	}
	loss.UnpushedCommits, err = unpushed(ctx, info.Root)
	if err != nil {
		return Loss{}, err
	}
	return loss, nil
}

// unpushed counts commits on HEAD that no remote-tracking ref contains. A
// worktree with no commit yet has none.
func unpushed(ctx context.Context, root string) (int, error) {
	if _, err := git(ctx, root, "rev-parse", "--quiet", "--verify", "HEAD"); err != nil {
		return 0, nil //nolint:nilerr // an unborn HEAD has no commits to lose
	}
	out, err := git(ctx, root, "rev-list", "--count", "HEAD", "--not", "--remotes")
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}

// gitignoreIsScaffold reports a .gitignore whose only uncommitted change is the
// ignore rule aiman appends at session create and restart.
func gitignoreIsScaffold(ctx context.Context, root, st string) bool {
	now, err := os.ReadFile(filepath.Join(root, ".gitignore")) // #nosec G304 -- inside a worktree git just listed
	if err != nil {
		return false
	}
	switch st {
	case "??":
		return onlyScaffoldIgnoreLines(string(now))
	case " M":
		head, err := git(ctx, root, "show", "HEAD:.gitignore")
		if err != nil {
			return false
		}
		added, ok := strings.CutPrefix(string(now), head)
		return ok && onlyScaffoldIgnoreLines(added)
	}
	return false
}

func onlyScaffoldIgnoreLines(text string) bool {
	for line := range strings.SplitSeq(text, "\n") {
		switch strings.TrimSpace(line) {
		case "", domain.AimanGeneratedFileGlob, domain.AimanGeneratedIgnoreComment:
		default:
			return false
		}
	}
	return true
}

// ErrNotLinked refuses a Remove of anything but a linked worktree.
var ErrNotLinked = errors.New("not a linked git worktree")

// Remove runs `git worktree remove`, leaving the branch in place. Without force
// git still refuses a worktree with changes in it, so a change made after the
// last Pending survives; aiman's scaffolding is cleared first because it would
// trip that refusal. A locked worktree stays, force or not.
func Remove(ctx context.Context, info Info, force bool) error {
	if info.Kind != KindLinked {
		return ErrNotLinked
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	} else if err := clearScaffold(ctx, info.Root); err != nil {
		return err
	}
	_, err := git(ctx, info.CommonDir, append(args, info.Root)...)
	return err
}

// clearScaffold restores an aiman-only .gitignore change, which un-ignores the
// .aiman_* files, then deletes the untracked scaffolding status now reports.
func clearScaffold(ctx context.Context, root string) error {
	entries, err := status(ctx, root)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.path != ".gitignore" || !gitignoreIsScaffold(ctx, root, e.status) {
			continue
		}
		if e.status == "??" {
			err = os.Remove(filepath.Join(root, ".gitignore"))
		} else {
			_, err = git(ctx, root, "checkout", "--", ".gitignore")
		}
		if err != nil {
			return err
		}
	}
	if entries, err = status(ctx, root); err != nil {
		return err
	}
	for _, e := range entries {
		if e.status != "??" || !isScaffold(e.path) || !filepath.IsLocal(e.path) {
			continue
		}
		if err := os.Remove(filepath.Join(root, e.path)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
