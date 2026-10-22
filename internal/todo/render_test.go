package todo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestFormatSteerNoteExactText(t *testing.T) {
	list := domain.TodoList{Items: []domain.TodoItem{{ID: "t1", Text: "Write spec", State: domain.TodoDone}, {ID: "t3", Text: "Run preprod --check --dry", State: domain.TodoActive}, {ID: "t4", Text: "Add rollback test", State: domain.TodoPending}}}
	ops := []Op{{Kind: OpAdd, ID: "t4", Text: "Add rollback test"}, {Kind: OpEdit, ID: "t3", Text: "Run preprod --check --dry"}}
	want := "[aiman] Your to-do list was changed by user:\n+ t4 Add rollback test\n~ t3 now: Run preprod --check --dry\nCurrent list:\nt1 ✓ Write spec\nt3 ▸ Run preprod --check --dry\nt4 ○ Add rollback test\nAdjust your work to match. Update the list with `aiman todo` using these ids."
	if got := FormatSteerNote([]domain.TodoAuthor{domain.AuthorUser}, ops, list); got != want {
		t.Fatalf("got\n%s", got)
	}
}

func TestFormatSteerNoteLinesAndAuthors(t *testing.T) {
	ops := []Op{
		{Kind: OpState, ID: "t2", State: domain.TodoDone},
		{Kind: OpState, ID: "t3", State: domain.TodoActive},
		{Kind: OpState, ID: "t4", State: domain.TodoPending},
		{Kind: OpState, ID: "t5", State: domain.TodoDropped},
		{Kind: OpRemove, ID: "t6"},
		{Kind: OpMove, ID: "t4", ToIndex: 1},
	}
	got := FormatSteerNote([]domain.TodoAuthor{domain.AuthorUser, domain.AuthorAiman, domain.AuthorVerifier}, ops, domain.TodoList{})
	for _, w := range []string{"changed by user, aiman and verifier:", "\n✓ t2 done\n", "\n▸ t3 active\n", "\n○ t4 pending\n", "\n- t5 dropped\n", "\n- t6 removed\n", "\n↕ t4 moved to 2\n"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, got)
		}
	}
	if g := FormatSteerNote([]domain.TodoAuthor{domain.AuthorUser, domain.AuthorAiman}, nil, domain.TodoList{}); !strings.Contains(g, "by user and aiman:") {
		t.Errorf("two authors: %s", g)
	}
}

func TestFormatSteerNoteSkipsRef(t *testing.T) {
	ops := []Op{{Kind: OpRef, ID: "t3", Ref: "PROJ-1"}, {Kind: OpAdd, ID: "t4", Text: "X"}}
	got := FormatSteerNote([]domain.TodoAuthor{domain.AuthorUser}, ops, domain.TodoList{})
	if strings.Contains(got, "t3") || strings.Contains(got, "PROJ-1") {
		t.Fatalf("ref leaked:\n%s", got)
	}
	if !strings.Contains(got, "+ t4 X") {
		t.Fatalf("add missing:\n%s", got)
	}
}

func sampleList() domain.TodoList {
	return domain.TodoList{Rev: 1, Items: []domain.TodoItem{
		{ID: "t1", Text: "Write spec", State: domain.TodoDone},
		{ID: "t3", Text: "Run it", State: domain.TodoActive},
		{ID: "t4", Text: "Add test", State: domain.TodoPending},
		{ID: "t5", Text: "Old idea", State: domain.TodoDropped},
	}}
}

func TestFormatListSkipsDropped(t *testing.T) {
	want := "t1 ✓ Write spec\nt3 ▸ Run it\nt4 ○ Add test"
	if got := FormatList(sampleList()); got != want {
		t.Fatalf("got %q", got)
	}
	if got := FormatLine(sampleList().Items[1]); got != "t3 ▸ Run it" {
		t.Fatalf("got %q", got)
	}
}

func TestRenderStartsWithScaffoldHeader(t *testing.T) {
	got := Render(sampleList())
	if !strings.HasPrefix(got, domain.AimanScaffoldHeader+"# To-do list\n") {
		t.Fatalf("bad start:\n%s", got)
	}
	for _, w := range []string{"- [x] t1 Write spec\n", "- [ ] t3 ▸ Run it (active)\n", "- [ ] t4 Add test\n", "- [~] t5 Old idea (dropped)\n", "Update it with `aiman todo`; ids are stable.\n"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in\n%s", w, got)
		}
	}
}

func TestWriteRenderedSkipsUnchanged(t *testing.T) {
	dir := t.TempDir()
	list := sampleList()
	if changed, err := WriteRendered(dir, list); err != nil || !changed {
		t.Fatalf("first: %v %v", changed, err)
	}
	p := filepath.Join(dir, domain.AimanTodoFileName)
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	list.Rev++
	if changed, err := WriteRendered(dir, list); err != nil || changed {
		t.Fatalf("second: %v %v", changed, err)
	}
	if st, _ := os.Stat(p); !st.ModTime().Equal(old) {
		t.Fatal("mtime changed")
	}
	list.Items[2].State = domain.TodoDone
	if changed, _ := WriteRendered(dir, list); !changed {
		t.Fatal("expected change")
	}
}
