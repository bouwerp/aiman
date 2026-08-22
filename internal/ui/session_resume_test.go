package ui

import (
	"context"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

type stubRemote struct {
	out string
	err error
}

func (s stubRemote) Execute(context.Context, string) (string, error) {
	return s.out, s.err
}

func TestWithRemoteNativeResumeFromSidecar(t *testing.T) {
	s := &domain.Session{ID: "sess-1", AgentSessionID: "stale"}
	got := withRemoteNativeResume(context.Background(), stubRemote{out: `{"id":"native-9"}`}, s, "claude --dangerously-skip-permissions")
	want := "claude --resume native-9 --dangerously-skip-permissions"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if s.AgentSessionID != "native-9" {
		t.Fatalf("session not updated: %q", s.AgentSessionID)
	}
}

func TestWithRemoteNativeResumeUsesStoredWhenSidecarEmpty(t *testing.T) {
	s := &domain.Session{ID: "sess-1", AgentSessionID: "kept"}
	got := withRemoteNativeResume(context.Background(), stubRemote{out: ""}, s, "grok")
	if got != "grok --resume kept" {
		t.Fatalf("%q", got)
	}
}

func TestWithRemoteNativeResumeNoID(t *testing.T) {
	s := &domain.Session{ID: "sess-1"}
	got := withRemoteNativeResume(context.Background(), stubRemote{}, s, "claude")
	if got != "claude" {
		t.Fatalf("%q", got)
	}
}
