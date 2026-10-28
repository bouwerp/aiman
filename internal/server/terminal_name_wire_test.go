package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// session.register sends a marshalled domain.Session, so the field keeps the
// key older serves read.
func TestSessionRegisterWireKeyStable(t *testing.T) {
	b, err := json.Marshal(domain.Session{ID: "s1", TerminalName: "fix-yield"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"TmuxSession":"fix-yield"`) {
		t.Fatalf("marshalled session lost the TmuxSession key: %s", b)
	}
	var back domain.Session
	if err := json.Unmarshal([]byte(`{"ID":"s1","TmuxSession":"fix-yield"}`), &back); err != nil {
		t.Fatal(err)
	}
	if back.TerminalName != "fix-yield" {
		t.Fatalf("TerminalName = %q, want fix-yield", back.TerminalName)
	}
}

// session.list and remote clients read the terminal name as tmux_session.
func TestSessionInfoWireKeyStable(t *testing.T) {
	b, err := json.Marshal(SessionInfo{ID: "s1", TerminalName: "fix-yield"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"tmux_session":"fix-yield"`) {
		t.Fatalf("SessionInfo lost the tmux_session key: %s", b)
	}
}
