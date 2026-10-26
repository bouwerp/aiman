package handover

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestRenderSections(t *testing.T) {
	in := Input{Label: "WTB-12", Reason: "restart", Agent: "claude", Branch: "wtb-12", BaseBranch: "main",
		Todos: &domain.TodoList{Items: []domain.TodoItem{
			{ID: "t1", Text: "Write test", State: domain.TodoDone},
			{ID: "t2", Text: "Fix parser", State: domain.TodoActive},
			{ID: "t3", Text: "Docs", State: domain.TodoPending}}},
		Undelivered: []string{"[aiman] Your to-do list was changed by user:\n+ t4 Add rollback test"},
		DiffStat:    " a.go | 3 ++-\n 1 file changed", HookTitle: "Fixing parser", HookMessage: "almost",
		Blocked: "permission prompt", PaneTail: "line1\nline2",
		Evidence: Evidence{
			TestCommand: "go test ./...",
			TestOutput:  "FAIL parser",
			ErrorTrace:  "panic: parser",
			Commands:    []string{"go test ./...", "git status"},
			Files:       []string{"b.go"},
		},
		Verification: "passed",
	}
	out := Render(in)
	prev := -1
	for _, want := range []string{
		"DO NOT COMMIT", "# Handover: WTB-12", "Reason: restart",
		"Reason: restart · Agent: claude · Branch: wtb-12 (base main)",
		"## Goal", "Fix parser",
		"## Constraints & Preferences", "See .aiman_task.md",
		"## Progress", "Done: 1", "In progress: 1", "Pending: 1", "Blocked: permission prompt",
		"t1 ✓ Write test", "t2 ▸ Fix parser", "t3 ○ Docs",
		"## Key Decisions", "None recorded",
		"## Next Steps", "- t2 ▸ Fix parser", "- t3 ○ Docs",
		"## Critical Context", "Agent: almost", "Undelivered notes", "+ t4 Add rollback test",
		"## Evidence", "Diff (git diff --stat main)", "a.go | 3 ++-", "Last screen (40 lines)", "line2",
		"Test command", "go test ./...", "Test output", "FAIL parser", "Error", "panic: parser",
		"Files", "b.go", "Commands", "git status",
		"## Verification", "passed",
	} {
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
	if missing := MissingSections(out); len(missing) != 0 {
		t.Fatalf("missing sections: %v", missing)
	}
}

func TestRenderGoalFallsBackToHookTitleAndDefault(t *testing.T) {
	title := NoteBody(Input{Reason: "restart", HookTitle: "Fixing parser"})
	if !strings.Contains(title, "## Goal\n\nFixing parser") {
		t.Fatalf("hook title goal:\n%s", title)
	}
	bare := NoteBody(Input{Reason: "restart"})
	if !strings.Contains(bare, "## Goal\n\nContinue the session.") {
		t.Fatalf("default goal:\n%s", bare)
	}
}

func TestRenderCriticalContextNone(t *testing.T) {
	out := NoteBody(Input{Reason: "forget"})
	if !strings.Contains(out, "## Critical Context\n\nNone.") {
		t.Fatalf("critical context:\n%s", out)
	}
	if strings.Contains(out, "## Undelivered notes") {
		t.Fatal("legacy undelivered heading present")
	}
}

func TestRenderNoTodoList(t *testing.T) {
	out := Render(Input{Todos: nil, Reason: "archive"})
	for _, want := range []string{"No to-do list.", "Blocked: none", "No open items."} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q:\n%s", want, out)
		}
	}
}

func TestRenderDiffUnavailable(t *testing.T) {
	out := Render(Input{BaseBranch: "main", DiffStat: ""})
	if !strings.Contains(out, "Diff (git diff --stat main):") || !strings.Contains(out, "(unavailable)") {
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

func TestRenderFencesDiffStatNotesAndBacktickTodos(t *testing.T) {
	diff := "x\n```\ny"
	note := "look at ``` this"
	todoText := "ship ``` now"
	line := "t1 " + domain.TodoActive.Glyph() + " " + todoText
	out := Render(Input{
		BaseBranch:  "main",
		DiffStat:    diff,
		Undelivered: []string{note},
		Todos: &domain.TodoList{Rev: 1, Items: []domain.TodoItem{
			{ID: "t1", Text: todoText, State: domain.TodoActive},
			{ID: "t2", Text: "plain line", State: domain.TodoPending},
		}},
		PaneTail: "screen line",
	})
	for _, want := range []string{
		"````\n" + diff + "\n````",
		"````\n" + note + "\n````",
		"````\n" + line + "\n````",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing fence:\n%s\n---\n%s", want, out)
		}
	}
	evidence := strings.Index(out, "## Evidence")
	contextHeading := strings.Index(out, "## Critical Context")
	if evidence < 0 || contextHeading < 0 || evidence < contextHeading || evidence > strings.Index(out, diff) {
		t.Fatalf("evidence heading is not between context and the diff:\n%s", out)
	}
	if strings.Index(out, note) > evidence {
		t.Fatalf("undelivered note leaked into evidence:\n%s", out)
	}
	plain := "t2 " + domain.TodoPending.Glyph() + " plain line"
	if !strings.Contains(out, plain) {
		t.Fatalf("plain to-do line missing:\n%s", out)
	}
	if strings.Contains(out, "````\n"+plain+"\n````") {
		t.Fatal("a to-do line with no backtick run was fenced")
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

func TestRenderHookMessageOnlyInCriticalContext(t *testing.T) {
	both := NoteBody(Input{HookTitle: "Fixing parser", HookMessage: "almost"})
	if !strings.Contains(both, "## Goal\n\nFixing parser") || !strings.Contains(both, "Agent: almost") {
		t.Fatalf("both:\n%s", both)
	}
	if strings.Contains(both, "Title: Fixing parser") {
		t.Fatalf("legacy title label leaked:\n%s", both)
	}
	titleOnly := NoteBody(Input{HookTitle: "Fixing parser"})
	if strings.Contains(titleOnly, "Agent: almost") {
		t.Fatalf("empty message leaked:\n%s", titleOnly)
	}
	messageOnly := NoteBody(Input{HookMessage: "almost"})
	if !strings.Contains(messageOnly, "Agent: almost") {
		t.Fatalf("message-only context:\n%s", messageOnly)
	}
	if !strings.Contains(NoteBody(Input{}), "## Critical Context\n\nNone.") {
		t.Fatal("empty hook message did not render as None.")
	}
}

func TestMissingSections(t *testing.T) {
	all := MissingSections("")
	if len(all) != len(RequiredSections) {
		t.Fatalf("empty markdown missing %d, want %d", len(all), len(RequiredSections))
	}
	rendered := Render(Input{Reason: "restart", PaneTail: "x"})
	if missing := MissingSections(rendered); len(missing) != 0 {
		t.Fatalf("rendered missing: %v", missing)
	}
	without := strings.Replace(rendered, "## Verification", "## Not verification", 1)
	missing := MissingSections(without)
	if len(missing) != 1 || missing[0] != "## Verification" {
		t.Fatalf("missing = %v", missing)
	}
}

func TestVerificationDefaultsToNotRecorded(t *testing.T) {
	if got := verification(""); got != "not recorded" {
		t.Fatalf("verification = %q", got)
	}
}

func TestHandoverFileName(t *testing.T) {
	if domain.AimanHandoverFileName != ".aiman_handover.md" {
		t.Fatalf("file name %q", domain.AimanHandoverFileName)
	}
}
