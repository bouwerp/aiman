package ui

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
)

func TestTodoProgressIgnoresDropped(t *testing.T) {
	l := domain.TodoList{Items: []domain.TodoItem{
		{ID: "t1", State: domain.TodoDone}, {ID: "t2", State: domain.TodoActive},
		{ID: "t3", State: domain.TodoPending}, {ID: "t4", State: domain.TodoDropped},
	}}
	done, total := todoProgress(l)
	if done != 1 || total != 3 {
		t.Fatalf("got %d/%d, want 1/3", done, total)
	}
}

func TestPromptMarker(t *testing.T) {
	sent := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		st   server.SteerStatus
		want string
	}{
		{"queued", server.SteerStatus{Queued: 1}, "queued"},
		{"delivered after send", server.SteerStatus{LastDeliveredAt: sent.Add(time.Second)}, "delivered"},
		{"delivery older than send", server.SteerStatus{LastDeliveredAt: sent.Add(-time.Minute)}, "queued"},
		{"agent ended", server.SteerStatus{Queued: 1, Held: "ended"}, "not delivered"},
	}
	if got := promptMarker(time.Time{}, server.SteerStatus{Queued: 1}); got != "" {
		t.Fatalf("unsent: %q", got)
	}
	for _, c := range cases {
		if got := promptMarker(sent, c.st); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestTodoSnapshotHasList(t *testing.T) {
	never := newTodoSnapshot(domain.TodoList{}, server.SteerStatus{})
	if never.HasList {
		t.Fatal("rev 0 and no items should not have a list")
	}
	emptied := newTodoSnapshot(domain.TodoList{Rev: 2}, server.SteerStatus{Queued: 1})
	if !emptied.HasList || emptied.List.Rev != 2 || emptied.Steer.Queued != 1 {
		t.Fatalf("emptied list: %+v", emptied)
	}
	items := newTodoSnapshot(domain.TodoList{Items: []domain.TodoItem{{ID: "t1"}}}, server.SteerStatus{})
	if !items.HasList || len(items.List.Items) != 1 {
		t.Fatalf("items: %+v", items)
	}
}

func TestTodoSummaryDecodes(t *testing.T) {
	var s todoSummary
	if err := json.Unmarshal([]byte(`{"done":3,"total":7,"active":"Run preprod","rev":9}`), &s); err != nil {
		t.Fatal(err)
	}
	if s != (todoSummary{Done: 3, Total: 7, Active: "Run preprod", Rev: 9}) {
		t.Fatalf("%+v", s)
	}
}
