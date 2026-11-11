package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// landPushRetries is how many times a land refetches and remerges after the
// push was rejected because the parent branch moved.
const landPushRetries = 3

var (
	// ErrLandDefaultBranch: land never pushes to the repository's default
	// branch.
	ErrLandDefaultBranch = errors.New("land never targets the default branch")
	// ErrLandParentDiverged: the coordinator's worktree is not on the parent
	// branch, holds commits origin lacks, has tracked changes, or git could
	// not answer.
	ErrLandParentDiverged = errors.New("the parent worktree is not level with origin")
	// ErrLandBusy: the parent branch kept moving while the merge was pushed.
	ErrLandBusy = errors.New("the parent branch kept moving; land again")
)

// LandConflictError is a child sha that does not merge cleanly onto the
// parent branch. Files are the conflicted paths.
type LandConflictError struct {
	Files []string
}

func (e *LandConflictError) Error() string {
	return "merge conflict in " + strings.Join(e.Files, ", ")
}

// LandSpec is one child landing on its parent's branch. Every path is local
// to the host that runs Land.
type LandSpec struct {
	// MainClone is the repository's main clone; worktrees share its objects
	// and remote-tracking refs, and the push goes out from it.
	MainClone string
	// ParentWorktree is the coordinator's worktree, checked for divergence.
	ParentWorktree string
	ParentBranch   string
	// DefaultBranch is the repository's default branch. Empty reads
	// origin/HEAD in the main clone.
	DefaultBranch string
	ChildSHA      string
	// ScratchDir is the detached worktree the merge is made in. It is
	// removed on every path.
	ScratchDir string
	// LockPath is the per repository and branch lock file, held across
	// fetch, parent check, merge and push.
	LockPath       string
	CommitterName  string
	CommitterEmail string
	Message        string
}

// LandResult is a landed child.
type LandResult struct {
	MergeSHA string
}

// Land merges spec.ChildSHA with --no-ff onto origin/<parent> in a detached
// scratch worktree and pushes the merge sha to the parent branch from the
// main clone, hooks off and never forced. The child branch is never pushed.
func Land(ctx context.Context, spec LandSpec) (LandResult, error) {
	if err := spec.validate(); err != nil {
		return LandResult{}, err
	}
	def := spec.DefaultBranch
	if def == "" {
		var err error
		if def, err = DefaultBranch(ctx, spec.MainClone); err != nil {
			return LandResult{}, err
		}
	}
	if spec.ParentBranch == def {
		return LandResult{}, fmt.Errorf("%w: %s", ErrLandDefaultBranch, def)
	}
	unlock, err := lockLand(ctx, spec.LockPath)
	if err != nil {
		return LandResult{}, err
	}
	defer unlock()

	for attempt := 0; ; attempt++ {
		sha, err := spec.landOnce(ctx)
		if err == nil {
			return LandResult{MergeSHA: sha}, nil
		}
		if !errors.Is(err, errPushRejected) {
			return LandResult{}, err
		}
		if attempt >= landPushRetries {
			return LandResult{}, fmt.Errorf("%w: %v", ErrLandBusy, err)
		}
	}
}

var errPushRejected = errors.New("push rejected")

// DefaultBranch reads origin/HEAD in mainClone.
func DefaultBranch(ctx context.Context, mainClone string) (string, error) {
	out, err := landGit(ctx, mainClone, nil, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", fmt.Errorf("read the default branch: %w", err)
	}
	return strings.TrimPrefix(strings.TrimSpace(out), "origin/"), nil
}

// LandLockName is the lock file name for repo (owner/name) and branch:
// owner__name@branch.lock, with the branch flattened as worktree
// directories are.
func LandLockName(repo, branch string) (string, error) {
	b, err := worktreeBranchDir(branch)
	if err != nil {
		return "", err
	}
	r := strings.ReplaceAll(strings.TrimSpace(repo), "/", "__")
	if r == "" || strings.ContainsAny(r, `\`) || r == "." || r == ".." {
		return "", fmt.Errorf("land lock: bad repository %q", repo)
	}
	return r + worktreeDirSeparator + b + ".lock", nil
}

func (s LandSpec) validate() error {
	for name, v := range map[string]string{
		"main clone": s.MainClone, "parent worktree": s.ParentWorktree, "parent branch": s.ParentBranch,
		"child sha": s.ChildSHA, "scratch dir": s.ScratchDir, "lock path": s.LockPath,
	} {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("land: %s is required", name)
		}
	}
	return nil
}

// landOnce runs one fetch, parent check, merge and push. A push git rejects
// is errPushRejected.
func (s LandSpec) landOnce(ctx context.Context) (string, error) {
	if _, err := landGit(ctx, s.MainClone, nil, "fetch", "-q", "--no-tags", "origin"); err != nil {
		return "", fmt.Errorf("fetch origin: %w", err)
	}
	if err := s.checkParent(ctx); err != nil {
		return "", err
	}
	sha, err := s.merge(ctx)
	if err != nil {
		return "", err
	}
	out, err := landGit(ctx, s.MainClone, []string{"LC_ALL=C"},
		"-c", "core.hooksPath=/dev/null", "push", "origin", sha+":refs/heads/"+s.ParentBranch)
	if err != nil {
		if strings.Contains(out+err.Error(), "[rejected]") {
			return "", fmt.Errorf("%w: %v", errPushRejected, err)
		}
		return "", fmt.Errorf("push %s: %w", s.ParentBranch, err)
	}
	return sha, nil
}

// checkParent refuses when the coordinator's worktree is not on the parent
// branch, is ahead of origin, has tracked changes, or git cannot answer
// (an index.lock while the coordinator commits).
func (s LandSpec) checkParent(ctx context.Context) error {
	head, err := landGit(ctx, s.ParentWorktree, nil, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrLandParentDiverged, err)
	}
	if want := "refs/heads/" + s.ParentBranch; strings.TrimSpace(head) != want {
		return fmt.Errorf("%w: worktree is on %s, not %s", ErrLandParentDiverged, strings.TrimSpace(head), want)
	}
	ahead, err := landGit(ctx, s.ParentWorktree, nil, "rev-list", "origin/"+s.ParentBranch+"..HEAD")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrLandParentDiverged, err)
	}
	if strings.TrimSpace(ahead) != "" {
		return fmt.Errorf("%w: unpushed commits on %s", ErrLandParentDiverged, s.ParentBranch)
	}
	status, err := landGit(ctx, s.ParentWorktree, nil, "status", "--porcelain", "--untracked-files=no")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrLandParentDiverged, err)
	}
	if strings.TrimSpace(status) != "" {
		return fmt.Errorf("%w: tracked changes in %s", ErrLandParentDiverged, s.ParentWorktree)
	}
	return nil
}

// merge makes the --no-ff merge in a detached scratch worktree at
// origin/<parent> and returns its sha. The scratch tree is removed whatever
// happens.
func (s LandSpec) merge(ctx context.Context) (string, error) {
	s.removeScratch()
	if err := os.MkdirAll(filepath.Dir(s.ScratchDir), 0o700); err != nil {
		return "", fmt.Errorf("land scratch dir: %w", err)
	}
	if _, err := landGit(ctx, s.MainClone, nil, "worktree", "add", "-q", "--detach", s.ScratchDir, "origin/"+s.ParentBranch); err != nil {
		return "", fmt.Errorf("add scratch worktree: %w", err)
	}
	defer s.removeScratch()

	name, email := s.CommitterName, s.CommitterEmail
	if name == "" {
		name = "Aiman"
	}
	if email == "" {
		email = "aiman@localhost"
	}
	env := []string{
		"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=" + email,
	}
	msg := s.Message
	if msg == "" {
		msg = "Land " + s.ChildSHA + " on " + s.ParentBranch
	}
	if _, err := landGit(ctx, s.ScratchDir, env, "-c", "core.hooksPath=/dev/null", "merge", "--no-ff", "--no-edit", "-m", msg, s.ChildSHA); err != nil {
		files, ferr := landGit(ctx, s.ScratchDir, nil, "diff", "--name-only", "--diff-filter=U")
		if ferr == nil && strings.TrimSpace(files) != "" {
			_, _ = landGit(ctx, s.ScratchDir, nil, "merge", "--abort")
			return "", &LandConflictError{Files: strings.Fields(files)}
		}
		return "", fmt.Errorf("merge %s: %w", s.ChildSHA, err)
	}
	sha, err := landGit(ctx, s.ScratchDir, nil, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sha), nil
}

// removeScratch removes the scratch worktree and its registration. It runs
// on a fresh context so a cancelled land still cleans up.
func (s LandSpec) removeScratch() {
	ctx := context.Background()
	_, _ = landGit(ctx, s.MainClone, nil, "worktree", "remove", "--force", s.ScratchDir)
	_ = os.RemoveAll(s.ScratchDir)
	_, _ = landGit(ctx, s.MainClone, nil, "worktree", "prune")
}

// LandedOn fetches the branch into mainClone and reports whether sha is an
// ancestor of origin/<branch>.
func LandedOn(ctx context.Context, mainClone, branch, sha string) (bool, error) {
	if sha == "" || branch == "" {
		return false, nil
	}
	if _, err := landGit(ctx, mainClone, nil, "fetch", "-q", "--no-tags", "origin"); err != nil {
		return false, fmt.Errorf("fetch origin: %w", err)
	}
	cmd := exec.CommandContext(ctx, "git", "-C", mainClone, "merge-base", "--is-ancestor", sha, "origin/"+branch)
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	// 128: sha unknown to this clone, which also means it has not landed.
	return false, nil
}

// landGit runs git in dir with extra environment and returns stdout. The
// error carries stderr.
func landGit(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	// #nosec G204 -- argv only, no shell.
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Env = append(cmd.Env, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		return stdout.String(), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return stdout.String(), nil
}
