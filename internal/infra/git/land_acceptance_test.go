package git

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ci-minutes rev 3, piece B: a child lands on its parent's branch with a
// local --no-ff merge pushed by sha. The child branch is never pushed, the
// default branch is never a target, and a dirty or unpushed parent is refused.

type landRepo struct {
	origin, clone, parent, child string
	root                         string
}

func landWrite(t *testing.T, dir, name, body, msg string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", name)
	runGit(t, dir, "commit", "-q", "-m", msg)
	return gitOut(t, dir, "rev-parse", "HEAD")
}

// newLandRepo makes a bare origin with main and feat, a main clone, the
// coordinator's worktree on feat and a child worktree on feat-t1.
func newLandRepo(t *testing.T) landRepo {
	t.Helper()
	root := t.TempDir()
	r := landRepo{root: root, origin: filepath.Join(root, "origin.git"), clone: filepath.Join(root, "repo")}
	runGit(t, root, "init", "-q", "--bare", "-b", "main", r.origin)
	runGit(t, root, "clone", "-q", r.origin, r.clone)
	for _, k := range [][2]string{{"user.name", "Aiman Test"}, {"user.email", "aiman@example.com"}} {
		runGit(t, r.clone, "config", k[0], k[1])
	}
	landWrite(t, r.clone, "a.txt", "base\n", "base")
	runGit(t, r.clone, "push", "-q", "origin", "HEAD:main")
	runGit(t, r.clone, "push", "-q", "origin", "HEAD:refs/heads/feat")
	runGit(t, r.clone, "fetch", "-q", "origin")
	r.parent = filepath.Join(root, "repo@feat")
	r.child = filepath.Join(root, "repo@feat-t1")
	runGit(t, r.clone, "worktree", "add", "-q", "-b", "feat", r.parent, "origin/feat")
	runGit(t, r.clone, "worktree", "add", "-q", "-b", "feat-t1", r.child, "origin/feat")
	return r
}

func (r landRepo) spec(childSHA string) LandSpec {
	return LandSpec{
		MainClone:      r.clone,
		ParentWorktree: r.parent,
		ParentBranch:   "feat",
		DefaultBranch:  "main",
		ChildSHA:       childSHA,
		ScratchDir:     filepath.Join(r.root, "land-scratch"),
		LockPath:       filepath.Join(r.root, "locks", "o__r@feat.lock"),
		CommitterName:  "aiman",
		CommitterEmail: "aiman@localhost",
		Message:        "Land child t1",
	}
}

func landCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func originRef(t *testing.T, r landRepo, ref string) string {
	t.Helper()
	return gitOut(t, r.origin, "rev-parse", "--verify", "-q", ref)
}

func TestLandMergesTheChildBySha(t *testing.T) {
	r := newLandRepo(t)
	before := originRef(t, r, "refs/heads/feat")
	child := landWrite(t, r.child, "b.txt", "child\n", "child work")

	res, err := Land(landCtx(t), r.spec(child))
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	after := originRef(t, r, "refs/heads/feat")
	if after != res.MergeSHA {
		t.Fatalf("origin/feat = %s, want the merge %s", after, res.MergeSHA)
	}
	parents := strings.Fields(gitOut(t, r.origin, "rev-list", "--parents", "-n", "1", res.MergeSHA))
	if len(parents) != 3 || parents[1] != before || parents[2] != child {
		t.Fatalf("merge parents = %v, want [merge %s %s] (a --no-ff merge)", parents, before, child)
	}
	if c := gitOut(t, r.origin, "log", "-1", "--format=%cn <%ce>", res.MergeSHA); c != "aiman <aiman@localhost>" {
		t.Fatalf("committer = %q, want aiman's identity", c)
	}
	if out, _ := runGitAllowFail(r.origin, "rev-parse", "--verify", "-q", "refs/heads/feat-t1"); out != "" {
		t.Fatal("the child branch was pushed; only the merge may reach origin")
	}
	assertScratchGone(t, r)
}

func TestLandConflictLeavesOriginAlone(t *testing.T) {
	r := newLandRepo(t)
	// The parent moves a.txt on origin; the child changes the same line.
	landWrite(t, r.parent, "a.txt", "parent\n", "parent edit")
	runGit(t, r.parent, "push", "-q", "origin", "feat")
	before := originRef(t, r, "refs/heads/feat")
	child := landWrite(t, r.child, "a.txt", "child\n", "child edit")

	_, err := Land(landCtx(t), r.spec(child))
	var conflict *LandConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("err = %v, want *LandConflictError", err)
	}
	if len(conflict.Files) != 1 || conflict.Files[0] != "a.txt" {
		t.Fatalf("conflict files = %v, want [a.txt]", conflict.Files)
	}
	if after := originRef(t, r, "refs/heads/feat"); after != before {
		t.Fatalf("origin/feat moved to %s on a conflict", after)
	}
	assertScratchGone(t, r)
}

func TestLandRefusesADivergedParent(t *testing.T) {
	cases := map[string]func(t *testing.T, r landRepo){
		"unpushed commit": func(t *testing.T, r landRepo) { landWrite(t, r.parent, "p.txt", "p\n", "local only") },
		"tracked change": func(t *testing.T, r landRepo) {
			if err := os.WriteFile(filepath.Join(r.parent, "a.txt"), []byte("dirty\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"other branch": func(t *testing.T, r landRepo) { runGit(t, r.parent, "checkout", "-q", "-b", "elsewhere") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			r := newLandRepo(t)
			child := landWrite(t, r.child, "b.txt", "child\n", "child work")
			before := originRef(t, r, "refs/heads/feat")
			mutate(t, r)
			_, err := Land(landCtx(t), r.spec(child))
			if !errors.Is(err, ErrLandParentDiverged) {
				t.Fatalf("err = %v, want ErrLandParentDiverged", err)
			}
			if after := originRef(t, r, "refs/heads/feat"); after != before {
				t.Fatal("origin/feat moved for a diverged parent")
			}
		})
	}
}

func TestLandNeverTargetsTheDefaultBranch(t *testing.T) {
	r := newLandRepo(t)
	child := landWrite(t, r.child, "b.txt", "child\n", "child work")
	s := r.spec(child)
	s.ParentBranch = "main"
	before := originRef(t, r, "refs/heads/main")
	if _, err := Land(landCtx(t), s); !errors.Is(err, ErrLandDefaultBranch) {
		t.Fatalf("err = %v, want ErrLandDefaultBranch", err)
	}
	if after := originRef(t, r, "refs/heads/main"); after != before {
		t.Fatal("origin/main moved")
	}
}

func TestConcurrentLandsSerialiseAndBothLand(t *testing.T) {
	r := newLandRepo(t)
	child2 := filepath.Join(r.root, "repo@feat-t2")
	runGit(t, r.clone, "worktree", "add", "-q", "-b", "feat-t2", child2, "origin/feat")
	c1 := landWrite(t, r.child, "b.txt", "one\n", "child one")
	c2 := landWrite(t, child2, "c.txt", "two\n", "child two")

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, sha := range []string{c1, c2} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := r.spec(sha)
			s.ScratchDir = filepath.Join(r.root, "land-scratch", sha[:8])
			_, errs[i] = Land(landCtx(t), s)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("land %d: %v", i, err)
		}
	}
	tip := originRef(t, r, "refs/heads/feat")
	for _, sha := range []string{c1, c2} {
		if _, err := runGitAllowFail(r.origin, "merge-base", "--is-ancestor", sha, tip); err != nil {
			t.Fatalf("%s is not on origin/feat after both lands", sha[:8])
		}
	}
}

func assertScratchGone(t *testing.T, r landRepo) {
	t.Helper()
	entries, _ := os.ReadDir(filepath.Join(r.root, "land-scratch"))
	if len(entries) != 0 {
		t.Fatalf("scratch tree left behind: %v", entries)
	}
	if wt := gitOut(t, r.clone, "worktree", "list", "--porcelain"); strings.Contains(wt, "land-scratch") {
		t.Fatalf("scratch worktree still registered:\n%s", wt)
	}
}

func runGitAllowFail(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestLandedOnSeesTheMergeAfterAFetch(t *testing.T) {
	r := newLandRepo(t)
	child := landWrite(t, r.child, "b.txt", "child\n", "child work")
	ctx := landCtx(t)
	if ok, err := LandedOn(ctx, r.clone, "feat", child); err != nil || ok {
		t.Fatalf("LandedOn before land = %v, %v; want false", ok, err)
	}
	res, err := Land(ctx, r.spec(child))
	if err != nil {
		t.Fatal(err)
	}
	// Another clone moves feat on; LandedOn must fetch to see it still holds the merge.
	other := filepath.Join(r.root, "other")
	runGit(t, r.root, "clone", "-q", "-b", "feat", r.origin, other)
	runGit(t, other, "config", "user.name", "x")
	runGit(t, other, "config", "user.email", "x@example.com")
	landWrite(t, other, "z.txt", "z\n", "later")
	runGit(t, other, "push", "-q", "origin", "feat")
	if ok, err := LandedOn(ctx, r.clone, "feat", res.MergeSHA); err != nil || !ok {
		t.Fatalf("LandedOn after land = %v, %v; want true", ok, err)
	}
}
