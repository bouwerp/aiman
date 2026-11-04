package usecase

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestRestartHandoverReason(t *testing.T) {
	if got := restartHandoverReason(domain.SessionConfig{FreshAgent: true}); got != "agent_switch" {
		t.Fatalf("FreshAgent: got %q, want agent_switch", got)
	}
	if got := restartHandoverReason(domain.SessionConfig{}); got != "restart" {
		t.Fatalf("same agent: got %q, want restart", got)
	}
}

func TestRestartHandoverAgent(t *testing.T) {
	target := &domain.Agent{Name: "Grok Build CLI", Command: "grok"}
	if got := restartHandoverAgent(domain.SessionConfig{FreshAgent: true, Agent: target}); got != "grok" {
		t.Fatalf("switch: got %q, want grok", got)
	}
	if got := restartHandoverAgent(domain.SessionConfig{Agent: target}); got != "" {
		t.Fatalf("same-agent restart: got %q, want empty", got)
	}
	if got := restartHandoverAgent(domain.SessionConfig{FreshAgent: true}); got != "" {
		t.Fatalf("switch with no agent: got %q, want empty", got)
	}
}

func TestRestartPromptAddsHandover(t *testing.T) {
	prepared := "Read .aiman_task.md and continue."
	cases := []struct {
		name     string
		prepared string
		mode     HandoffMode
		want     string
	}{
		{
			name: "empty handover",
			mode: HandoffHandover,
			want: HandoverPrompt,
		},
		{
			name:     "prepared handover",
			prepared: prepared,
			mode:     HandoffHandover,
			want:     prepared + " " + HandoverPrompt,
		},
		{
			name:     "summary unchanged",
			prepared: "x",
			mode:     HandoffSummary,
			want:     "x",
		},
		{
			name:     "none unchanged",
			prepared: "x",
			mode:     HandoffNone,
			want:     "x",
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := restartPrompt(tt.prepared, tt.mode)
			if got != tt.want {
				t.Fatalf("restartPrompt(%q, %d) = %q, want %q", tt.prepared, tt.mode, got, tt.want)
			}
		})
	}
}
