package server

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
)

// fakeLister lets the diffing be tested without holders.
type fakeLister struct{ sessions []ptyruntime.SessionInfo }

func (f *fakeLister) List() []ptyruntime.SessionInfo { return f.sessions }

// The stream is a series of changes, so an unchanged session must produce
// nothing — otherwise a reader is woken several times a second for no reason,
// which is the polling this replaces.
func TestCollectSessionEventsOnlyReportsChanges(t *testing.T) {
	l := &fakeLister{sessions: []ptyruntime.SessionInfo{
		{ID: "a", Status: ptyruntime.StatusRunning, Title: "◐ task", OutputBytes: 10},
	}}
	seen := map[string]SessionEvent{}

	first := collectSessionEvents(l, seen)
	if len(first) != 1 || first[0].ID != "a" || first[0].Type != "session_activity" {
		t.Fatalf("first pass should report the session: %+v", first)
	}
	if again := collectSessionEvents(l, seen); len(again) != 0 {
		t.Fatalf("an unchanged session must not be reported again: %+v", again)
	}

	// A moving title is the signal worth pushing.
	l.sessions[0].Title = "◑ task"
	changed := collectSessionEvents(l, seen)
	if len(changed) != 1 || changed[0].Title != "◑ task" {
		t.Fatalf("a changed title should be reported: %+v", changed)
	}
}

func TestCollectSessionEventsCarriesActivityTimes(t *testing.T) {
	out := time.Now().Add(-2 * time.Second)
	title := time.Now().Add(-time.Second)
	l := &fakeLister{sessions: []ptyruntime.SessionInfo{
		{ID: "a", Status: ptyruntime.StatusRunning, LastOutput: out, TitleChanged: title},
	}}
	ev := collectSessionEvents(l, map[string]SessionEvent{})
	if len(ev) != 1 {
		t.Fatalf("expected one event, got %d", len(ev))
	}
	if ev[0].LastOutput == "" || ev[0].TitleChanged == "" {
		t.Fatalf("activity times missing: %+v", ev[0])
	}
	// RFC3339Nano, so a reader can compute a sub-second age.
	if !strings.Contains(ev[0].LastOutput, "T") {
		t.Errorf("last output is not a timestamp: %q", ev[0].LastOutput)
	}
	if _, err := time.Parse(time.RFC3339, ev[0].TitleChanged); err != nil {
		t.Errorf("title time not parseable: %v", err)
	}
}

// A session going away is news too: a reader holding stale state would
// otherwise keep showing it.
func TestCollectSessionEventsReportsSessionsGoingAway(t *testing.T) {
	l := &fakeLister{sessions: []ptyruntime.SessionInfo{{ID: "a", Status: ptyruntime.StatusRunning}}}
	seen := map[string]SessionEvent{}
	collectSessionEvents(l, seen)

	l.sessions = nil
	gone := collectSessionEvents(l, seen)
	if len(gone) != 1 || gone[0].Type != "session_gone" || gone[0].ID != "a" {
		t.Fatalf("expected a session_gone event, got %+v", gone)
	}
	// Reported once, not on every tick.
	if again := collectSessionEvents(l, seen); len(again) != 0 {
		t.Fatalf("session_gone must not repeat: %+v", again)
	}
}

func TestCollectRepoCreatedEventsEmitsOnce(t *testing.T) {
	sessions := []domain.Session{
		{ID: "new", Name: "reviewer", ParentID: "parent", Status: domain.SessionStatusActive},
	}
	announced := map[string]struct{}{}
	first := collectRepoCreatedEvents(sessions, announced)
	if len(first) != 1 || first[0].Type != "session_created" || first[0].ID != "new" {
		t.Fatalf("first pass: %+v", first)
	}
	if first[0].Session == nil || first[0].Session.Name != "reviewer" || first[0].Session.ParentID != "parent" {
		t.Fatalf("session payload: %+v", first[0].Session)
	}
	if again := collectRepoCreatedEvents(sessions, announced); len(again) != 0 {
		t.Fatalf("must not re-announce: %+v", again)
	}
}

// A session exiting changes its status, which must reach the reader.
func TestCollectSessionEventsReportsStatusChanges(t *testing.T) {
	l := &fakeLister{sessions: []ptyruntime.SessionInfo{{ID: "a", Status: ptyruntime.StatusRunning}}}
	seen := map[string]SessionEvent{}
	collectSessionEvents(l, seen)

	l.sessions[0].Status = ptyruntime.StatusExited
	ev := collectSessionEvents(l, seen)
	if len(ev) != 1 || ev[0].Status != string(ptyruntime.StatusExited) {
		t.Fatalf("expected the status change to be reported: %+v", ev)
	}
}

// End to end over a real socket and a real holder: creating a session and
// producing output must reach a connected reader without it asking.
func TestSessionEventsStreamReachesAReader(t *testing.T) {
	sock := startPTYServer(t)

	conn, err := EventsDial(sock)
	if err != nil {
		t.Fatalf("events dial: %v", err)
	}
	defer conn.Close()

	// A session that prints, so the holder records output and a title.
	create := createPTY(t, sock, map[string]any{
		"id":      "ev1",
		"command": `printf '\033]0;working on it\007hello from the session\n'; sleep 30`,
	})
	if create.Error != nil {
		t.Fatalf("create: %v", create.Error)
	}

	// Read until the session shows up with output recorded, or give up.
	deadline := time.After(20 * time.Second)
	got := make(chan SessionEvent, 32)
	errs := make(chan error, 1)
	go func() {
		for {
			ev, rerr := conn.Next()
			if rerr != nil {
				errs <- rerr
				return
			}
			got <- ev
		}
	}()

	var sawSession, sawTitle bool
	for !sawSession || !sawTitle {
		select {
		case ev := <-got:
			if ev.ID != "ev1" {
				continue
			}
			sawSession = true
			if ev.Title == "working on it" {
				sawTitle = true
			}
		case rerr := <-errs:
			t.Fatalf("stream ended: %v", rerr)
		case <-deadline:
			t.Fatalf("timed out; session seen=%v title seen=%v", sawSession, sawTitle)
		}
	}
}

// startEventsServer is a serve with a store and no PTY runtime.
func startEventsServer(t *testing.T, repo domain.SessionRepository) (*Server, string) {
	t.Helper()
	dir := shortTempDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(ln, repo, nil, nil, nil, nil, "test")
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() { defer close(served); _ = srv.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-served })
	return srv, SocketPath(dir)
}

// readEvents pumps the stream into a channel until it ends.
func readEvents(conn *EventsConn) <-chan SessionEvent {
	got := make(chan SessionEvent, 64)
	go func() {
		defer close(got)
		for {
			ev, err := conn.Next()
			if err != nil {
				return
			}
			got <- ev
		}
	}()
	return got
}

func waitForEvent(t *testing.T, got <-chan SessionEvent, match func(SessionEvent) bool) SessionEvent {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-got:
			if !ok {
				t.Fatal("stream ended")
			}
			if match(ev) {
				return ev
			}
		case <-deadline:
			t.Fatal("timed out waiting for event")
		}
	}
}

func TestSessionEventsWithoutPTYRuntime(t *testing.T) {
	repo := testRepo(t)
	_, sock := startEventsServer(t, repo)

	conn, err := EventsDial(sock)
	if err != nil {
		t.Fatalf("events dial: %v", err)
	}
	defer conn.Close()
	got := readEvents(conn)

	if err := repo.Save(context.Background(), &domain.Session{
		ID: "tm1", TerminalName: "tm1", Backend: domain.BackendPTY,
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	waitForEvent(t, got, func(ev SessionEvent) bool { return ev.Type == "session_created" && ev.ID == "tm1" })
}

func TestSessionEventsCarriesHubEvents(t *testing.T) {
	srv, sock := startEventsServer(t, testRepo(t))

	conn, err := EventsDial(sock)
	if err != nil {
		t.Fatalf("events dial: %v", err)
	}
	defer conn.Close()
	got := readEvents(conn)

	// The subscription starts after the attach reply, so keep publishing until
	// one lands rather than racing a single publish against it.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tick := time.NewTicker(20 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				srv.hub.Publish(SessionEvent{Type: "todo_changed", ID: "x", Rev: 3})
			}
		}
	}()
	ev := waitForEvent(t, got, func(ev SessionEvent) bool { return ev.Type == "todo_changed" })
	if ev.ID != "x" || ev.Rev != 3 {
		t.Fatalf("got %+v", ev)
	}
}
