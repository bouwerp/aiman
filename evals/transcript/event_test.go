package transcripteval

import (
	"os"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/server"
)

// arrival is one transcript event: when it arrived and its rev.
type arrival struct {
	at  time.Time
	rev int64
}

// transcriptEvents streams every transcript event for session with the time
// it arrived, until the connection closes.
func transcriptEvents(t *testing.T, socket, session string) (<-chan arrival, func()) {
	t.Helper()
	conn, err := server.EventsDial(socket)
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan arrival, 64)
	go func() {
		defer close(out)
		for {
			ev, err := conn.Next()
			if err != nil {
				return
			}
			if ev.Type == "transcript" && ev.ID == session && ev.Subagent == "" {
				out <- arrival{at: time.Now(), rev: ev.Rev}
			}
		}
	}()
	return out, func() { _ = conn.Close() }
}

// drain discards events arriving within d.
func drain(ch <-chan arrival, d time.Duration) {
	deadline := time.After(d)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-deadline:
			return
		}
	}
}

// TestTranscriptEvent: once the phone has read a session's transcript,
// appending to the real file sends transcript {id, rev} on session.events
// within 3 s, and a burst of appends sends at most one a second.
func TestTranscriptEvent(t *testing.T) {
	w := newWorld(t)
	lines := fixtureLines(t)
	w.page(map[string]any{"session": sessPath})

	events, stop := transcriptEvents(t, w.socket, sessPath)
	defer stop()
	drain(events, 1500*time.Millisecond)

	appendFile(t, w.pathFile, lines[fixtureHead])
	select {
	case ev, ok := <-events:
		if !ok {
			t.Fatal("session.events closed")
		}
		if ev.rev == 0 {
			t.Error("transcript event carries no rev")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no transcript event within 3s of an append")
	}
	drain(events, 1200*time.Millisecond)

	// One line every 100ms for about 1.5s.
	burst := lines[fixtureHead+1 : fixtureNextCallLine]
	go func() {
		for _, l := range burst {
			if err := appendLine(w.pathFile, l); err != nil {
				t.Errorf("append: %v", err)
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	var got []time.Time
	deadline := time.After(4 * time.Second)
collect:
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("session.events closed")
			}
			got = append(got, ev.at)
		case <-deadline:
			break collect
		}
	}
	if len(got) == 0 {
		t.Fatalf("no transcript event during a burst of %d appends", len(burst))
	}
	for i := 1; i < len(got); i++ {
		// 100ms slack for the poll interval and scheduling.
		if gap := got[i].Sub(got[i-1]); gap < server.TranscriptEventDebounce-100*time.Millisecond {
			t.Errorf("events %d and %d arrived %v apart, want at least %v", i-1, i, gap, server.TranscriptEventDebounce)
		}
	}
}

// appendLine is appendFile for a goroutine, which may not call t.Fatal.
func appendLine(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
