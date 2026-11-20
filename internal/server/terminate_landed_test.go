package server

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// A landed child goes with its own children and its clean worktree; the
// parent stays, and clients hear session_forgotten for each row dropped.
func TestTerminateLandedEndsTheChildTree(t *testing.T) {
	r := newLifecycleRig(t)
	f := newGitFixture(t)
	r.session(domain.Session{ID: "p", WorkingDirectory: t.TempDir()})
	r.session(domain.Session{ID: "c1", ParentID: "p", WorktreePath: f.linked, WorkingDirectory: f.linked})
	r.session(domain.Session{ID: "g1", ParentID: "c1", WorkingDirectory: t.TempDir()})
	events, cancel := r.srv.hub.Subscribe()
	defer cancel()

	if err := r.srv.TerminateLanded(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	if r.rowExists("c1") || r.rowExists("g1") || !r.rowExists("p") || pathExists(f.linked) {
		t.Fatal("wrong rows or worktree left")
	}
	var forgotten []string
	for len(events) > 0 {
		if ev := <-events; ev.Type == "session_forgotten" {
			forgotten = append(forgotten, ev.ID)
		}
	}
	if !slices.Equal(forgotten, []string{"g1", "c1"}) {
		t.Fatalf("session_forgotten for %v, want [g1 c1]", forgotten)
	}
	// Already gone: nothing to do.
	if err := r.srv.TerminateLanded(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
}

// Unsaved work is never discarded: the child stays and the error says why.
func TestTerminateLandedKeepsUnsavedWork(t *testing.T) {
	r := newLifecycleRig(t)
	f := newGitFixture(t)
	r.session(domain.Session{ID: "p", WorkingDirectory: t.TempDir()})
	r.session(domain.Session{ID: "c1", ParentID: "p", WorktreePath: f.linked, WorkingDirectory: f.linked})
	f.commitUnpushed(t)

	err := r.srv.TerminateLanded(context.Background(), "c1")
	if err == nil || !strings.Contains(err.Error(), "unpushed commits") {
		t.Fatalf("err = %v", err)
	}
	if !r.rowExists("c1") || !pathExists(f.linked) || len(r.pty.Kills()) != 0 {
		t.Fatal("a refused terminate touched the child")
	}
}
