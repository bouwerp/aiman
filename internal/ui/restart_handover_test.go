package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/usecase"
)

func TestRestartHandoverReason(t *testing.T) {
	if got := restartHandoverReason(domain.SessionConfig{FreshAgent: true}); got != "agent_switch" {
		t.Fatalf("FreshAgent: got %q, want agent_switch", got)
	}
	if got := restartHandoverReason(domain.SessionConfig{}); got != "restart" {
		t.Fatalf("same agent: got %q, want restart", got)
	}
}

func TestRestartPromptAddsHandover(t *testing.T) {
	prepared := "Read .aiman_task.md and continue."
	cases := []struct {
		name     string
		prepared string
		mode     usecase.HandoffMode
		want     string
	}{
		{
			name: "empty handover",
			mode: usecase.HandoffHandover,
			want: usecase.HandoverPrompt,
		},
		{
			name:     "prepared handover",
			prepared: prepared,
			mode:     usecase.HandoffHandover,
			want:     prepared + " " + usecase.HandoverPrompt,
		},
		{
			name:     "summary unchanged",
			prepared: "x",
			mode:     usecase.HandoffSummary,
			want:     "x",
		},
		{
			name:     "none unchanged",
			prepared: "x",
			mode:     usecase.HandoffNone,
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
