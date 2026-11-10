package git

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/local"
)

// session create --branch <b> where origin already has <b>: the worktree's
// branch must start at origin/<b> and track it, never be reset to the base.

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// remoteFixture builds an origin with main (two commits) and a pushed branch
// feat (one more commit), and a main checkout at <root>/demo cloned before
// feat was pushed, whose stale local feat sits at the initial commit.
type remoteFixture struct {
	root, origin, initial, featTip string
}

func newRemoteFixture(t *testing.T) remoteFixture {
	t.Helper()
	base := t.TempDir()
	origin := filepath.Join(base, "origin.git")
	gitRun(t, base, "init", "-q", "--bare", "-b", "main", origin)
	seed := filepath.Join(base, "seed")
	gitRun(t, base, "clone", "-q", origin, seed)
	gitRun(t, seed, "commit", "-q", "--allow-empty", "-m", "initial")
	initial := gitRun(t, seed, "rev-parse", "HEAD")
	gitRun(t, seed, "commit", "-q", "--allow-empty", "-m", "main work")
	gitRun(t, seed, "push", "-q", "origin", "main")

	root := filepath.Join(base, "repos")
	gitRun(t, base, "clone", "-q", origin, filepath.Join(root, "demo"))
	gitRun(t, filepath.Join(root, "demo"), "branch", "feat", initial)

	gitRun(t, seed, "switch", "-q", "-c", "feat")
	gitRun(t, seed, "commit", "-q", "--allow-empty", "-m", "feat work")
	gitRun(t, seed, "push", "-q", "origin", "feat")
	return remoteFixture{root: root, origin: origin, initial: initial, featTip: gitRun(t, seed, "rev-parse", "HEAD")}
}

func setupFeat(t *testing.T, f remoteFixture, base string) domain.Worktree {
	t.Helper()
	m := &Manager{}
	wt, err := m.SetupRemoteWorktree(context.Background(), local.NewExecutor(f.root),
		domain.Repo{Name: "owner/demo", URL: f.origin}, "feat", base)
	if err != nil {
		t.Fatalf("SetupRemoteWorktree: %v", err)
	}
	return wt
}

func assertAtOriginFeat(t *testing.T, f remoteFixture, wt domain.Worktree) {
	t.Helper()
	if head := gitRun(t, wt.Path, "rev-parse", "HEAD"); head != f.featTip {
		if head == f.initial {
			t.Fatalf("worktree HEAD is the stale initial commit %s, want origin/feat %s", head, f.featTip)
		}
		t.Fatalf("worktree HEAD = %s, want origin/feat %s", head, f.featTip)
	}
	if b := gitRun(t, wt.Path, "rev-parse", "--abbrev-ref", "HEAD"); b != "feat" {
		t.Fatalf("worktree branch = %q, want feat", b)
	}
	if up := gitRun(t, wt.Path, "rev-parse", "--abbrev-ref", "feat@{upstream}"); up != "origin/feat" {
		t.Fatalf("feat tracks %q, want origin/feat", up)
	}
}

func TestExistingRemoteBranchStartsAtOriginBranch(t *testing.T) {
	f := newRemoteFixture(t)
	assertAtOriginFeat(t, f, setupFeat(t, f, ""))
}

// A child's create passes the parent's base (here origin/main); a branch that
// exists on origin still wins over it.
func TestExistingRemoteBranchIgnoresExplicitBase(t *testing.T) {
	f := newRemoteFixture(t)
	assertAtOriginFeat(t, f, setupFeat(t, f, "origin/main"))
}

// A branch origin does not have is still cut from the base, as before.
func TestNewBranchStillCutFromBase(t *testing.T) {
	f := newRemoteFixture(t)
	m := &Manager{}
	wt, err := m.SetupRemoteWorktree(context.Background(), local.NewExecutor(f.root),
		domain.Repo{Name: "owner/demo", URL: f.origin}, "fresh", "origin/main")
	if err != nil {
		t.Fatalf("SetupRemoteWorktree: %v", err)
	}
	if head, want := gitRun(t, wt.Path, "rev-parse", "HEAD"), gitRun(t, wt.Path, "rev-parse", "origin/main"); head != want {
		t.Fatalf("new branch HEAD = %s, want origin/main %s", head, want)
	}
}
