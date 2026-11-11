package server

// Acceptance tests for ci-minutes piece C (docs/specs/ci-minutes.md,
// Behaviour 2): ci.ready, the RPC behind aiman pr ready.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/cigate"
	"github.com/bouwerp/aiman/internal/infra/config"
)

type fakeMarkReady struct {
	calls []int
	err   error
}

func (f *fakeMarkReady) mark(_ context.Context, _ string, number int) error {
	f.calls = append(f.calls, number)
	return f.err
}

func ciReadyServer(t *testing.T, gate string, lookup *fakePRLookup, mark *fakeMarkReady) (*Server, string) {
	t.Helper()
	dir := ciRepo(t, gate)
	sess := todoSession("s1")
	sess.WorktreePath = dir
	sess.RepoName = "o/r"
	srv, _, _ := todoServer(t, sess)
	srv.SetCILocal(CILocalDeps{
		Runner:    &cigate.Runner{Dir: filepath.Join(t.TempDir(), "ci-local"), MaxParallel: 2},
		Repos:     map[string]config.RepoBootstrap{"o/r": {CI: config.RepoCI{DraftsSkipCI: true}}},
		LookupPR:  lookup.lookup,
		MarkReady: mark.mark,
	})
	return srv, dir
}

func readyHeadSHA(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func passGate(t *testing.T, srv *Server) {
	t.Helper()
	var local struct {
		Record cigate.Record `json:"record"`
		Done   bool          `json:"done"`
	}
	rpcOK(t, srv, "", "ci.local", map[string]any{"session": "s1", "wait": true}, &local)
	if !local.Done || local.Record.Status != cigate.StatusPass {
		t.Fatalf("ci.local = %+v", local)
	}
}

func wantCINotReady(t *testing.T, srv *Server, mark *fakeMarkReady, why string) {
	t.Helper()
	resp := rpc(t, srv, "", "ci.ready", map[string]any{"session": "s1"})
	if resp.Error == nil || resp.Error.Code != CodeCINotReady {
		t.Fatalf("%s: %+v, want %s", why, resp.Error, CodeCINotReady)
	}
	if len(mark.calls) != 0 {
		t.Fatalf("%s: gh pr ready ran %v", why, mark.calls)
	}
}

const passingGate = "commands:\n  - name: ok\n    run: \"true\"\n"

func TestCIReadyMarksTheDraftReady(t *testing.T) {
	lookup := &fakePRLookup{}
	mark := &fakeMarkReady{}
	srv, dir := ciReadyServer(t, passingGate, lookup, mark)
	passGate(t, srv)
	lookup.pr = &cigate.PR{Number: 9, Draft: true, State: "OPEN", HeadSHA: readyHeadSHA(t, dir)}

	var res struct {
		Type   string `json:"type"`
		Ready  bool   `json:"ready"`
		Number int    `json:"number"`
	}
	rpcOK(t, srv, "", "ci.ready", map[string]any{"session": "s1"}, &res)
	if res.Type != "ci_ready" || !res.Ready || res.Number != 9 {
		t.Fatalf("ci.ready = %+v", res)
	}
	if len(mark.calls) != 1 || mark.calls[0] != 9 {
		t.Fatalf("gh pr ready calls %v, want [9]", mark.calls)
	}

	// The cache now knows the PR runs CI: a push of HEAD is judged against
	// the pass record without asking gh again.
	before := lookup.calls
	var gate ciGateResult
	rpcOK(t, srv, "", "ci.gate", map[string]any{"session": "s1", "kind": "push", "command": "git push"}, &gate)
	if !gate.Allow || gate.PR == nil || gate.PR.Draft {
		t.Fatalf("push after ready: %+v, want allowed against a non-draft PR", gate)
	}
	if lookup.calls != before {
		t.Fatalf("gh asked %d more times after ready, want 0", lookup.calls-before)
	}
}

func TestCIReadyNeedsAPassForHead(t *testing.T) {
	lookup := &fakePRLookup{}
	mark := &fakeMarkReady{}
	srv, dir := ciReadyServer(t, passingGate, lookup, mark)
	lookup.pr = &cigate.PR{Number: 9, Draft: true, State: "OPEN", HeadSHA: readyHeadSHA(t, dir)}
	wantCINotReady(t, srv, mark, "no gate run")

	passGate(t, srv)
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ciGit(t, dir, "add", "new.txt")
	ciGit(t, dir, "commit", "-q", "-m", "more")
	lookup.pr.HeadSHA = readyHeadSHA(t, dir)
	wantCINotReady(t, srv, mark, "pass is for the previous commit")
}

func TestCIReadyNeedsACleanPushedHead(t *testing.T) {
	lookup := &fakePRLookup{}
	mark := &fakeMarkReady{}
	srv, dir := ciReadyServer(t, passingGate, lookup, mark)
	passGate(t, srv)

	lookup.pr = &cigate.PR{Number: 9, Draft: true, State: "OPEN", HeadSHA: "0000000000000000000000000000000000000000"}
	wantCINotReady(t, srv, mark, "PR head is not HEAD (not pushed)")

	lookup.pr = nil
	wantCINotReady(t, srv, mark, "no PR")

	lookup.pr = &cigate.PR{Number: 9, Draft: true, State: "OPEN", HeadSHA: readyHeadSHA(t, dir)}
	if err := os.WriteFile(filepath.Join(dir, "dirty.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantCINotReady(t, srv, mark, "untracked file in the tree")
}
