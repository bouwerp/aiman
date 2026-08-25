package pane

import (
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name       string
		obs        Observation
		want       domain.AgentState
		confidence Confidence
	}{
		{
			// Captured verbatim from a real session that was reported as stuck
			// but was twelve minutes into one turn.
			name: "elapsed timer means working",
			obs: Observation{Pane: `● Root cause confirmed. Let me map all callers.

  Map callers of the seam methods

· Pondering… (8m 30s · ↓ 24.9k tokens · still thinking)`},
			want:       domain.AgentStateWorking,
			confidence: High,
		},
		{
			name:       "interrupt hint means working",
			obs:        Observation{Pane: "✻ Reticulating splines… (12s · esc to interrupt)"},
			want:       domain.AgentStateWorking,
			confidence: High,
		},
		{
			// The case the old detector got backwards: an answered question in
			// scrollback while the agent works.
			name: "old question in scrollback does not beat a live timer",
			obs: Observation{Pane: `● Earlier I asked: are you sure you want to continue? You said yes.
  Ran 2 shell commands
✢ Pondering… (2m 4s · ↓ 8.1k tokens)`},
			want:       domain.AgentStateWorking,
			confidence: High,
		},
		{
			name: "numbered choice list needs input",
			obs: Observation{Pane: `Do you want to proceed?
❯ 1. Yes
  2. No, and tell Claude what to do differently`},
			want:       domain.AgentStateWaitingInput,
			confidence: High,
		},
		{
			name:       "yes/no confirmation needs input",
			obs:        Observation{Pane: "Allow execution of `rm -rf build/`? [y/N]"},
			want:       domain.AgentStateWaitingInput,
			confidence: High,
		},
		{
			name:       "password prompt needs input",
			obs:        Observation{Pane: "code@regent0's password:"},
			want:       domain.AgentStateWaitingInput,
			confidence: High,
		},
		{
			name:       "press enter needs input",
			obs:        Observation{Pane: "Installation complete. Press enter to continue"},
			want:       domain.AgentStateWaitingInput,
			confidence: High,
		},
		{
			name:       "shell prompt with silence means the agent exited",
			obs:        Observation{Pane: "code@regent0:~/repos/realfi$ ", SinceOutput: 10 * time.Minute},
			want:       domain.AgentStateExited,
			confidence: High,
		},
		{
			name: "grok live turn is working without a claude timer",
			obs: Observation{Pane: `Running cargo test

Enter to send now
esc cancel
❯
`, SinceOutput: 4 * time.Second},
			want:       domain.AgentStateWorking,
			confidence: High,
		},
		{
			name: "grok still-running line is waiting on background work",
			obs: Observation{Pane: `◎ 1 command still running · send a message to interrupt
❯
`, SinceOutput: 8 * time.Second},
			want:       domain.AgentStateWaitingBackground,
			confidence: High,
		},
		{
			name: "finished agent with prompt is idle",
			obs: Observation{
				Pane:        "● Done. I fixed the datum-hash handling and pushed the branch.\n\n❯ ",
				SinceOutput: 5 * time.Minute,
			},
			want:       domain.AgentStateIdle,
			confidence: High,
		},
		{
			name:       "long silence with no markers is idle",
			obs:        Observation{Pane: "some leftover output", SinceOutput: 30 * time.Minute},
			want:       domain.AgentStateIdle,
			confidence: High,
		},
		{
			name: "pane changing means working",
			obs: Observation{
				Pane:        "line one\nline two\nline three",
				Previous:    "line one\nline two",
				SinceOutput: 2 * time.Second,
			},
			want:       domain.AgentStateWorking,
			confidence: High,
		},
		{
			// Silence shorter than the threshold with nothing recognisable is
			// exactly what an LLM tier is for.
			name:       "recent output with no marker is unknown",
			obs:        Observation{Pane: "building target x86_64", SinceOutput: 3 * time.Second},
			want:       domain.AgentStateUnknown,
			confidence: Low,
		},
		{
			name:       "empty pane with no activity data is unknown",
			obs:        Observation{Pane: "", SinceOutput: -1},
			want:       domain.AgentStateUnknown,
			confidence: Low,
		},
		{
			name: "waiting for a background agent is not idle",
			obs: Observation{Pane: `● Dispatched a reviewer.

Waiting for 1 background agent to finish

❯
  -- INSERT -- ⏵⏵ bypass permissions on · ← 1 agent
`, SinceOutput: 20 * time.Second},
			want:       domain.AgentStateWaitingBackground,
			confidence: High,
		},
		{
			name: "waiting for several background agents",
			obs: Observation{
				Pane:        "Waiting for 3 background agents to finish",
				SinceOutput: 5 * time.Second,
			},
			want:       domain.AgentStateWaitingBackground,
			confidence: High,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.obs.SinceOutput == 0 {
				tt.obs.SinceOutput = -1 // unknown unless the case sets it
			}
			got := Classify(tt.obs)
			if got.State != tt.want {
				t.Errorf("State = %q, want %q (reason: %s)", got.State, tt.want, got.Reason)
			}
			if got.Confidence != tt.confidence {
				t.Errorf("Confidence = %v, want %v (reason: %s)", got.Confidence, tt.confidence, got.Reason)
			}
			if got.Reason == "" {
				t.Error("expected a reason for the classification")
			}
		})
	}
}

// The whole point of the tail window: state lives at the bottom, and a keyword
// far above it must not decide the answer.
func TestClassifyIgnoresScrollbackBeyondTheTail(t *testing.T) {
	var b string
	b = "Do you want to proceed?\n❯ 1. Yes\n  2. No\n"
	for i := 0; i < 30; i++ {
		b += "  Ran a shell command\n"
	}
	b += "✻ Pondering… (1m 2s · ↓ 3k tokens)"

	got := Classify(Observation{Pane: b, SinceOutput: 2 * time.Second})
	if got.State != domain.AgentStateWorking {
		t.Errorf("State = %q, want working — a question 30 lines back was answered long ago", got.State)
	}
}

func TestTail(t *testing.T) {
	tests := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"fewer lines than n", "a\nb", 6, "a\nb"},
		{"truncates to the last n", "a\nb\nc\nd", 2, "c\nd"},
		{"drops trailing blanks so the real last line shows", "a\nb\n\n\n", 2, "a\nb"},
		{"empty", "", 6, ""},
		{"zero n", "a\nb", 0, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Tail(tt.in, tt.n); got != tt.want {
				t.Errorf("Tail() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUIActivity(t *testing.T) {
	cases := map[domain.AgentState]string{
		domain.AgentStateWorking:           "busy",
		domain.AgentStateWaitingInput:      "input",
		domain.AgentStateWaitingBackground: "bgwait",
		domain.AgentStateIdle:              "idle",
		domain.AgentStateExited:            "exited",
		domain.AgentStateUnknown:           "",
		domain.AgentStateErrored:           "",
	}
	for state, want := range cases {
		if got := UIActivity(state); got != want {
			t.Errorf("UIActivity(%q) = %q, want %q", state, got, want)
		}
	}
}

// A prompt that appeared this instant means the agent process just exited,
// but not confidently so — the caller should look again rather than act on it.
func TestClassifyFlagsFreshShellPromptAsLowConfidence(t *testing.T) {
	got := Classify(Observation{
		Pane:        "code@regent0:~$ ",
		SinceOutput: time.Second,
	})
	if got.State != domain.AgentStateExited {
		t.Errorf("State = %q, want exited", got.State)
	}
	if got.Confidence != Low {
		t.Errorf("Confidence = %v, want Low for a prompt that just appeared", got.Confidence)
	}
}
