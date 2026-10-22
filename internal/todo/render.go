package todo

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/fsutil"
)

// FormatLine is the one-line form of an item, e.g. "t3 ▸ Run preprod --check".
func FormatLine(it domain.TodoItem) string {
	return it.ID + " " + it.State.Glyph() + " " + it.Text
}

// FormatList renders one FormatLine per non-dropped item, newline-separated.
func FormatList(list domain.TodoList) string {
	var lines []string
	for _, it := range list.Items {
		if it.State == domain.TodoDropped {
			continue
		}
		lines = append(lines, FormatLine(it))
	}
	return strings.Join(lines, "\n")
}

func joinAuthors(authors []domain.TodoAuthor) string {
	names := make([]string, len(authors))
	for i, a := range authors {
		names[i] = string(a)
	}
	switch len(names) {
	case 0:
		return "user"
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func opLine(op Op) (string, bool) {
	switch op.Kind {
	case OpAdd:
		return "+ " + op.ID + " " + op.Text, true
	case OpEdit:
		return "~ " + op.ID + " now: " + op.Text, true
	case OpState:
		switch op.State {
		case domain.TodoDone:
			return "✓ " + op.ID + " done", true
		case domain.TodoActive:
			return "▸ " + op.ID + " active", true
		case domain.TodoDropped:
			return "- " + op.ID + " dropped", true
		default:
			return "○ " + op.ID + " pending", true
		}
	case OpRemove:
		return "- " + op.ID + " removed", true
	case OpMove:
		return fmt.Sprintf("↕ %s moved to %d", op.ID, op.ToIndex+1), true
	}
	return "", false
}

// FormatSteerNote is the message relayed to an agent after someone else
// changed its list. OpRef operations produce no line.
func FormatSteerNote(authors []domain.TodoAuthor, ops []Op, list domain.TodoList) string {
	var b strings.Builder
	b.WriteString("[aiman] Your to-do list was changed by " + joinAuthors(authors) + ":\n")
	for _, op := range ops {
		if line, ok := opLine(op); ok {
			b.WriteString(line + "\n")
		}
	}
	b.WriteString("Current list:\n")
	if cur := FormatList(list); cur != "" {
		b.WriteString(cur + "\n")
	}
	b.WriteString("Adjust your work to match. Update the list with `aiman todo` using these ids.")
	return b.String()
}

// Render is the content of the worktree's .aiman_todo.md. It carries no
// revision or timestamps, so an unchanged list renders identical bytes.
func Render(list domain.TodoList) string {
	var b strings.Builder
	b.WriteString(domain.AimanScaffoldHeader)
	b.WriteString("# To-do list\n\n")
	for _, it := range list.Items {
		switch it.State {
		case domain.TodoDone:
			fmt.Fprintf(&b, "- [x] %s %s\n", it.ID, it.Text)
		case domain.TodoActive:
			fmt.Fprintf(&b, "- [ ] %s (active)\n", FormatLine(it))
		case domain.TodoDropped:
			fmt.Fprintf(&b, "- [~] %s %s (dropped)\n", it.ID, it.Text)
		default:
			fmt.Fprintf(&b, "- [ ] %s %s\n", it.ID, it.Text)
		}
	}
	b.WriteString("\nUpdate it with `aiman todo`; ids are stable.\n")
	return b.String()
}

// WriteRendered writes Render(list) to dir/.aiman_todo.md only when it changed.
func WriteRendered(dir string, list domain.TodoList) (changed bool, err error) {
	return fsutil.WriteIfChanged(filepath.Join(dir, domain.AimanTodoFileName), []byte(Render(list)), 0o644)
}
