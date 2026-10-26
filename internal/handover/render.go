package handover

import (
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/todo"
)

// PaneLines is the pane-tail bound named in the handover heading.
// Callers trim to this many lines; the heading uses the same number.
const PaneLines = 40

// Input is the data a handover note is rendered from.
// Render does not read git, the pane, or the clock.
type Input struct {
	Label       string // session Name, else IssueKey, else ID
	Reason      string // "archive" | "forget" | "restart" | "agent_switch" | "pre_compact"
	Agent       string
	Branch      string
	BaseBranch  string
	Todos       *domain.TodoList // nil: no list
	Undelivered []string         // steering note texts, oldest first
	DiffStat    string           // "" renders "(unavailable)"
	HookTitle   string
	HookMessage string
	PaneTail    string // already cleaned, at most PaneLines lines
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
	writeSection(&b, "## To-do list", formatTodos(in.Todos))
	if len(in.Undelivered) > 0 {
		writeSection(&b, "## Undelivered notes", fenceNotes(in.Undelivered))
	}
	writeSection(&b, fmt.Sprintf("## Changed files (git diff --stat %s)", in.BaseBranch), codeFence(diffStat(in.DiffStat)))
	if status := agentStatus(in); status != "" {
		writeSection(&b, "## Last agent status", status)
	}
	writeSection(&b, fmt.Sprintf("## Last screen (%d lines)", PaneLines), codeFence(in.PaneTail))
	return b.String()
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

func agentStatus(in Input) string {
	var lines []string
	if in.HookTitle != "" {
		lines = append(lines, "Title: "+in.HookTitle)
	}
	if in.HookMessage != "" {
		lines = append(lines, "Message: "+in.HookMessage)
	}
	return strings.Join(lines, "\n")
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
