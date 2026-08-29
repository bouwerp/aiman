package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func dirtySession(agent string) *domain.Session {
	return &domain.Session{
		ID: "s1", AgentName: agent,
		AgentSessionID: "old-id", AgentSessionPath: "/old/path.jsonl",
		AgentTitle: "old title", AgentEnded: true,
		HookState: domain.AgentStateIdle, HookStateMessage: "old message",
		HookStateSource: "session_end", HookStateSeq: 7,
	}
}

// Switching agent leaves the previous one's identity on the record. None of it
// describes the agent about to start, and a latched AgentEnded would render the
// fresh session as already exited.
func TestRestartWithADifferentAgentClearsTheOldIdentity(t *testing.T) {
	s := dirtySession("codex")
	adoptRestartAgent(s, "claude")

	if s.AgentName != "claude" {
		t.Errorf("agent not switched: %q", s.AgentName)
	}
	if s.AgentSessionID != "" || s.AgentSessionPath != "" || s.AgentTitle != "" {
		t.Errorf("previous agent's identity survived: %+v", s)
	}
	if s.AgentEnded {
		t.Error("a fresh agent must not start out marked as ended")
	}
	if s.HookState != "" || s.HookStateSource != "" || s.HookStateSeq != 0 {
		t.Errorf("previous agent's hook state survived: %+v", s)
	}
}

// Restarting with the same agent is a resume: its transcript and state are still
// its own and must be kept.
func TestRestartWithTheSameAgentKeepsItsIdentity(t *testing.T) {
	s := dirtySession("codex")
	adoptRestartAgent(s, "Codex")

	if s.AgentSessionID != "old-id" || s.AgentSessionPath != "/old/path.jsonl" {
		t.Errorf("a same-agent restart must keep the transcript: %+v", s)
	}
	if s.HookStateSeq != 7 {
		t.Errorf("hook state should survive a resume: %+v", s)
	}
}
