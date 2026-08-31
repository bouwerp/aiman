package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

type stubRemote struct {
	out     string
	err     error
	lastCmd string
}

func (s *stubRemote) Execute(_ context.Context, cmd string) (string, error) {
	s.lastCmd = cmd
	return s.out, s.err
}

func TestWithRemoteNativeResumeFromSidecar(t *testing.T) {
	s := &domain.Session{ID: "sess-1", AgentSessionID: "stale"}
	remote := &stubRemote{out: `{"id":"native-9"}`}
	got := withRemoteNativeResume(context.Background(), remote, s, "claude --dangerously-skip-permissions", false)
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
	got := withRemoteNativeResume(context.Background(), &stubRemote{out: ""}, s, "grok", false)
	if got != "grok --resume kept" {
		t.Fatalf("%q", got)
	}
}

func TestWithRemoteNativeResumeNoID(t *testing.T) {
	s := &domain.Session{ID: "sess-1"}
	got := withRemoteNativeResume(context.Background(), &stubRemote{}, s, "claude", false)
	if got != "claude" {
		t.Fatalf("%q", got)
	}
}

// A deliberate agent switch must start a fresh conversation: the previous
// agent's sidecar id does not belong to the new binary.
func TestWithRemoteNativeResumeFreshStartSkipsSidecarAndStored(t *testing.T) {
	s := &domain.Session{ID: "sess-1", AgentSessionID: "old-claude"}
	remote := &stubRemote{out: `{"id":"00ca0f57-1d8f-42f2-ab4e-d460fa8b03f2","path":"/home/code/.claude/projects/x/00ca0f57.jsonl"}`}
	got := withRemoteNativeResume(context.Background(), remote, s, "grok --always-approve", true)
	if got != "grok --always-approve" {
		t.Fatalf("fresh start must not resume, got %q", got)
	}
	if s.AgentSessionID != "" {
		t.Fatalf("fresh start must leave AgentSessionID empty, got %q", s.AgentSessionID)
	}
}

// Even without freshStart, a Claude transcript path must not be resumed into grok.
func TestWithRemoteNativeResumeRejectsCrossAgentSidecar(t *testing.T) {
	s := &domain.Session{ID: "sess-1"}
	remote := &stubRemote{out: `{"id":"00ca0f57-1d8f-42f2-ab4e-d460fa8b03f2","path":"/home/code/.claude/projects/x/00ca0f57.jsonl"}`}
	got := withRemoteNativeResume(context.Background(), remote, s, "grok --always-approve", false)
	if got != "grok --always-approve" {
		t.Fatalf("cross-agent sidecar must not resume, got %q", got)
	}
}

func TestClearRemoteNativeSidecar(t *testing.T) {
	remote := &stubRemote{}
	clearRemoteNativeSidecar(context.Background(), remote, "ed0fc625-b396-4b0a-8811-8d38e2449bef")
	if !strings.Contains(remote.lastCmd, "native-sessions") ||
		!strings.Contains(remote.lastCmd, "ed0fc625-b396-4b0a-8811-8d38e2449bef") ||
		!strings.Contains(remote.lastCmd, "rm") {
		t.Fatalf("expected rm of sidecar, got %q", remote.lastCmd)
	}
}
