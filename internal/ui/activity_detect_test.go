package ui

import (
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/pane"
)

// The dashboard's activity strings must follow the classifier, including the
// case the previous substring matcher got backwards.
func TestDetectSessionActivity(t *testing.T) {
	tests := []struct {
		name         string
		pane         string
		since        time.Duration
		wantActivity string
		wantNeeds    bool
	}{
		{
			name:         "running agent is busy",
			pane:         "✻ Pondering… (8m 30s · ↓ 24.9k tokens · still thinking)",
			since:        2 * time.Second,
			wantActivity: "busy",
		},
		{
			// Previously: "are you sure" anywhere in the pane forced "input",
			// so a working agent was reported as blocked.
			name: "answered question in scrollback does not mask a running agent",
			pane: "● Earlier I asked: are you sure you want to continue? You said yes.\n" +
				"  Ran 2 shell commands\n✢ Pondering… (2m 4s · ↓ 8.1k tokens)",
			since:        2 * time.Second,
			wantActivity: "busy",
		},
		{
			name:         "choice list needs input",
			pane:         "Do you want to proceed?\n❯ 1. Yes\n  2. No, and tell Claude what to do differently",
			since:        30 * time.Second,
			wantActivity: "input",
			wantNeeds:    true,
		},
		{
			name:         "silent shell prompt means the agent exited",
			pane:         "code@regent0:~/repos/realfi$ ",
			since:        10 * time.Minute,
			wantActivity: "exited",
		},
		{
			// Captured from a real session: turn finished, agent at its prompt.
			name:         "agent input box with a finished turn is idle",
			pane:         "✻ Brewed for 42s\n\n❯ \n  -- INSERT -- ⏵⏵ bypass permissions on (shift+tab to cycle)",
			since:        20 * time.Second,
			wantActivity: "idle",
		},
		{
			name: "waiting on a background agent is not idle",
			pane: "Waiting for 1 background agent to finish\n\n❯\n" +
				"  -- INSERT -- ⏵⏵ bypass permissions on · ← 1 agent",
			since:        15 * time.Second,
			wantActivity: "bgwait",
		},
		{
			name:         "grok send-now footer is busy",
			pane:         "Editing launch_flags.go\n\nEnter to send now\n❯ ",
			since:        3 * time.Second,
			wantActivity: "busy",
		},
		{
			name:         "grok still-running footer is waiting",
			pane:         "◎ 2 monitors still running · send a message to interrupt\n❯ ",
			since:        10 * time.Second,
			wantActivity: "bgwait",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			activity, needs := detectSessionActivityFrom(pane.Observation{Pane: tt.pane, SinceOutput: tt.since, SinceTitleChange: -1})
			if activity != tt.wantActivity {
				t.Errorf("activity = %q, want %q", activity, tt.wantActivity)
			}
			if needs != tt.wantNeeds {
				t.Errorf("needsInput = %v, want %v", needs, tt.wantNeeds)
			}
		})
	}
}

// The no-age entry point must keep working for callers that have not been
// given the tmux activity timestamp yet.
func TestDetectSessionActivityWithoutAge(t *testing.T) {
	if activity, _ := detectSessionActivityFrom(pane.Observation{Pane: "✻ Pondering… (1m 2s · ↓ 3k tokens)", SinceOutput: -1, SinceTitleChange: -1}); activity != "busy" {
		t.Errorf("activity = %q, want busy", activity)
	}
	if _, needs := detectSessionActivityFrom(pane.Observation{Pane: "Allow execution of `rm -rf build/`? [y/N]", SinceOutput: -1, SinceTitleChange: -1}); !needs {
		t.Error("expected a yes/no confirmation to need input")
	}
}
