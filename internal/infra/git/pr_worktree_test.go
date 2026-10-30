package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/local"
)

// A PR from a fork has no branch in origin; only pull/<n>/head reaches it.
func TestSetupRemotePRWorktreeFork(t *testing.T) {
	mgr := NewManager(nil)
	remote := &commandRecorderRemote{mockRemote: mockRemote{
		root: "/home/dev",
		dirs: map[string]bool{"/home/dev/r": true},
		outputs: map[string]string{
			`git -C "/home/dev/r" rev-parse --git-dir`:                      ".git",
			`git -C "/home/dev/r" rev-parse HEAD 2>/dev/null || echo EMPTY`: "abc",
		},
	}}
	wt, err := mgr.SetupRemotePRWorktree(context.Background(), remote, domain.Repo{Name: "o/r"}, 7, "main")
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(remote.commandsRun, "\n")
	for _, want := range []string{
		"fetch -q origin pull/7/head",
		"worktree add -q --detach '/home/dev/r/../r@pr-7' FETCH_HEAD",
		"merge-base HEAD 'origin/main'",
		"reset -q --soft",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("no command contains %q in:\n%s", want, all)
		}
	}
	if !strings.HasSuffix(wt.Path, "r@pr-7") || wt.Base != "main" {
		t.Fatalf("worktree %+v", wt)
	}
}

func TestSetupRemotePRWorktreeNeedsBase(t *testing.T) {
	remote := &commandRecorderRemote{mockRemote: mockRemote{root: "/home/dev"}}
	if _, err := NewManager(nil).SetupRemotePRWorktree(context.Background(), remote, domain.Repo{Name: "o/r"}, 7, " "); err == nil {
		t.Fatal("empty base accepted")
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// prUpstream makes an origin whose PR 7 exists only as refs/pull/7/head, with
// a main commit after the PR's fork point, and a clone of it at root/r.
func prUpstream(t *testing.T) (upstream, root string) {
	t.Helper()
	upstream, root = t.TempDir(), t.TempDir()
	runGit(t, upstream, "init", "-q", "-b", "main")
	runGit(t, upstream, "config", "user.name", "Aiman Test")
	runGit(t, upstream, "config", "user.email", "aiman@example.com")
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(upstream, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		runGit(t, upstream, "add", name)
		runGit(t, upstream, "commit", "-q", "-m", name)
	}
	write("main.txt", "base\n")
	runGit(t, upstream, "checkout", "-q", "-b", "fork")
	write("pr.txt", "pr\n")
	write("old.txt", "old\n")
	runGit(t, upstream, "update-ref", "refs/pull/7/head", "fork")
	runGit(t, upstream, "checkout", "-q", "main")
	runGit(t, upstream, "branch", "-q", "-D", "fork")
	write("main.txt", "later\n")
	runGit(t, root, "clone", "-q", upstream, "r")
	return upstream, root
}

func TestSetupRemotePRWorktreeStagesThePR(t *testing.T) {
	upstream, root := prUpstream(t)
	mgr := NewManager(nil)
	ex := local.NewExecutor(root)
	repo := domain.Repo{Name: "o/r", URL: upstream}
	wt, err := mgr.SetupRemotePRWorktree(context.Background(), ex, repo, 7, "main")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(wt.Path) != "r@pr-7" {
		t.Fatalf("path %s", wt.Path)
	}
	if got := gitOut(t, wt.Path, "diff", "--cached", "--name-only"); got != "old.txt\npr.txt" {
		t.Fatalf("staged %q", got)
	}
	if raw, _ := os.ReadFile(filepath.Join(wt.Path, "main.txt")); string(raw) != "base\n" {
		t.Fatalf("main.txt %q: the worktree is not the PR head", raw)
	}
	// Serve's files stay out of git status without touching .gitignore.
	if err := os.MkdirAll(filepath.Join(wt.Path, ".aiman_review"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, ".aiman_review", "meta.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := gitOut(t, wt.Path, "status", "--porcelain", "--untracked-files=all"); strings.Contains(got, ".aiman") || strings.Contains(got, ".gitignore") {
		t.Fatalf("status shows aiman files: %q", got)
	}

	// The head moves and drops old.txt: a second setup reuses the directory.
	runGit(t, upstream, "checkout", "-q", "--detach", "refs/pull/7/head")
	runGit(t, upstream, "rm", "-q", "old.txt")
	runGit(t, upstream, "commit", "-q", "-m", "drop old")
	runGit(t, upstream, "update-ref", "refs/pull/7/head", "HEAD")
	again, err := mgr.SetupRemotePRWorktree(context.Background(), ex, repo, 7, "main")
	if err != nil {
		t.Fatal(err)
	}
	if again.Path != wt.Path {
		t.Fatalf("path %s, want %s reused", again.Path, wt.Path)
	}
	if got := gitOut(t, wt.Path, "diff", "--cached", "--name-only"); got != "pr.txt" {
		t.Fatalf("staged after move %q", got)
	}
	if _, err := os.Stat(filepath.Join(wt.Path, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("old.txt left behind: %v", err)
	}
}
