package agenthook

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// A grok PostToolUse payload is camelCase, the tool is todo_write, and a
// merge:false call carries the whole list.
func TestExtractTodoSnapshotGrok(t *testing.T) {
	items, ok := ExtractTodoSnapshot(readHookFixture(t, "grok_posttooluse_todowrite.json"), nil)
	if !ok {
		t.Fatal("todo_write merge:false fixture should be a full list")
	}
	want := []domain.TodoSnapshotItem{
		{Text: "Auth + fresh clone of ux/wtb-2024-prototype-kit", Status: domain.TodoActive},
		{Text: "Review PR diff: eligibility, lazy boundary, store/validate, tests", Status: domain.TodoPending},
		{Text: "Run frontend typecheck/lint/unit tests", Status: domain.TodoPending},
		{Text: "Fix only confirmed issues with tests", Status: domain.TodoDone},
		{Text: "Rebase, re-verify, commit, push, PR comment", Status: domain.TodoDropped},
	}
	if len(items) != len(want) {
		t.Fatalf("len %d, want %d (%+v)", len(items), len(want), items)
	}
	for i := range want {
		if items[i] != want[i] {
			t.Fatalf("item %d = %+v, want %+v", i, items[i], want[i])
		}
	}
}

// A merge:true call is a partial update and must never replace the list.
func TestExtractTodoSnapshotGrokMergeIsNotFull(t *testing.T) {
	raw := []byte(`{"toolName":"todo_write","toolInput":{"merge":true,"todos":[{"id":"3","content":"x","status":"completed"}]}}`)
	if _, ok := ExtractTodoSnapshot(raw, nil); ok {
		t.Fatal("merge:true must not be a full snapshot")
	}
}

// An empty merge:false list is the agent clearing its list.
func TestExtractTodoSnapshotGrokCleared(t *testing.T) {
	raw := []byte(`{"toolName":"todo_write","toolInput":{"todos":[],"merge":false}}`)
	items, ok := ExtractTodoSnapshot(raw, nil)
	if !ok || len(items) != 0 {
		t.Fatalf("cleared ok=%v items=%v", ok, items)
	}
}
