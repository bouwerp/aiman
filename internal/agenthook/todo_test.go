package agenthook

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func readHookFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestExtractTodoSnapshotClaude(t *testing.T) {
	items, ok := ExtractTodoSnapshot(readHookFixture(t, "claude_posttooluse_todowrite.json"), nil)
	if !ok {
		t.Fatal("TodoWrite fixture should be a full list")
	}
	want := []domain.TodoSnapshotItem{
		{Text: "Outline README sections (overview, setup, usage)", Status: domain.TodoActive},
		{Text: "Draft README content", Status: domain.TodoPending},
		{Text: "Review and finalize README", Status: domain.TodoPending},
	}
	if len(items) != len(want) {
		t.Fatalf("len %d, want %d", len(items), len(want))
	}
	for i := range want {
		if items[i] != want[i] {
			t.Fatalf("item %d = %+v, want %+v", i, items[i], want[i])
		}
	}

	// pending|in_progress|completed are the vendor strings the fixture's
	// statuses travel through. The captured fixture has no completed row.
	mapped, ok := ExtractTodoSnapshot([]byte(`{"tool_name":"TodoWrite","tool_input":{"todos":[{"content":"a","status":"pending"},{"content":"b","status":"in_progress"},{"content":"c","status":"completed"}]}}`), nil)
	if !ok || len(mapped) != 3 {
		t.Fatalf("mapping ok=%v items=%v", ok, mapped)
	}
	if mapped[0].Status != domain.TodoPending || mapped[1].Status != domain.TodoActive || mapped[2].Status != domain.TodoDone {
		t.Fatalf("statuses %+v, want pending/active/done", mapped)
	}

	cleared, ok := ExtractTodoSnapshot([]byte(`{"tool_name":"TodoWrite","tool_input":{"todos":[]}}`), nil)
	if !ok || len(cleared) != 0 {
		t.Fatalf("empty list ok=%v items=%v", ok, cleared)
	}
}

func TestExtractTodoSnapshotClaudeTasks(t *testing.T) {
	raw := readHookFixture(t, "claude_posttooluse_taskupdate.json")
	want := []domain.TodoSnapshotItem{
		{Text: "Outline", Status: domain.TodoPending},
		{Text: "Draft", Status: domain.TodoActive},
		{Text: "Review", Status: domain.TodoDone},
	}
	var gotRef string
	items, ok := ExtractTodoSnapshot(raw, func(listRef string) ([]domain.TodoSnapshotItem, error) {
		gotRef = listRef
		return want, nil
	})
	if !ok {
		t.Fatal("readable full list should be ok")
	}
	if gotRef != "sess-1" {
		t.Fatalf("list ref %q, want session id", gotRef)
	}
	if len(items) != 3 {
		t.Fatalf("len %d", len(items))
	}
	for i := range want {
		if items[i] != want[i] {
			t.Fatalf("item %d = %+v, want %+v", i, items[i], want[i])
		}
	}

	_, ok = ExtractTodoSnapshot(raw, func(string) ([]domain.TodoSnapshotItem, error) {
		return []domain.TodoSnapshotItem{{Text: "only the updated task", Status: domain.TodoActive}}, errors.New("unreadable")
	})
	if ok {
		t.Fatal("readTasks error must not mirror a partial list")
	}
	_, ok = ExtractTodoSnapshot(raw, nil)
	if ok {
		t.Fatal("Tasks payload without a reader is not a full list")
	}

	create := readHookFixture(t, "claude_posttooluse_taskcreate.json")
	_, ok = ExtractTodoSnapshot(create, func(string) ([]domain.TodoSnapshotItem, error) {
		return nil, errors.New("missing dir")
	})
	if ok {
		t.Fatal("TaskCreate without a readable list must not be ok")
	}
}

func TestExtractTodoSnapshotRejectsUnrelated(t *testing.T) {
	cases := []string{
		`{"hook_event_name":"PostToolUse","tool_name":"Bash","tool_input":{"command":"ls"}}`,
		"not json",
		`{"tool_name":"TodoWrite"}`,
		`{"tool_name":"TodoWrite","tool_input":{"todos":{"content":"x","status":"pending"}}}`,
		`{"tool_name":"update_plan","tool_input":{"plan":[{"step":"Write code","status":"pending"}]}}`,
	}
	for _, raw := range cases {
		items, ok := ExtractTodoSnapshot([]byte(raw), func(string) ([]domain.TodoSnapshotItem, error) {
			t.Fatal("unrelated payload must not read the task list")
			return nil, nil
		})
		if ok || items != nil {
			t.Fatalf("ok=%v items=%v for %s", ok, items, raw)
		}
	}
}

func TestLoadClaudeTasksOmitsDeleted(t *testing.T) {
	dir := t.TempDir()
	writeTask := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeTask("2.json", `{"id":"2","subject":"Draft","status":"in_progress"}`)
	writeTask("1.json", `{"id":"1","subject":"Outline","status":"pending"}`)
	writeTask("3.json", `{"id":"3","subject":"Gone","status":"deleted"}`)
	writeTask("10.json", `{"id":"10","subject":"Review","status":"completed"}`)
	if err := os.WriteFile(filepath.Join(dir, ".lock"), []byte("lock"), 0o644); err != nil {
		t.Fatal(err)
	}

	items, err := LoadClaudeTasks(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.TodoSnapshotItem{
		{Text: "Outline", Status: domain.TodoPending},
		{Text: "Draft", Status: domain.TodoActive},
		{Text: "Review", Status: domain.TodoDone},
	}
	if len(items) != len(want) {
		t.Fatalf("len %d items=%+v", len(items), items)
	}
	for i := range want {
		if items[i] != want[i] {
			t.Fatalf("item %d = %+v, want %+v", i, items[i], want[i])
		}
	}

	if _, err := LoadClaudeTasks(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing task dir must fail so a partial list is not mirrored")
	}
}
