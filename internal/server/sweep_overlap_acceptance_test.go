package server

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
	"github.com/bouwerp/aiman/internal/ptyruntime"
)

// Serve load guard P0 (docs/specs/serve-load-guard.md, Behaviour 0): sweeps
// never stack, and the event stream stops reading the session table every
// 300 ms.

func sweepRow(id string) domain.Session {
	return domain.Session{ID: id, Name: id, Group: "g", TerminalName: id, Backend: domain.BackendPTY,
		Status: domain.SessionStatusActive, WorkingDirectory: "/w/" + id}
}

func TestSweepSkipsWhileThePreviousOneRuns(t *testing.T) {
	srv, pty, _ := ptyServer(t, sweepRow("sw-1"), sweepRow("sw-2"), sweepRow("sw-3"))
	for _, id := range []string{"sw-1", "sw-2", "sw-3"} {
		pty.Add(ptyruntime.SessionInfo{ID: id, Name: id, Dir: "/w/" + id, Status: ptyruntime.StatusRunning})
	}
	pty.SetCaptureDelay(300 * time.Millisecond)
	ctx := context.Background()

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.sweepStates(ctx)
	}()
	time.Sleep(50 * time.Millisecond) // the first sweep is inside its first capture

	start := time.Now()
	srv.sweepStates(ctx)
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("second sweep ran for %v while the first was still running, want it skipped at once", d)
	}
	if got := srv.SweepSkipped(); got != 1 {
		t.Fatalf("SweepSkipped = %d, want 1", got)
	}
	wg.Wait()
	if n := pty.Captures(); n != 3 {
		t.Fatalf("captures = %d, want one per session from the one sweep that ran", n)
	}
	// A sweep after the first has finished runs again.
	pty.SetCaptureDelay(0)
	srv.sweepStates(ctx)
	if n := pty.Captures(); n != 6 {
		t.Fatalf("captures after a later sweep = %d, want 6", n)
	}
	if got := srv.SweepSkipped(); got != 1 {
		t.Fatalf("SweepSkipped = %d after an uncontended sweep, want still 1", got)
	}
}

// countingRepo counts List calls on the repository behind serve. It embeds
// the concrete repository so serve still sees OnChange and ClearAgentEnded.
type countingRepo struct {
	*sqlite.Repository
	lists atomic.Int64
}

func (c *countingRepo) List(ctx context.Context) ([]domain.Session, error) {
	c.lists.Add(1)
	return c.Repository.List(ctx)
}

func TestEventStreamReadsTheTableAtMostOncePerSecond(t *testing.T) {
	repo := &countingRepo{Repository: testRepo(t).(*sqlite.Repository)}
	srv, sock := startEventsServer(t, repo)
	conn, err := EventsDial(sock)
	if err != nil {
		t.Fatalf("events dial: %v", err)
	}
	defer conn.Close()
	got := readEvents(conn)

	base := repo.lists.Load()
	time.Sleep(2500 * time.Millisecond)
	if n := repo.lists.Load() - base; n > 4 {
		t.Fatalf("List called %d times in 2.5s with nothing happening, want at most 4 (once a second); 300ms polling would be 8", n)
	}

	// A new row is still announced within the next poll.
	row := sweepRow("ev-new")
	if err := repo.Save(context.Background(), &row); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(1500 * time.Millisecond)
	for {
		select {
		case ev := <-got:
			if ev.Type == "session_created" && ev.ID == "ev-new" {
				goto announced
			}
		case <-deadline:
			t.Fatal("session_created for a new row did not arrive within 1.5s")
		}
	}
announced:
	// A hub event is forwarded at once and does not by itself require a table
	// read: once the session list cache (P1) lands, a new row reaches the stream
	// through invalidation, not through polling the table.
	srv.hub.Publish(SessionEvent{Type: "todo_changed", ID: "ev-new", Rev: 1})
	waitForEvent(t, got, func(ev SessionEvent) bool { return ev.Type == "todo_changed" })
}

func TestEventPollIntervalIsAtLeastASecond(t *testing.T) {
	if EventPollInterval < time.Second {
		t.Fatalf("EventPollInterval = %v, want at least 1s", EventPollInterval)
	}
}
