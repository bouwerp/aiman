package server

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/cigate"
	"github.com/bouwerp/aiman/internal/ciwatch"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

func ciGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// ciRepo is a committed checkout on branch feat-x whose gate list is gate.
func ciRepo(t *testing.T, gate string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "wt")
	if err := os.MkdirAll(filepath.Join(dir, ".aiman"), 0o750); err != nil {
		t.Fatal(err)
	}
	ciGit(t, dir, "init", "-q", "-b", "feat-x")
	ciGit(t, dir, "config", "user.name", "T")
	ciGit(t, dir, "config", "user.email", "t@example.com")
	if err := os.WriteFile(filepath.Join(dir, ".aiman", "ci-local.yaml"), []byte(gate), 0o600); err != nil {
		t.Fatal(err)
	}
	ciGit(t, dir, "add", "-A")
	ciGit(t, dir, "commit", "-q", "-m", "init")
	return dir
}

type fakePRLookup struct {
	pr    *cigate.PR
	err   error
	calls int
}

func (f *fakePRLookup) lookup(context.Context, string, string) (*cigate.PR, error) {
	f.calls++
	return f.pr, f.err
}

func ciLocalServer(t *testing.T, gate string, sess domain.Session, lookup *fakePRLookup) (*Server, string) {
	t.Helper()
	dir := ciRepo(t, gate)
	sess.WorktreePath = dir
	sess.RepoName = "o/r"
	srv, _, _ := todoServer(t, sess)
	srv.SetCILocal(CILocalDeps{
		Runner:   &cigate.Runner{Dir: filepath.Join(t.TempDir(), "ci-local"), MaxParallel: 2},
		Repos:    map[string]config.RepoBootstrap{},
		LookupPR: lookup.lookup,
	})
	return srv, dir
}

type ciGateResult struct {
	Allow  bool       `json:"allow"`
	Reason string     `json:"reason"`
	PR     *cigate.PR `json:"pr"`
}

func TestCILocalWaitThenGatePasses(t *testing.T) {
	lookup := &fakePRLookup{pr: &cigate.PR{Number: 3, State: "OPEN"}}
	srv, _ := ciLocalServer(t, "commands:\n  - name: ok\n    run: \"true\"\n", todoSession("s1"), lookup)

	var gate ciGateResult
	rpcOK(t, srv, "", "ci.gate", map[string]any{"session": "s1", "kind": "push"}, &gate)
	if gate.Allow || gate.PR == nil || gate.PR.Number != 3 {
		t.Fatalf("push before a run: %+v, want refused with the PR", gate)
	}

	var local struct {
		Record cigate.Record `json:"record"`
		Done   bool          `json:"done"`
	}
	rpcOK(t, srv, "", "ci.local", map[string]any{"session": "s1", "wait": true}, &local)
	if !local.Done || local.Record.Status != cigate.StatusPass {
		t.Fatalf("ci.local = %+v", local)
	}
	var status struct {
		Status CIStatus `json:"status"`
	}
	rpcOK(t, srv, "", "ci.status", map[string]any{"session": "s1"}, &status)
	if status.Status.Record == nil || status.Status.Record.Status != cigate.StatusPass || !status.Status.HasList || !status.Status.Clean {
		t.Fatalf("ci.status = %+v", status.Status)
	}
	rpcOK(t, srv, "", "ci.gate", map[string]any{"session": "s1", "kind": "push", "command": "git push origin feat-x"}, &gate)
	if !gate.Allow {
		t.Fatalf("push after a pass: %+v", gate)
	}
	if lookup.calls != 1 {
		t.Fatalf("gh asked %d times, want 1 (the second answer comes from the cache)", lookup.calls)
	}
	rpcOK(t, srv, "", "ci.gate", map[string]any{"session": "s1", "kind": "push", "command": "git push origin other"}, &gate)
	if gate.Allow || gate.Reason != cigate.PushRefusal {
		t.Fatalf("push of another branch: %+v", gate)
	}
}

func TestCIStatusShowsTheFailedTail(t *testing.T) {
	srv, _ := ciLocalServer(t, "commands:\n  - name: bad\n    run: echo boom; exit 4\n", todoSession("s1"), &fakePRLookup{})
	rpcOK(t, srv, "", "ci.local", map[string]any{"session": "s1", "wait": true}, nil)
	var status struct {
		Status CIStatus `json:"status"`
	}
	rpcOK(t, srv, "", "ci.status", map[string]any{"session": "s1"}, &status)
	r := status.Status.Record
	if r == nil || r.Status != cigate.StatusFail || r.Failed != "bad" || !strings.Contains(status.Status.Tail, "boom") {
		t.Fatalf("status = %+v", status.Status)
	}
}

func TestCILocalRefusesDirtyAndMissingList(t *testing.T) {
	srv, dir := ciLocalServer(t, "commands:\n  - name: ok\n    run: \"true\"\n", todoSession("s1"), &fakePRLookup{})
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if resp := rpc(t, srv, "", "ci.local", map[string]any{"session": "s1"}); resp.Error == nil || resp.Error.Code != CodeCIDirty {
		t.Fatalf("dirty: %+v", resp.Error)
	}
	ciGit(t, dir, "rm", "-q", ".aiman/ci-local.yaml")
	if err := os.Remove(filepath.Join(dir, "new.txt")); err != nil {
		t.Fatal(err)
	}
	ciGit(t, dir, "commit", "-q", "-m", "drop list")
	if resp := rpc(t, srv, "", "ci.local", map[string]any{"session": "s1"}); resp.Error == nil || resp.Error.Code != CodeCINoList {
		t.Fatalf("no list: %+v", resp.Error)
	}
}

func TestCIGateCreateForAChild(t *testing.T) {
	child := todoSession("c1")
	child.ParentID = "p1"
	srv, _ := ciLocalServer(t, "commands:\n  - name: ok\n    run: \"true\"\n", child, &fakePRLookup{})
	var gate ciGateResult
	rpcOK(t, srv, "", "ci.gate", map[string]any{"session": "c1", "kind": "create", "draft": true}, &gate)
	if gate.Allow || !strings.Contains(gate.Reason, "aiman land") {
		t.Fatalf("child create: %+v", gate)
	}
}

func TestCIGateLookupFailureFallsBackToTheWatchersPR(t *testing.T) {
	lookup := &fakePRLookup{err: errors.New("gh down")}
	srv, dir := ciLocalServer(t, "commands:\n  - name: ok\n    run: \"true\"\n", todoSession("s1"), lookup)
	var gate ciGateResult
	rpcOK(t, srv, "", "ci.gate", map[string]any{"session": "s1", "kind": "push"}, &gate)
	if gate.Allow || gate.PR != nil {
		t.Fatalf("failed lookup with a list: %+v, want gated", gate)
	}
	gh := srv.PRCacheGitHub(stubPRView{v: ciwatch.PRView{Number: 9, State: "OPEN"}})
	if _, err := gh.PRView(context.Background(), dir); err != nil {
		t.Fatal(err)
	}
	rpcOK(t, srv, "", "ci.gate", map[string]any{"session": "s1", "kind": "push"}, &gate)
	if gate.Allow || gate.PR == nil || gate.PR.Number != 9 {
		t.Fatalf("watcher-seen PR: %+v", gate)
	}
}

func TestCILocalUnavailableWithoutRunner(t *testing.T) {
	srv, _, _ := todoServer(t, todoSession("s1"))
	if resp := rpc(t, srv, "", "ci.status", map[string]any{"session": "s1"}); resp.Error == nil || resp.Error.Code != CodeUnavailable {
		t.Fatalf("resp %+v", resp.Error)
	}
}

func TestPRCacheTTLAndObserve(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	c := newPRCache(func() time.Time { return now })
	c.put("k", &cigate.PR{Number: 1, State: "OPEN", Draft: true})
	if pr, ok := c.fresh("k"); !ok || !pr.Draft {
		t.Fatalf("fresh = %+v %v", pr, ok)
	}
	c.observe("k", &cigate.PR{Number: 1, State: "OPEN"})
	if pr, ok := c.fresh("k"); !ok || !pr.Draft {
		t.Fatal("a watcher read of the same PR must not drop the draft flag")
	}
	now = now.Add(prCacheTTL)
	if _, ok := c.fresh("k"); ok {
		t.Fatal("entry still fresh after the TTL")
	}
	c.observe("k", &cigate.PR{Number: 2, State: "OPEN"})
	if _, ok := c.fresh("k"); ok {
		t.Fatal("a watcher entry is never fresh")
	}
}

type stubPRView struct {
	noGitHub
	v ciwatch.PRView
}

func (s stubPRView) PRView(context.Context, string) (ciwatch.PRView, error) { return s.v, nil }

// A release tag push is not a branch push: no local gate, no PR lookup.
func TestCIGateAllowsAnExistingTagPush(t *testing.T) {
	lookup := &fakePRLookup{pr: &cigate.PR{Number: 3, State: "OPEN"}}
	srv, dir := ciLocalServer(t, "commands:\n  - name: ok\n    run: \"false\"\n", todoSession("s1"), lookup)
	ciGit(t, dir, "tag", "v1.2.3")

	var gate ciGateResult
	for _, cmd := range []string{"git push origin v1.2.3", "git push origin refs/tags/v1.2.3", "git push -q origin v1.2.3"} {
		rpcOK(t, srv, "", "ci.gate", map[string]any{"session": "s1", "kind": "push", "command": cmd}, &gate)
		if !gate.Allow {
			t.Fatalf("%q: %+v, want allowed", cmd, gate)
		}
	}
	if lookup.calls != 0 {
		t.Fatalf("a tag push asked gh %d times", lookup.calls)
	}
	for _, cmd := range []string{"git push origin v9.9.9", "git push --force origin v1.2.3", "git push origin v1.2.3 feat-x", "git push origin v1.2.3:v1.2.3x"} {
		rpcOK(t, srv, "", "ci.gate", map[string]any{"session": "s1", "kind": "push", "command": cmd}, &gate)
		if gate.Allow {
			t.Fatalf("%q allowed, want refused", cmd)
		}
	}
}
