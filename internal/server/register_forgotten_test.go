package server

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
)

// registerResult calls session.register for ids and returns the result's ids
// and forgotten lists.
func (r *lifecycleRig) register(ids ...string) (registered, forgotten []string) {
	r.t.Helper()
	sessions := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		sessions = append(sessions, map[string]any{"id": id, "name": id, "group": "g"})
	}
	got := lifecycleResult(r.t, r.call("session.register", map[string]any{"sessions": sessions}))
	list := func(key string) []string {
		raw, ok := got[key].([]any)
		if !ok {
			r.t.Fatalf("result %q = %#v, want a list", key, got[key])
		}
		out := make([]string, 0, len(raw))
		for _, v := range raw {
			out = append(out, v.(string))
		}
		return out
	}
	return list("ids"), list("forgotten")
}

func TestRegisterSkipsTerminatedSession(t *testing.T) {
	r := newLifecycleRig(t)
	s := r.session(domain.Session{ID: "s1", WorkingDirectory: t.TempDir()})
	lifecycleResult(t, r.terminate(s.ID, false))

	ids, forgotten := r.register("s1", "s2")
	if !slices.Equal(ids, []string{"s2"}) || !slices.Equal(forgotten, []string{"s1"}) {
		t.Fatalf("ids %v forgotten %v", ids, forgotten)
	}
	if r.rowExists("s1") {
		t.Fatal("terminated session came back")
	}
	if !r.rowExists("s2") {
		t.Fatal("unrelated session not registered")
	}
}

func TestRegisterSkipsForgottenSession(t *testing.T) {
	r := newLifecycleRig(t)
	r.session(domain.Session{ID: "s1", WorkingDirectory: t.TempDir()})
	lifecycleResult(t, r.call("session.forget", map[string]any{"id": "s1"}))

	ids, forgotten := r.register("s1")
	if len(ids) != 0 || !slices.Equal(forgotten, []string{"s1"}) {
		t.Fatalf("ids %v forgotten %v", ids, forgotten)
	}
	if r.rowExists("s1") {
		t.Fatal("forgotten session came back")
	}
	if kinds := r.eventKinds(); slices.Index(kinds, domain.EventSessionCreated) > slices.Index(kinds, domain.EventSessionEnded) {
		t.Fatalf("register appended session.created after the end: %v", kinds)
	}
}

func TestRegisterRevivesForgottenSessionWithLiveHolder(t *testing.T) {
	r := newLifecycleRig(t)
	r.session(domain.Session{ID: "s1", WorkingDirectory: t.TempDir()})
	lifecycleResult(t, r.call("session.forget", map[string]any{"id": "s1"}))
	r.pty.Add(ptyruntime.SessionInfo{ID: "s1", Name: "s1", Status: ptyruntime.StatusRunning})

	ids, forgotten := r.register("s1")
	if !slices.Equal(ids, []string{"s1"}) || len(forgotten) != 0 {
		t.Fatalf("ids %v forgotten %v", ids, forgotten)
	}
	if !r.rowExists("s1") {
		t.Fatal("live session not registered")
	}
	// The live holder cleared the tombstone, so a later register without it
	// still goes through.
	_ = r.pty.Forget("s1")
	if ids, forgotten := r.register("s1"); !slices.Equal(ids, []string{"s1"}) || len(forgotten) != 0 {
		t.Fatalf("second register: ids %v forgotten %v", ids, forgotten)
	}
}

func TestRegisterKeepsForgottenSessionThatHasARowAgain(t *testing.T) {
	r := newLifecycleRig(t)
	r.session(domain.Session{ID: "s1", WorkingDirectory: t.TempDir()})
	lifecycleResult(t, r.call("session.forget", map[string]any{"id": "s1"}))
	// Something on serve stored the id again after the forget.
	if err := r.repo.Save(context.Background(), &domain.Session{ID: "s1", Name: "s1", Group: "g"}); err != nil {
		t.Fatal(err)
	}
	ids, forgotten := r.register("s1")
	if !slices.Equal(ids, []string{"s1"}) || len(forgotten) != 0 {
		t.Fatalf("ids %v forgotten %v", ids, forgotten)
	}
}

// failingTombstones is a repository whose tombstone lookup fails.
type failingTombstones struct{ domain.SessionRepository }

func (failingTombstones) IsForgotten(context.Context, string) (bool, error) {
	return false, errors.New("disk on fire")
}

func (failingTombstones) MarkForgotten(context.Context, string, string) error { return nil }

func (failingTombstones) ClearForgotten(context.Context, string) error { return nil }

func TestRegisterTreatsFailedTombstoneLookupAsNotForgotten(t *testing.T) {
	r := newLifecycleRig(t)
	r.srv.repo = failingTombstones{r.repo}
	ids, forgotten := r.register("s1")
	if !slices.Equal(ids, []string{"s1"}) || len(forgotten) != 0 {
		t.Fatalf("ids %v forgotten %v", ids, forgotten)
	}
}

func TestRegisterWithoutTombstoneSupportRegisters(t *testing.T) {
	r := newLifecycleRig(t)
	r.srv.repo = struct{ domain.SessionRepository }{r.repo}
	r.session(domain.Session{ID: "s1", WorkingDirectory: t.TempDir()})
	lifecycleResult(t, r.call("session.forget", map[string]any{"id": "s1"}))
	ids, forgotten := r.register("s1")
	if !slices.Equal(ids, []string{"s1"}) || len(forgotten) != 0 {
		t.Fatalf("ids %v forgotten %v", ids, forgotten)
	}
}
