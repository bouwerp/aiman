package pane

import (
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// workingPaneWithChrome is a real Claude Code pane, captured while the agent was
// mid-turn. The spinner sits eight lines from the bottom because the agent draws
// its input box and status bars underneath it.
//
// This shape was classified as idle: a six-line tail cut the spinner off and
// left only the furniture, which looks exactly like a finished turn. Both the
// rules and the model agreed on the wrong answer, because the rules handed the
// model an already-truncated tail.
const workingPaneWithChrome = `● Running 4 shell commands…
  ⎿  $ SP=/tmp/claude-XXXX/scratchpad
     cd $SP/pa-97
     sed -n '220,300p' api/lib/governance/nativeScript.ts

· Sautéing… (10m 42s · ↓ 27.5k tokens · thought for 17s)

────────────────────────────────────────
❯
────────────────────────────────────────
  code@regent0:/home/code/repos/realfi@agent-preprod-v1-migration-safety  $324.55 · 20h42m · +1907/-202 · ↑456290.1k ↓673.0k tok · [███░░░░░░░] 28% ctx · 5h [████░░░░░░] 42% (1h41m) · 7d [███░░░░░░░] 27% (5d10h)                              /rc
  -- INSERT -- ⏵⏵ bypass permissions on · 1 shell · ← 2 agents
  ⧉  cutover-status`

func TestClassifyWorkingBeneathAgentChrome(t *testing.T) {
	got := Classify(Observation{Pane: workingPaneWithChrome, SinceOutput: 3 * time.Second})
	if got.State != domain.AgentStateWorking {
		t.Fatalf("State = %q (%s), want working — the spinner is 8 lines up, under the agent's own chrome",
			got.State, got.Reason)
	}
	if got.Confidence != High {
		t.Errorf("Confidence = %v, want High", got.Confidence)
	}
}

// The same pane with the spinner replaced by a finished-turn marker is idle:
// the input box alone must not decide either way.
func TestClassifyIdleBeneathAgentChrome(t *testing.T) {
	idlePane := strings.Replace(workingPaneWithChrome,
		"· Sautéing… (10m 42s · ↓ 27.5k tokens · thought for 17s)",
		"✻ Brewed for 42s", 1)

	got := Classify(Observation{Pane: idlePane, SinceOutput: 30 * time.Second})
	if got.State != domain.AgentStateIdle {
		t.Fatalf("State = %q (%s), want idle", got.State, got.Reason)
	}
}

// The context bar carries durations like "20h42m" and "(1h41m)". Those are
// budget readouts, not a running turn, and must not be read as one.
func TestClassifyIgnoresDurationsInTheContextBar(t *testing.T) {
	bar := "  code@regent0:/repos/app  $324.55 · 20h42m · 5h [████░░] 42% (1h41m) · 7d [███░░] 27% (5d10h)\n" +
		"  -- INSERT -- ⏵⏵ bypass permissions on\n"
	got := Classify(Observation{Pane: bar, SinceOutput: 10 * time.Minute})
	if got.State == domain.AgentStateWorking {
		t.Errorf("State = working (%s) — the context bar's durations are not a running turn", got.Reason)
	}
}

// A question must still win over a spinner further up: the agent has stopped to
// ask, even though the turn's timer is still on screen.
func TestClassifyPrefersAQuestionOverAnOlderSpinner(t *testing.T) {
	pane := `· Sautéing… (2m 4s · ↓ 8k tokens)

Do you want to proceed?
❯ 1. Yes
  2. No, and tell Claude what to do differently`

	got := Classify(Observation{Pane: pane, SinceOutput: 5 * time.Second})
	if got.State != domain.AgentStateWaitingInput {
		t.Errorf("State = %q (%s), want waiting_input", got.State, got.Reason)
	}
}
