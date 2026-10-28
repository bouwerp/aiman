package ui

import (
	"errors"
	"strings"
	"testing"
)

// TestAttachFailureNamesTheRealBackend: every attach goes to the PTY runtime,
// so a failure must name it and never send the user looking for tmux.
func TestAttachFailureNamesTheRealBackend(t *testing.T) {
	msg := attachFailure(attachDoneMsg{err: errors.New("exit status 1")})
	if strings.Contains(strings.ToLower(msg), "tmux") {
		t.Fatalf("attach failure must not mention tmux, got %q", msg)
	}
	if !strings.Contains(msg, "PTY") || !strings.Contains(msg, "exit status 1") {
		t.Fatalf("message must name the backend and the cause, got %q", msg)
	}
}
