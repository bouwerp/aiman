package pane

import (
	"regexp"
	"strings"
)

// QuestionReason is what the rules report for a question awaiting an answer.
const QuestionReason = "question awaiting an answer"

// rightAlignedIndent marks status chrome set against the right edge, such as
// Claude Code's "✔ Update installed" and "◐ medium · /effort" rows. Agents do not
// indent prose this far.
const rightAlignedIndent = 20

var (
	// An agent's input box row. Grok draws it inside a box.
	composerRowRe = regexp.MustCompile(`^\s*(?:│\s*)?[❯›](?:\s|$)`)

	// Box rules drawn around the composer, already trimmed of any side border.
	boxRuleRe = regexp.MustCompile(`^[─━═╭╮╰╯┌┐└┘├┤]+$`)

	// A finished-turn marker occupying its own row. It is anchored, unlike
	// turnFinishedRe, because this one decides which row counts as prose. Claude
	// Code picks a new past-tense verb per turn but always marks it with ✻, and
	// Grok reports tenths of a second.
	finishedRowRe = regexp.MustCompile(`(?i)^(?:✻\s+\p{L}+|brewed|thought|pondered|worked|ruminated) for\s+(?:\d+h\s*)?(?:\d+m\s*)?\d+(?:\.\d+)?s\b`)
)

// AsksQuestion reports an agent's finished turn ending in a question: above the
// last composer row, past blank rows and chrome, the first row of prose ends in
// `?`. Text in the composer is below that row and never counts, whether typed or
// a dim suggestion, and a screen with no composer is false.
//
// This is deliberately wider than questionRe: "want me to continue?" is an agent
// waiting on a decision. A false match costs one notification per turn.
func AsksQuestion(screen string) bool {
	rows := strings.Split(strings.ReplaceAll(StripANSI(screen), " ", " "), "\n")
	composer := lastComposerRow(rows)
	if composer < 0 {
		return false
	}
	for i := composer - 1; i >= 0; i-- {
		row := proseOf(rows[i])
		if isChromeRow(rows[i], row) {
			continue
		}
		// An earlier composer row is the operator's own prompt echoed in the
		// transcript, with no reply beneath it yet.
		if composerRowRe.MatchString(rows[i]) {
			return false
		}
		return strings.HasSuffix(row, "?")
	}
	return false
}

// lastComposerRow finds the composer in the live bottom of the screen, or -1. It
// is not searched for further up, where a `❯` is transcript.
func lastComposerRow(rows []string) int {
	end := len(rows) - 1
	for end >= 0 && strings.TrimSpace(rows[end]) == "" {
		end--
	}
	for i := end; i >= 0 && i > end-StatusLines; i-- {
		if composerRowRe.MatchString(rows[i]) {
			return i
		}
	}
	return -1
}

// proseOf removes what agents draw around a row: spaces, side borders, and the
// scrollbar Grok keeps at the right edge.
func proseOf(row string) string {
	return strings.Trim(row, " \t\r│█")
}

func isChromeRow(raw, prose string) bool {
	if prose == "" || boxRuleRe.MatchString(prose) {
		return true
	}
	if finishedRowRe.MatchString(prose) && !strings.HasSuffix(prose, "?") {
		return true
	}
	return len(raw)-len(strings.TrimLeft(raw, " ")) >= rightAlignedIndent
}
