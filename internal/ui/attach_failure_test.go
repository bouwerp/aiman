package ui

import (
	"errors"
	"strings"
	"testing"
)

// TestAttachFailureNamesTheRealBackend guards against telling the user a tmux
// attach failed for a session that never involved tmux. A PTY session is hosted
// by a holder process, so "tmux" in the message sends them looking in the wrong
// place entirely.
func TestAttachFailureNamesTheRealBackend(t *testing.T) {
	err := errors.New("exit status 1")

	pty := attachFailure(attachDoneMsg{err: err, pty: true})
	if strings.Contains(strings.ToLower(pty), "tmux") {
		t.Fatalf("PTY attach failure must not mention tmux, got %q", pty)
	}
	if !strings.Contains(pty, "PTY") || !strings.Contains(pty, "exit status 1") {
		t.Fatalf("PTY message must name the backend and the cause, got %q", pty)
	}

	tmux := attachFailure(attachDoneMsg{err: err})
	if !strings.Contains(tmux, "tmux") || !strings.Contains(tmux, "exit status 1") {
		t.Fatalf("tmux message must name the backend and the cause, got %q", tmux)
	}
}
