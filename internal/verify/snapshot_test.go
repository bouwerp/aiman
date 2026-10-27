package verify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

func newTestRepo(t *testing.T) string {
	t.Helper()
	gitAvailable(t)
	dir := t.TempDir()
	runTestGit(t, dir, "init", "-b", "master")
	runTestGit(t, dir, "config", "user.email", "test@example.com")
	runTestGit(t, dir, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, dir, "add", ".")
	runTestGit(t, dir, "commit", "-m", "base")
	return dir
}

func runTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestTakeSnapshotIncludesUncommittedAndUntracked(t *testing.T) {
	repo := newTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("new\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("*.log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "ignored.log"), []byte("ignored\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := runTestGit(t, repo, "status", "--porcelain")
	snap, err := TakeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	tree := runTestGit(t, repo, "ls-tree", "-r", "--name-only", snap.Commit)
	for _, want := range []string{"tracked.txt", "new.txt", ".gitignore"} {
		if !strings.Contains(tree, want) {
			t.Fatalf("tree missing %q:\n%s", want, tree)
		}
	}
	if strings.Contains(tree, "ignored.log") {
		t.Fatalf("ignored file entered the snapshot:\n%s", tree)
	}
	if got := runTestGit(t, repo, "show", snap.Commit+":tracked.txt"); got != "changed" {
		t.Fatalf("snapshot tracked content = %q", got)
	}
	if after := runTestGit(t, repo, "status", "--porcelain"); after != before {
		t.Fatalf("real index changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestTakeSnapshotSameTreeTwice(t *testing.T) {
	repo := newTestRepo(t)
	first, err := TakeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	second, err := TakeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if first.Tree != second.Tree {
		t.Fatalf("trees differ: %q vs %q", first.Tree, second.Tree)
	}
}

func TestAddAndRemoveDetachedWorktree(t *testing.T) {
	repo := newTestRepo(t)
	snap, err := TakeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "review")
	if err := AddDetachedWorktree(context.Background(), repo, path, snap.Commit); err != nil {
		t.Fatal(err)
	}
	if got := runTestGit(t, path, "rev-parse", "HEAD"); got != snap.Commit {
		t.Fatalf("HEAD = %q, want %q", got, snap.Commit)
	}
	if !strings.Contains(runTestGit(t, repo, "worktree", "list", "--porcelain"), path) {
		t.Fatal("worktree list does not contain the review path")
	}
	if err := RemoveWorktree(context.Background(), repo, path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("review path still exists: %v", err)
	}
	if strings.Contains(runTestGit(t, repo, "worktree", "list", "--porcelain"), path) {
		t.Fatal("worktree list still contains the review path")
	}
}

func TestResolveBaseFallsBackToOriginHead(t *testing.T) {
	repo := newTestRepo(t)
	head := runTestGit(t, repo, "rev-parse", "HEAD")
	runTestGit(t, repo, "update-ref", "refs/remotes/origin/main", head)

	got, err := ResolveBase(context.Background(), repo, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != head {
		t.Fatalf("empty base = %q, want %q", got, head)
	}
	got, err = ResolveBase(context.Background(), repo, "main")
	if err != nil {
		t.Fatal(err)
	}
	if got != head {
		t.Fatalf("named base = %q, want %q", got, head)
	}
}

func TestDiffAgainstTruncates(t *testing.T) {
	repo := newTestRepo(t)
	base := runTestGit(t, repo, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "tracked.txt"), []byte(strings.Repeat("changed\n", 1000)), 0o600); err != nil {
		t.Fatal(err)
	}
	snap, err := TakeSnapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	diff, err := DiffAgainst(context.Background(), repo, base, snap.Commit, 64)
	if err != nil {
		t.Fatal(err)
	}
	if !diff.Truncated || len(diff.Patch) > 64 || diff.Stat == "" || diff.MergeBase != base {
		t.Fatalf("diff = truncated %v len %d stat %q base %q", diff.Truncated, len(diff.Patch), diff.Stat, diff.MergeBase)
	}
}
