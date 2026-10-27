package coordinator

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

type mutation struct {
	session string
	author  domain.TodoAuthor
}

type fakeTodos struct {
	mu        sync.Mutex
	lists     map[string]domain.TodoList
	mutations []mutation
}

func (f *fakeTodos) Get(id string) (domain.TodoList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.lists[id]
	l.Items = append([]domain.TodoItem(nil), l.Items...)
	return l, nil
}

func (f *fakeTodos) Mutate(_ context.Context, id string, author domain.TodoAuthor, fn func(*domain.TodoList) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.lists[id]
	l.Items = append([]domain.TodoItem(nil), l.Items...)
	if err := fn(&l); err != nil {
		return err
	}
	f.lists[id] = l
	f.mutations = append(f.mutations, mutation{session: id, author: author})
	return nil
}

// t3 returns the parent p's item t3.
func (f *fakeTodos) t3(t *testing.T) domain.TodoItem {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, it := range f.lists["p"].Items {
		if it.ID == "t3" {
			return it
		}
	}
	t.Fatal("item p/t3 not found")
	return domain.TodoItem{}
}

type emitted struct {
	session string
	kind    domain.NotifyKind
	detail  string
}

type fakeNotify struct {
	mu     sync.Mutex
	events []emitted
}

func (f *fakeNotify) Emit(_ context.Context, sessionID, _ string, kind domain.NotifyKind, detail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, emitted{session: sessionID, kind: kind, detail: detail})
	return nil
}

// fixture is a parent "p" on branch feat/x whose item t3 links to child c1.
type fixture struct {
	todos   *fakeTodos
	notify  *fakeNotify
	probed  []string
	tracker *Tracker
}

func newFixture(ref string, pr PRState) *fixture {
	f := &fixture{
		todos: &fakeTodos{lists: map[string]domain.TodoList{
			"p": {SessionID: "p", Items: []domain.TodoItem{
				{ID: "t3", Text: "Add the parser", State: domain.TodoPending, Ref: ref},
			}},
		}},
		notify: &fakeNotify{},
	}
	sessions := []domain.Session{
		{ID: "p", Name: "parent", Branch: "feat/x"},
		{ID: "c1", Name: "child", Branch: "feat/x--t3", BaseBranch: "feat/x", ParentID: "p"},
	}
	f.tracker = &Tracker{
		Todos:    f.todos,
		Sessions: func() ([]domain.Session, error) { return sessions, nil },
		PR: func(_ context.Context, child domain.Session) (PRState, error) {
			f.probed = append(f.probed, child.ID)
			return pr, nil
		},
		Notify: f.notify,
	}
	return f
}

func TestTrackMarksMergedChild(t *testing.T) {
	f := newFixture("child:c1", PRState{Base: "feat/x", Merged: true})
	f.tracker.TrackOnce(context.Background())

	if it := f.todos.t3(t); it.State != domain.TodoDone {
		t.Fatalf("t3 state = %s, want done", it.State)
	}
	if len(f.todos.mutations) != 1 || f.todos.mutations[0].author != domain.AuthorAiman {
		t.Fatalf("mutations = %+v, want one by aiman", f.todos.mutations)
	}

	f.tracker.TrackOnce(context.Background())
	if len(f.todos.mutations) != 1 {
		t.Fatalf("a done item was written again: %+v", f.todos.mutations)
	}
}

func TestTrackIgnoresOtherBase(t *testing.T) {
	f := newFixture("child:c1", PRState{Base: "main", Merged: true})
	f.tracker.TrackOnce(context.Background())

	if it := f.todos.t3(t); it.State != domain.TodoPending || it.Ref != "child:c1" {
		t.Fatalf("t3 = %+v, want pending and still linked", it)
	}
	if len(f.todos.mutations) != 0 {
		t.Fatalf("mutations = %+v, want none", f.todos.mutations)
	}
}

func TestTrackOpenPRWhenBeforePR(t *testing.T) {
	open := PRState{Base: "feat/x", Open: true}
	cases := []struct {
		name     string
		beforePR bool
		passed   bool
		want     domain.TodoState
	}{
		{"before_pr and passed", true, true, domain.TodoDone},
		{"before_pr and not passed", true, false, domain.TodoPending},
		{"no before_pr", false, true, domain.TodoPending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture("child:c1", open)
			var passedFor, beforeFor string
			f.tracker.BeforePR = func(id string) bool { beforeFor = id; return c.beforePR }
			f.tracker.Passed = func(id string) bool { passedFor = id; return c.passed }
			f.tracker.TrackOnce(context.Background())

			if it := f.todos.t3(t); it.State != c.want {
				t.Fatalf("t3 state = %s, want %s", it.State, c.want)
			}
			if beforeFor != "p" {
				t.Fatalf("BeforePR asked for %q, want the parent p", beforeFor)
			}
			if c.beforePR && passedFor != "c1" {
				t.Fatalf("Passed asked for %q, want the child c1", passedFor)
			}
		})
	}
}

func TestTrackOpenPRNilHooksStayOpen(t *testing.T) {
	f := newFixture("child:c1", PRState{Base: "feat/x", Open: true})
	f.tracker.TrackOnce(context.Background())
	if it := f.todos.t3(t); it.State != domain.TodoPending {
		t.Fatalf("t3 state = %s, want pending", it.State)
	}
}

func TestTrackResumesFromRefs(t *testing.T) {
	first := newFixture("child:c1", PRState{Base: "feat/x", Open: true})
	first.tracker.TrackOnce(context.Background())

	// A restarted serve builds a fresh Tracker over the same stored lists.
	restarted := &Tracker{
		Todos:    first.todos,
		Sessions: first.tracker.Sessions,
		PR: func(context.Context, domain.Session) (PRState, error) {
			return PRState{Base: "feat/x", Merged: true}, nil
		},
	}
	restarted.TrackOnce(context.Background())

	if it := first.todos.t3(t); it.State != domain.TodoDone {
		t.Fatalf("t3 state = %s, want done", it.State)
	}
}

func TestTrackClearsRefOfMissingChild(t *testing.T) {
	f := newFixture("child:gone-42", PRState{Base: "feat/x", Merged: true})
	f.tracker.TrackOnce(context.Background())

	it := f.todos.t3(t)
	if it.Ref != "" || it.State != domain.TodoPending {
		t.Fatalf("t3 = %+v, want pending with no ref", it)
	}
	if len(f.probed) != 0 {
		t.Fatalf("probed %v, want no PR read", f.probed)
	}
	if len(f.notify.events) != 1 {
		t.Fatalf("events = %+v, want one", f.notify.events)
	}
	ev := f.notify.events[0]
	if ev.kind != domain.NotifyError || ev.session != "p" || !strings.Contains(ev.detail, "gone-42") {
		t.Fatalf("event = %+v, want NotifyError on p naming gone-42", ev)
	}

	f.tracker.TrackOnce(context.Background())
	if len(f.notify.events) != 1 {
		t.Fatalf("an unlinked item notified again: %+v", f.notify.events)
	}
}

func TestTrackIgnoresOtherRefs(t *testing.T) {
	f := newFixture("check:lint", PRState{Base: "feat/x", Merged: true})
	f.tracker.TrackOnce(context.Background())

	if len(f.probed) != 0 {
		t.Fatalf("probed %v, want none", f.probed)
	}
	if it := f.todos.t3(t); it.State != domain.TodoPending || it.Ref != "check:lint" {
		t.Fatalf("t3 = %+v, want unchanged", it)
	}
}

func TestTrackSkipsChildOnPRError(t *testing.T) {
	f := newFixture("child:c1", PRState{})
	f.tracker.PR = func(context.Context, domain.Session) (PRState, error) {
		return PRState{}, errors.New("gh: boom")
	}
	f.tracker.TrackOnce(context.Background())

	if it := f.todos.t3(t); it.State != domain.TodoPending || it.Ref != "child:c1" {
		t.Fatalf("t3 = %+v, want unchanged", it)
	}
	if len(f.notify.events) != 0 {
		t.Fatalf("events = %+v, want none", f.notify.events)
	}
}

func TestTrackSessionsErrorUnlinksNothing(t *testing.T) {
	f := newFixture("child:c1", PRState{})
	f.tracker.Sessions = func() ([]domain.Session, error) { return nil, errors.New("db down") }
	f.tracker.TrackOnce(context.Background())

	if len(f.todos.mutations) != 0 || len(f.notify.events) != 0 {
		t.Fatalf("mutations %+v events %+v, want none", f.todos.mutations, f.notify.events)
	}
}

func TestParsePRState(t *testing.T) {
	cases := []struct {
		raw  string
		want PRState
	}{
		{`{"state":"MERGED","mergedAt":"2026-10-20T10:00:00Z","baseRefName":"feat/x"}`, PRState{Base: "feat/x", Merged: true}},
		{`{"state":"OPEN","mergedAt":null,"baseRefName":"feat/x"}`, PRState{Base: "feat/x", Open: true}},
		{`{"state":"CLOSED","mergedAt":null,"baseRefName":"main"}`, PRState{Base: "main"}},
	}
	for _, c := range cases {
		got, err := ParsePRState([]byte(c.raw))
		if err != nil {
			t.Fatalf("ParsePRState(%s): %v", c.raw, err)
		}
		if got != c.want {
			t.Errorf("ParsePRState(%s) = %+v, want %+v", c.raw, got, c.want)
		}
	}
	if _, err := ParsePRState([]byte("not json")); err == nil {
		t.Fatal("ParsePRState(not json) = nil error")
	}
}
