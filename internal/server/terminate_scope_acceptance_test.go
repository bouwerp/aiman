package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

const f13Peer = 4242

// f13Rig holds a (parent), c (child of a, on a linked worktree), d (child of
// c) and b (unrelated), with procInSession admitting pid 4242.
func f13Rig(t *testing.T) (*lifecycleRig, gitFixture) {
	t.Helper()
	r := newLifecycleRig(t)
	f := newGitFixture(t)
	r.session(domain.Session{ID: "a", WorkingDirectory: t.TempDir()})
	r.session(domain.Session{ID: "b", WorkingDirectory: t.TempDir()})
	r.session(domain.Session{ID: "c", ParentID: "a", WorktreePath: f.linked, WorkingDirectory: f.linked, Branch: "feature"})
	r.session(domain.Session{ID: "d", ParentID: "c", WorkingDirectory: t.TempDir()})
	r.srv.procInSession = func(pid int) bool { return pid == f13Peer }
	return r, f
}

// f13Terminate calls session.terminate as a session: with its id as caller,
// from a process inside a holder.
func f13Terminate(r *lifecycleRig, caller, target string, discard bool) Response {
	r.t.Helper()
	return r.callCtx(withPeerPID(context.Background(), f13Peer, true), caller, "session.terminate",
		map[string]any{"session": target, "discard": discard})
}

func TestF13ParentTerminatesChild(t *testing.T) {
	r, f := f13Rig(t)
	got := lifecycleResult(t, f13Terminate(r, "a", "c", false))
	if got["type"] != "session_terminated" || got["worktree_removed"] != true {
		t.Fatalf("result = %v", got)
	}
	if pathExists(f.linked) {
		t.Fatal("c's worktree still there")
	}
	if r.rowExists("c") {
		t.Fatal("c's row survived")
	}
	left, _ := got["children_left"].([]any)
	if len(left) != 1 || left[0] != "d" {
		t.Fatalf("children_left = %v, want [d]", got["children_left"])
	}
	if !r.rowExists("d") {
		t.Fatal("c's own child was dropped")
	}
}

func TestF13ParentTerminatesGrandchild(t *testing.T) {
	r, _ := f13Rig(t)
	got := lifecycleResult(t, f13Terminate(r, "a", "d", false))
	if got["type"] != "session_terminated" {
		t.Fatalf("result = %v", got)
	}
	if r.rowExists("d") {
		t.Fatal("d's row survived")
	}
}

func TestF13UnrelatedCallerRefused(t *testing.T) {
	r, f := f13Rig(t)
	wantCode(t, f13Terminate(r, "b", "c", true), CodeSessionScope)
	if len(r.pty.Kills()) != 0 || !pathExists(f.linked) || !r.rowExists("c") {
		t.Fatal("a refused call touched the session")
	}
}

func TestF13SessionCannotTerminateItself(t *testing.T) {
	r, _ := f13Rig(t)
	wantCode(t, f13Terminate(r, "a", "a", true), CodeNeedsOperator)
	if len(r.pty.Kills()) != 0 || !r.rowExists("a") {
		t.Fatal("a refused call touched the session")
	}
}

func TestF13DirtyChildNeedsDiscard(t *testing.T) {
	r, f := f13Rig(t)
	putFile(t, filepath.Join(f.linked, "a.txt"), "changed\n")
	wantCode(t, f13Terminate(r, "a", "c", false), CodeWorktreeDirty)
	if len(r.pty.Kills()) != 0 || !pathExists(f.linked) || !r.rowExists("c") {
		t.Fatal("a refused dirty terminate touched the session")
	}
	got := lifecycleResult(t, f13Terminate(r, "a", "c", true))
	if got["worktree_removed"] != true || pathExists(f.linked) {
		t.Fatalf("discard did not remove the worktree: %v", got)
	}
	if r.rowExists("c") {
		t.Fatal("c's row survived")
	}
}
