package server

import (
	"errors"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bouwerp/aiman/internal/cigate"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// Acceptance tests for the git -C push gate: a session may push a branch in a
// directory outside its worktree. Do not edit.

var errLookup = errors.New("gh: timeout")

const okGate = "commands:\n  - name: ok\n    run: \"true\"\n"

// plainRepo is a committed checkout on branch feat-y that no session owns.
func plainRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "other")
	if out, err := exec.Command("git", "init", "-q", "-b", "feat-y", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	ciGit(t, dir, "-c", "user.name=T", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init")
	return dir
}

func TestCIGateDirInsideTheWorktreeIsAPlainPush(t *testing.T) {
	lookup := &fakePRLookup{pr: &cigate.PR{Number: 3, State: "OPEN"}}
	srv, dir := ciLocalServer(t, okGate, todoSession("s1"), lookup)
	var gate ciGateResult
	rpcOK(t, srv, "", "ci.gate", map[string]any{"session": "s1", "kind": "push", "command": "git -C " + dir + " push origin feat-x"}, &gate)
	if gate.Allow || gate.PR == nil || gate.PR.Number != 3 || gate.Reason == cigate.PushRefusal {
		t.Fatalf("push before a run: %+v, want refused for the missing run, with the PR", gate)
	}
	rpcOK(t, srv, "", "ci.gate", map[string]any{"session": "s1", "kind": "push", "command": "git -C " + dir + " push --force"}, &gate)
	if gate.Allow || gate.Reason != cigate.PushRefusal {
		t.Fatalf("forced push: %+v", gate)
	}
}

func TestCIGateDirOutsideAnySession(t *testing.T) {
	lookup := &fakePRLookup{}
	srv, _ := ciLocalServer(t, okGate, todoSession("s1"), lookup)
	other := plainRepo(t)
	cmd := "git -C " + other + " push -u origin feat-y"

	var gate ciGateResult
	rpcOK(t, srv, "", "ci.gate", map[string]any{"session": "s1", "kind": "push", "command": cmd}, &gate)
	if !gate.Allow {
		t.Fatalf("new branch with no PR: %+v, want allowed", gate)
	}
	if lookup.calls == 0 {
		t.Fatal("the PR lookup was not asked for the other directory")
	}

	srv2, _ := ciLocalServer(t, okGate, todoSession("s1"), &fakePRLookup{pr: &cigate.PR{Number: 9, State: "OPEN"}})
	rpcOK(t, srv2, "", "ci.gate", map[string]any{"session": "s1", "kind": "push", "command": cmd}, &gate)
	if gate.Allow {
		t.Fatalf("branch with an open PR and no session owning it: %+v, want refused", gate)
	}

	srv3, _ := ciLocalServer(t, okGate, todoSession("s1"), &fakePRLookup{err: errLookup})
	rpcOK(t, srv3, "", "ci.gate", map[string]any{"session": "s1", "kind": "push", "command": cmd}, &gate)
	if gate.Allow {
		t.Fatalf("lookup failed: %+v, want refused", gate)
	}

	rpcOK(t, srv, "", "ci.gate", map[string]any{"session": "s1", "kind": "push", "command": "git -C other push"}, &gate)
	if gate.Allow || gate.Reason != cigate.PushRefusal {
		t.Fatalf("relative -C: %+v, want the push refusal", gate)
	}
}

func TestCIGateDirInAnotherSessionsWorktree(t *testing.T) {
	lookup := &fakePRLookup{pr: &cigate.PR{Number: 4, State: "OPEN"}}
	s1, s2 := todoSession("s1"), todoSession("s2")
	s1.WorktreePath, s1.RepoName = ciRepo(t, okGate), "o/r"
	s2.WorktreePath, s2.RepoName = ciRepo(t, okGate), "o/r2"
	srv, _, _ := todoServer(t, s1, s2)
	srv.SetCILocal(CILocalDeps{
		Runner:   &cigate.Runner{Dir: filepath.Join(t.TempDir(), "ci-local"), MaxParallel: 2},
		Repos:    map[string]config.RepoBootstrap{},
		LookupPR: lookup.lookup,
	})
	var gate ciGateResult
	rpcOK(t, srv, "", "ci.local", map[string]any{"session": "s1", "wait": true}, nil)
	rpcOK(t, srv, "", "ci.gate", map[string]any{"session": "s1", "kind": "push", "command": "git -C " + s2.WorktreePath + " push origin feat-x"}, &gate)
	if gate.Allow {
		t.Fatalf("s1's pass record let it push s2's branch, which has none: %+v", gate)
	}
	rpcOK(t, srv, "", "ci.local", map[string]any{"session": "s2", "wait": true}, nil)
	rpcOK(t, srv, "", "ci.gate", map[string]any{"session": "s1", "kind": "push", "command": "git -C " + s2.WorktreePath + " push origin feat-x"}, &gate)
	if !gate.Allow {
		t.Fatalf("s2 passed: %+v, want allowed", gate)
	}
}
