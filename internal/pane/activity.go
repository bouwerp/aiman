package pane

import (
	"regexp"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// TailLines is the window used to decide whether the user is being asked
// something. It is deliberately tight: a question further up has been answered.
const TailLines = 6

// StatusLines is the window used to find evidence that work is in progress.
//
// It is wider than TailLines because agents render their input box below the
// spinner, not above it. Claude Code puts seven lines of chrome underneath —
// blank, box rule, prompt, box rule, context bar, mode line, background-task
// line — so a six-line tail cuts the spinner off entirely and leaves only the
// idle-looking furniture. That is not scrollback: it is the live bottom of the
// screen, so widening this window does not reintroduce the keyword-matching
// problem that TailLines exists to avoid.
const StatusLines = 20

// Confidence reports whether a caller should trust a classification or seek a
// second opinion (an LLM, or simply waiting for another sample).
type Confidence int

const (
	// Low means the signals were inconclusive; escalate.
	Low Confidence = iota
	// High means a definite signal was present.
	High
)

// Observation is everything the classifier needs. Every field is optional:
// more signal narrows the answer, none of it is required.
type Observation struct {
	// Pane is the captured pane content. Only its tail is examined.
	Pane string
	// Previous is the pane from the last sample, for change detection. Empty
	// when there is no prior sample.
	Previous string
	// SinceOutput is how long ago the session last produced output, from tmux's
	// own bookkeeping. Negative means unknown.
	SinceOutput time.Duration
	// IdleAfter is how much silence means idle. Zero uses DefaultIdleAfter.
	IdleAfter time.Duration
}

// DefaultIdleAfter is the silence that counts as idle. Agents routinely think
// for minutes without emitting anything — WTB-1925 sat on one turn for over
// twelve — so this is deliberately generous.
const DefaultIdleAfter = 90 * time.Second

// Result is a classification and why it was reached.
type Result struct {
	State      domain.AgentState
	Confidence Confidence
	// Reason names the signal that decided it, for logs and for explaining a
	// wrong answer later.
	Reason string
}

var (
	// A running agent renders an elapsed timer that advances: "(8m 30s · ↓ 24.9k
	// tokens · still thinking)". The timer is the signal, not the wording, so
	// this keeps working when an agent renames its spinner verbs.
	elapsedTimerRe = regexp.MustCompile(`\(\s*(?:\d+h\s*)?(?:\d+m\s*)?\d+s\s*[·)]`)

	// Explicit interrupt hints accompany every agent's working state.
	interruptHintRe = regexp.MustCompile(`(?i)\b(esc to interrupt|ctrl\+c to (?:stop|cancel)|press esc|enter to send now)\b`)

	// A numbered or arrow-marked choice list is a question, not output.
	menuChoiceRe = regexp.MustCompile(`(?m)^\s*[❯>]\s*\d+[.)]\s+\S`)

	// Yes/no confirmations, in the shapes agents and shells actually emit.
	yesNoRe = regexp.MustCompile(`(?i)[\[(](?:y/n|y/N|yes/no)[\])]\s*[:?]?\s*$`)

	// A trailing question addressed to the user.
	questionRe = regexp.MustCompile(`(?i)^(?:do you |would you |should i |shall i |are you sure|continue\?|proceed\?)`)

	// Credential and continue prompts, which block just as hard.
	blockingPromptRe = regexp.MustCompile(`(?i)(password|passphrase)\s*[:?]\s*$|press (?:any key|enter)\b|allow (?:once|always|execution|for this session)\b`)

	// A bare shell prompt at the end means nothing is running. Do not treat
	// agent composers (Claude/Grok ❯) as a shell: Grok keeps ❯ on screen
	// while a turn runs, and that used to classify busy sessions as idle.
	shellPromptRe = regexp.MustCompile(`(?:^|\n)\s*(?:\S+@\S+[:\s].*[$#%]|[$#%])\s*$`)

	// Agents render their own input box when they are ready for the next
	// instruction: Claude Code shows a "-- INSERT --" affordance line, a
	// permissions mode hint, or a "new task?" nudge. Seen without a running
	// timer, the turn is over and the agent is waiting on the human — idle, not
	// blocked, because nothing is being asked.
	agentReadyRe = regexp.MustCompile(`(?i)(-- INSERT --|bypass permissions on|new task\? /clear|\bshift\+tab to cycle\b)`)

	// Past-tense completion markers ("Brewed for 42s", Grok "Worked for 12s")
	// report a finished turn, unlike the present-tense timer that marks a running one.
	turnFinishedRe = regexp.MustCompile(`(?i)\b(?:brewed|thought|pondered|worked|ruminated) for\s+(?:\d+m\s*)?\d+s\b`)

	// The foreground agent is parked on its own sub-agents. Claude Code prints
	// "Waiting for 1 background agent to finish" under the input box; Grok
	// prints "◎ 1 command still running · send a message to interrupt".
	waitingBackgroundRe = regexp.MustCompile(`(?i)(?:waiting for \d+ (?:background )?(?:agents?|subagents?|tasks?)(?: to finish)?\b|send a message to interrupt|(?:\d+\s+)?(?:commands?|subagents?|monitors?|loops?|tasks?) still running)`)
)

// Classify decides what a session is doing from cheap, local signals.
//
// Order matters. A question appearing is itself a change, so input detection has
// to precede change detection or every prompt would read as work in progress.
func Classify(obs Observation) Result {
	tail := Tail(obs.Pane, TailLines)
	status := Tail(obs.Pane, StatusLines)
	if strings.TrimSpace(status) == "" && obs.SinceOutput < 0 {
		return Result{State: domain.AgentStateUnknown, Confidence: Low, Reason: "empty pane"}
	}

	if reason, ok := needsInput(tail); ok {
		return Result{State: domain.AgentStateWaitingInput, Confidence: High, Reason: reason}
	}

	if waitingBackgroundRe.MatchString(status) {
		return Result{State: domain.AgentStateWaitingBackground, Confidence: High, Reason: "waiting on background agent"}
	}

	// A running spinner sits above the agent's own chrome, so this looks further
	// up than the prompt checks do.
	if elapsedTimerRe.MatchString(status) {
		return Result{State: domain.AgentStateWorking, Confidence: High, Reason: "elapsed timer in status line"}
	}
	if interruptHintRe.MatchString(status) {
		return Result{State: domain.AgentStateWorking, Confidence: High, Reason: "interrupt hint"}
	}

	// The pane moved between samples, so something is producing output.
	if obs.Previous != "" && Tail(obs.Previous, TailLines) != tail {
		return Result{State: domain.AgentStateWorking, Confidence: High, Reason: "pane changed since last sample"}
	}

	idleAfter := obs.IdleAfter
	if idleAfter <= 0 {
		idleAfter = DefaultIdleAfter
	}
	silent := obs.SinceOutput >= 0 && obs.SinceOutput >= idleAfter

	if shellPromptRe.MatchString(strings.TrimRight(tail, " \t")) {
		if silent || obs.SinceOutput < 0 {
			return Result{State: domain.AgentStateIdle, Confidence: High, Reason: "shell prompt with no recent output"}
		}
		// A prompt that just appeared means a command finished this instant;
		// treat it as idle but invite a second look.
		return Result{State: domain.AgentStateIdle, Confidence: Low, Reason: "shell prompt but output is recent"}
	}

	if silent {
		return Result{State: domain.AgentStateIdle, Confidence: High, Reason: "no output for " + obs.SinceOutput.Round(time.Second).String()}
	}

	// The agent is sitting at its own input box and no running timer was found in
	// the wider window above, so the turn really is over. The box itself proves
	// nothing: agents render it while working too.
	if agentReadyRe.MatchString(status) {
		reason := "agent input box, no turn running"
		if turnFinishedRe.MatchString(status) {
			reason = "agent reported a finished turn"
		}
		return Result{State: domain.AgentStateIdle, Confidence: High, Reason: reason}
	}

	// Recent output with no recognisable marker: something is happening, but not
	// provably work. This is the case worth escalating to a model.
	return Result{State: domain.AgentStateUnknown, Confidence: Low, Reason: "no decisive signal in tail"}
}

// needsInput reports a blocking prompt in the tail, and which pattern matched.
func needsInput(tail string) (string, bool) {
	lines := nonEmptyLines(tail)
	if len(lines) == 0 {
		return "", false
	}

	if menuChoiceRe.MatchString(tail) {
		return "numbered choice list", true
	}

	// Anchor the remaining patterns to the last couple of lines: a question two
	// screens back has already been answered.
	for _, line := range lastN(lines, 2) {
		trimmed := strings.TrimSpace(line)
		switch {
		case yesNoRe.MatchString(trimmed):
			return "yes/no confirmation", true
		case blockingPromptRe.MatchString(trimmed):
			return "blocking prompt", true
		case questionRe.MatchString(trimmed) && strings.HasSuffix(trimmed, "?"):
			return "question awaiting an answer", true
		}
	}
	return "", false
}

// Tail returns the last n non-trailing-blank lines of s.
func Tail(s string, n int) string {
	if n <= 0 || s == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	// Drop trailing blank lines so a pane padded to the terminal height still
	// exposes its real last line.
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

func lastN(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}

// UIActivity maps a state onto the short strings the dashboard renders.
func UIActivity(s domain.AgentState) string {
	switch s {
	case domain.AgentStateWorking:
		return "busy"
	case domain.AgentStateWaitingInput:
		return "input"
	case domain.AgentStateWaitingBackground:
		return "bgwait"
	case domain.AgentStateIdle:
		return "idle"
	default:
		return ""
	}
}
