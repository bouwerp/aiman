package handover

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestRenderSections(t *testing.T) {
	in := Input{Label: "WTB-12", Reason: "restart", Agent: "claude", Branch: "wtb-12", BaseBranch: "main",
		Todos: &domain.TodoList{Items: []domain.TodoItem{
			{ID: "t1", Text: "Write test", State: domain.TodoState("done")},
			{ID: "t2", Text: "Fix parser", State: domain.TodoState("active")},
			{ID: "t3", Text: "Docs", State: domain.TodoState("pending")}}},
		Undelivered: []string{"[aiman] Your to-do list was changed by user:\n+ t4 Add rollback test"},
		DiffStat:    " a.go | 3 ++-\n 1 file changed", HookTitle: "Fixing parser", PaneTail: "line1\nline2"}
	out := Render(in)
	prev := -1
	for _, want := range []string{"DO NOT COMMIT", "# Handover: WTB-12", "Reason: restart",
		"Reason: restart · Agent: claude · Branch: wtb-12 (base main)",
		"t1 ✓ Write test", "t2 ▸ Fix parser", "t3 ○ Docs", "## Undelivered notes", "+ t4 Add rollback test",
		"## Changed files (git diff --stat main)", "a.go | 3 ++-", "Fixing parser", "## Last screen", "line2"} {
		i := strings.Index(out, want)
		if i < 0 {
			t.Errorf("missing %q", want)
			continue
		}
		if i < prev {
			t.Errorf("%q out of order", want)
		}
		prev = i
	}
	if strings.Contains(NoteBody(in), "DO NOT COMMIT") {
		t.Error("note body must not carry the file header")
	}
	if out != domain.AimanScaffoldHeader+NoteBody(in) {
		t.Error("Render must be the scaffold header plus NoteBody")
	}
}

func TestRenderOmitsUndeliveredWhenNone(t *testing.T) {
	if strings.Contains(Render(Input{Label: "x", Reason: "forget"}), "## Undelivered notes") {
		t.Fatal("undelivered section present with no notes")
	}
}

func TestRenderNoTodoList(t *testing.T) {
	out := Render(Input{Todos: nil, Reason: "archive"})
	if !strings.Contains(out, "No to-do list.") {
		t.Fatalf("missing empty-list line:\n%s", out)
	}
}

func TestRenderDiffUnavailable(t *testing.T) {
	out := Render(Input{BaseBranch: "main", DiffStat: ""})
	if !strings.Contains(out, "## Changed files (git diff --stat main)") || !strings.Contains(out, "(unavailable)") {
		t.Fatalf("diff:\n%s", out)
	}
}

func TestRenderFenceLongerThanContent(t *testing.T) {
	tail := "x\n```\ny"
	out := Render(Input{PaneTail: tail})
	if !strings.Contains(out, "````\n"+tail+"\n````") {
		t.Fatalf("fence:\n%s", out)
	}
	if strings.Contains(out, "`````") {
		t.Fatal("fence is longer than the backtick run plus one")
	}
	run := "````"
	longer := Render(Input{PaneTail: run})
	if !strings.Contains(longer, "`````\n"+run+"\n`````") {
		t.Fatalf("run of four:\n%s", longer)
	}
}

func TestRenderIsDeterministic(t *testing.T) {
	in := Input{Label: "WTB-12", Reason: "restart", Agent: "claude", Branch: "wtb-12", BaseBranch: "main",
		HookTitle: "Fixing parser", HookMessage: "still running", PaneTail: "line1\nline2", DiffStat: "a.go | 1 +"}
	first := Render(in)
	second := Render(in)
	if first != second {
		t.Fatal("Render is not stable")
	}
}

func TestAbstractPrefersActiveTodo(t *testing.T) {
	in := Input{Reason: "restart", HookTitle: "Fixing parser", Todos: &domain.TodoList{Items: []domain.TodoItem{
		{ID: "t1", Text: "Write test", State: domain.TodoDone},
		{ID: "t2", Text: "Fix parser", State: domain.TodoActive},
	}}}
	if got := Abstract(in); got != "Fix parser" {
		t.Fatalf("active: %q", got)
	}
	in.Todos = nil
	if got := Abstract(in); got != "Fixing parser" {
		t.Fatalf("title: %q", got)
	}
	in.HookTitle = ""
	if got := Abstract(in); got != "Handover at restart" {
		t.Fatalf("fallback: %q", got)
	}
}

func TestRenderAppendsDroppedTodos(t *testing.T) {
	in := Input{Todos: &domain.TodoList{Items: []domain.TodoItem{
		{ID: "t1", Text: "Keep", State: domain.TodoDone},
		{ID: "t2", Text: "Skip", State: domain.TodoDropped},
		{ID: "t3", Text: "Next", State: domain.TodoPending},
	}}}
	out := NoteBody(in)
	kept := strings.Index(out, "t1 ✓ Keep")
	next := strings.Index(out, "t3 ○ Next")
	dropped := "t2 " + domain.TodoDropped.Glyph() + " Skip (dropped)"
	at := strings.Index(out, dropped)
	if kept < 0 || next < 0 || at < 0 || kept > next || next > at {
		t.Fatalf("dropped line %q not after the live list:\n%s", dropped, out)
	}
}

func TestRenderAgentStatusOmitsEmptyLines(t *testing.T) {
	both := NoteBody(Input{HookTitle: "Fixing parser", HookMessage: "almost"})
	if !strings.Contains(both, "## Last agent status") || !strings.Contains(both, "Title: Fixing parser") || !strings.Contains(both, "Message: almost") {
		t.Fatalf("both:\n%s", both)
	}
	titleOnly := NoteBody(Input{HookTitle: "Fixing parser"})
	if strings.Contains(titleOnly, "Message:") {
		t.Fatalf("empty message leaked:\n%s", titleOnly)
	}
	messageOnly := NoteBody(Input{HookMessage: "almost"})
	if strings.Contains(messageOnly, "Title:") {
		t.Fatalf("empty title leaked:\n%s", messageOnly)
	}
	if strings.Contains(NoteBody(Input{}), "## Last agent status") {
		t.Fatal("status section present with no title or message")
	}
}

func TestHandoverFileName(t *testing.T) {
	if domain.AimanHandoverFileName != ".aiman_handover.md" {
		t.Fatalf("file name %q", domain.AimanHandoverFileName)
	}
}
