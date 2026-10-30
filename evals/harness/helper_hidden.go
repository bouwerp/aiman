package harness

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
)

// helperReportGap holds a child's row back after its first hook report for
// more than two of serve's event polls, so a stream poll always lands in the
// gap, the way a real agent reports seconds before the creator saves the row.
const helperReportGap = 2*server.EventPollInterval + 200*time.Millisecond

// onChildTerminal runs the hook set by SetChildTerminalHook, if any.
func (h *Harness) onChildTerminal(id string) {
	if fn := h.childTerminalHook.Load(); fn != nil {
		(*fn)(id)
	}
}

// SetChildTerminalHook runs fn for each child between its terminal starting
// and its row being saved.
func (h *Harness) SetChildTerminalHook(fn func(id string)) {
	h.childTerminalHook.Store(&fn)
}

// EventStream is an open session.events connection collecting what serve
// sends.
type EventStream struct {
	conn   net.Conn
	mu     sync.Mutex
	events []server.SessionEvent
	done   chan struct{}
}

// OpenEventStream attaches to session.events the way the dashboard does.
func (h *Harness) OpenEventStream() (*EventStream, error) {
	conn, err := net.DialTimeout("unix", h.Socket, 2*time.Second)
	if err != nil {
		return nil, err
	}
	req, err := json.Marshal(server.Request{ID: "events", Method: "session.events", Params: json.RawMessage(`{}`)})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if _, err := conn.Write(append(req, '\n')); err != nil {
		_ = conn.Close()
		return nil, err
	}
	r := bufio.NewReader(conn)
	if _, err := r.ReadBytes('\n'); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("session.events attach: %w", err)
	}
	st := &EventStream{conn: conn, done: make(chan struct{})}
	go st.read(json.NewDecoder(r))
	return st, nil
}

func (st *EventStream) read(dec *json.Decoder) {
	defer close(st.done)
	for {
		var ev server.SessionEvent
		if err := dec.Decode(&ev); err != nil {
			return
		}
		st.mu.Lock()
		st.events = append(st.events, ev)
		st.mu.Unlock()
	}
}

// FirstCreated returns the first session_created event for a session other
// than skip, waiting up to timeout.
func (st *EventStream) FirstCreated(skip string, timeout time.Duration) (server.SessionEvent, bool) {
	var got server.SessionEvent
	ok := waitUntil(timeout, func() bool {
		st.mu.Lock()
		defer st.mu.Unlock()
		for _, ev := range st.events {
			if ev.Type == "session_created" && ev.ID != skip && ev.Session != nil {
				got = ev
				return true
			}
		}
		return false
	})
	return got, ok
}

// Close ends the stream.
func (st *EventStream) Close() {
	_ = st.conn.Close()
	<-st.done
}

// CaseHandoverChildEarlyReport starts a handover child whose agent reports
// before its row is saved. The first session_created the dashboard hears for
// it must already carry the handover role, or the dashboard lists it.
func CaseHandoverChildEarlyReport() Case {
	return Case{Name: "handover-child-early-report", Setup: func(h *Harness) error {
		workingSession(h)
		h.SetChildTerminalHook(func(id string) {
			_ = h.Report([]byte(`{"id":"` + id + `","event":"SessionStart","state":"idle","source":"lifecycle"}`))
			time.Sleep(helperReportGap)
		})
		return h.EnableHandovers(30 * time.Second)
	}, Assert: func(h *Harness) []MetricResult {
		st, err := h.OpenEventStream()
		if err != nil {
			return []MetricResult{score("events_attached", false, err.Error())}
		}
		defer st.Close()
		if _, err := h.Call("handover.write", map[string]any{"session": "s1", "reason": "pre_compact"}); err != nil {
			return []MetricResult{score("handover_write", false, err.Error())}
		}
		ev, ok := st.FirstCreated("s1", 5*time.Second)
		if !ok {
			return []MetricResult{score("child_announced", false, "no session_created for the child")}
		}
		return []MetricResult{
			score("child_announced", true, ""),
			score("first_announce_hidden", ev.Session.Role == string(domain.SessionRoleHandover),
				fmt.Sprintf("first session_created role %q name %q", ev.Session.Role, ev.Session.Name)),
		}
	}}
}
