package handover

import (
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/todo"
)

// PaneLines is the pane-tail bound named in the handover evidence.
// Callers trim to this many lines; the heading uses the same number.
const PaneLines = 40

// RequiredSections is the fixed handover skeleton. The order is the order the
// sections appear in the file; a caller that adds a heading must add it here.
var RequiredSections = []string{
	"## Goal",
	"## Constraints & Preferences",
	"## Progress",
	"## Key Decisions",
	"## Next Steps",
	"## Critical Context",
	"## Evidence",
	"## Verification",
}

// Evidence is verification material captured outside the pane. Empty fields are
// omitted; the writer supplies them when a test command or verifier record is
// available.
type Evidence struct {
	TestCommand string
	TestOutput  string
	ErrorTrace  string
	Commands    []string
	Files       []string
}

// Input is the data a handover note is rendered from.
// Render does not read git, the pane, or the clock.
type Input struct {
	Label        string // session Name, else IssueKey, else ID
	Reason       string // "archive" | "forget" | "restart" | "agent_switch" | "pre_compact"
	Agent        string
	Branch       string
	BaseBranch   string
	Todos        *domain.TodoList // nil: no list
	Undelivered  []string         // steering note texts, oldest first
	DiffStat     string           // "" renders "(unavailable)"
	HookTitle    string
	HookMessage  string
	Blocked      string // waiting_input reason, "" renders "none"
	PaneTail     string // already cleaned, at most PaneLines lines
	Evidence     Evidence
	Verification string // "passed" | "incomplete" | "not recorded"
}

// Render is the full handover file: the shared scaffold header plus NoteBody.
func Render(in Input) string {
	return domain.AimanScaffoldHeader + NoteBody(in)
}

// NoteBody is the handover markdown without the scaffold header.
func NoteBody(in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Handover: %s\n\n", in.Label)
	fmt.Fprintf(&b, "Reason: %s · Agent: %s · Branch: %s (base %s)\n\n", in.Reason, in.Agent, in.Branch, in.BaseBranch)
	writeSection(&b, "## Goal", goal(in))
	writeSection(&b, "## Constraints & Preferences", constraints())
	writeSection(&b, "## Progress", progress(in))
	writeSection(&b, "## Key Decisions", "None recorded in this handover.")
	writeSection(&b, "## Next Steps", nextSteps(in.Todos))
	writeSection(&b, "## Critical Context", criticalContext(in))
	writeSection(&b, "## Evidence", evidence(in))
	writeSection(&b, "## Verification", verification(in.Verification))
	return b.String()
}

// MissingSections returns the required headings absent from md, in skeleton
// order. A heading counts only when it starts a line, so prose cannot satisfy
// the check.
func MissingSections(md string) []string {
	have := make(map[string]bool, len(RequiredSections))
	for _, line := range strings.Split(md, "\n") {
		have[strings.TrimSpace(line)] = true
	}
	var missing []string
	for _, heading := range RequiredSections {
		if !have[heading] {
			missing = append(missing, heading)
		}
	}
	return missing
}

// Abstract is the active to-do text, else the hook title, else "Handover at <reason>".
func Abstract(in Input) string {
	if text, ok := activeText(in.Todos); ok {
		return text
	}
	if in.HookTitle != "" {
		return in.HookTitle
	}
	return "Handover at " + in.Reason
}

func goal(in Input) string {
	if text, ok := activeText(in.Todos); ok {
		return text
	}
	if strings.TrimSpace(in.HookTitle) != "" {
		return in.HookTitle
	}
	return "Continue the session."
}

func constraints() string {
	return "See .aiman_task.md and the to-do list."
}

func progress(in Input) string {
	done, active, pending := countStates(in.Todos)
	var b strings.Builder
	fmt.Fprintf(&b, "Done: %d · In progress: %d · Pending: %d · Blocked: %s\n\n", done, active, pending, blockedText(in.Blocked))
	b.WriteString(formatTodos(in.Todos))
	return b.String()
}

func countStates(list *domain.TodoList) (done, active, pending int) {
	if list == nil {
		return 0, 0, 0
	}
	for _, it := range list.Items {
		switch it.State {
		case domain.TodoDone:
			done++
		case domain.TodoActive:
			active++
		case domain.TodoPending:
			pending++
		}
	}
	return done, active, pending
}

func blockedText(blocked string) string {
	if text := strings.TrimSpace(blocked); text != "" {
		return text
	}
	return "none"
}

func nextSteps(list *domain.TodoList) string {
	if list == nil {
		return "No open items."
	}
	var lines []string
	for _, state := range []domain.TodoState{domain.TodoActive, domain.TodoPending} {
		for _, it := range list.Items {
			if it.State == state {
				lines = append(lines, "- "+todo.FormatLine(it))
			}
		}
	}
	if len(lines) == 0 {
		return "No open items."
	}
	return strings.Join(lines, "\n")
}

func criticalContext(in Input) string {
	var parts []string
	if msg := strings.TrimSpace(in.HookMessage); msg != "" {
		parts = append(parts, "Agent: "+msg)
	}
	if len(in.Undelivered) > 0 {
		parts = append(parts, "Undelivered notes:\n"+fenceNotes(in.Undelivered))
	}
	if len(parts) == 0 {
		return "None."
	}
	return strings.Join(parts, "\n\n")
}

func evidence(in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Diff (git diff --stat %s):\n", in.BaseBranch)
	b.WriteString(evidenceBlock(diffStat(in.DiffStat)))
	b.WriteString("\n\n")
	fmt.Fprintf(&b, "Last screen (%d lines):\n", PaneLines)
	b.WriteString(evidenceBlock(in.PaneTail))
	writeEvidenceField(&b, "Test command", in.Evidence.TestCommand)
	writeEvidenceField(&b, "Test output", in.Evidence.TestOutput)
	writeEvidenceField(&b, "Error", in.Evidence.ErrorTrace)
	writeEvidenceField(&b, "Files", strings.Join(in.Evidence.Files, " · "))
	writeEvidenceField(&b, "Commands", strings.Join(in.Evidence.Commands, " · "))
	return b.String()
}

func writeEvidenceField(b *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	fmt.Fprintf(b, "\n\n%s:\n%s", label, evidenceBlock(value))
}

func evidenceBlock(content string) string {
	if strings.TrimSpace(content) == "" {
		return "(none)"
	}
	return codeFence(content)
}

func verification(value string) string {
	if text := strings.TrimSpace(value); text != "" {
		return text
	}
	return "not recorded"
}

func activeText(list *domain.TodoList) (string, bool) {
	if list == nil {
		return "", false
	}
	for _, it := range list.Items {
		if it.State == domain.TodoActive {
			return it.Text, true
		}
	}
	return "", false
}

func formatTodos(list *domain.TodoList) string {
	if list == nil {
		return "No to-do list."
	}
	var lines []string
	for _, it := range list.Items {
		if it.State == domain.TodoDropped {
			continue
		}
		lines = append(lines, fenceTodoLine(it.Text, todo.FormatLine(it)))
	}
	for _, it := range list.Items {
		if it.State != domain.TodoDropped {
			continue
		}
		lines = append(lines, fenceTodoLine(it.Text, todo.FormatLine(it)+" (dropped)"))
	}
	return strings.Join(lines, "\n")
}

func fenceTodoLine(text, line string) string {
	if !strings.Contains(text, "`") {
		return line
	}
	return codeFence(line)
}

func fenceNotes(notes []string) string {
	fenced := make([]string, len(notes))
	for i, note := range notes {
		fenced[i] = codeFence(note)
	}
	return strings.Join(fenced, "\n\n")
}

func diffStat(stat string) string {
	if stat == "" {
		return "(unavailable)"
	}
	return stat
}

func writeSection(b *strings.Builder, heading, body string) {
	b.WriteString(heading)
	b.WriteString("\n\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
}

// codeFence wraps content in a backtick fence longer than any run inside it,
// so a section that itself contains fences stays one code block.
func codeFence(content string) string {
	n := 3
	run := 0
	for _, r := range content {
		if r != '`' {
			run = 0
			continue
		}
		run++
		if run+1 > n {
			n = run + 1
		}
	}
	ticks := strings.Repeat("`", n)
	return ticks + "\n" + content + "\n" + ticks
}
