package server

import (
	"context"
	"encoding/json"
	"net"
	"time"

	"github.com/bouwerp/aiman/internal/ptyruntime"
)

// eventPollInterval is how often serve looks for activity changes.
//
// This is a local stat of a handful of small files, so it can be far tighter
// than anything the dashboard could do over SSH — which is the point: the
// dashboard used to make an SSH round trip per session per poll to learn the
// same thing.
const eventPollInterval = 300 * time.Millisecond

// eventKeepalive bounds how long the stream can stay silent. A reader blocked on
// a dead connection learns nothing from silence; a periodic empty event lets it
// notice and reconnect.
const eventKeepalive = 20 * time.Second

// SessionEvent is one line of the event stream. Absent fields mean unchanged or
// unknown; a reader should treat the stream as a series of updates, not
// snapshots.
type SessionEvent struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
	// Title is the terminal title the agent last set, which is where agents
	// advertise what they are doing.
	Title string `json:"title,omitempty"`
	// LastOutput and TitleChanged are RFC3339Nano, as published by the holder.
	LastOutput   string `json:"last_output,omitempty"`
	TitleChanged string `json:"title_changed_at,omitempty"`
	OutputBytes  int64  `json:"output_bytes,omitempty"`
	// Status is the session's lifecycle state, so a reader hears about a session
	// exiting as well as one working.
	Status string `json:"status,omitempty"`
}

// isSessionEvents reports whether a request opens the event stream, which like
// pty.attach takes over the connection rather than returning a single response.
func isSessionEvents(line []byte) bool {
	var probe struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return false
	}
	return probe.Method == "session.events"
}

// handleSessionEventsConn streams activity changes until the client goes away.
//
// The dashboard previously learned what a session was doing by polling it over
// SSH twice a second, per session. Everything it was asking for is known here
// the moment it happens, so this inverts the flow: serve watches the holders'
// activity files locally and pushes what changed.
func (s *Server) handleSessionEventsConn(ctx context.Context, conn net.Conn, line []byte) {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		writeResponse(conn, errResp("", CodeInvalidParams, "invalid request"))
		return
	}
	if s.pty == nil {
		writeResponse(conn, errResp(req.ID, CodeInvalidParams, "no PTY runtime on this host"))
		return
	}
	writeResponse(conn, Response{ID: req.ID, Result: map[string]any{"type": "events_attached"}})

	// A closed connection is only detectable on write, so the read side is
	// drained in the background purely to notice EOF.
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		buf := make([]byte, 256)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}()

	enc := json.NewEncoder(conn)
	seen := map[string]SessionEvent{}
	ticker := time.NewTicker(eventPollInterval)
	defer ticker.Stop()
	lastSent := time.Now()

	for {
		select {
		case <-ctx.Done():
			return
		case <-closed:
			return
		case <-ticker.C:
		}

		changed := collectSessionEvents(s.pty, seen)
		for _, ev := range changed {
			if err := enc.Encode(ev); err != nil {
				return
			}
			lastSent = time.Now()
		}
		if len(changed) == 0 && time.Since(lastSent) >= eventKeepalive {
			if err := enc.Encode(SessionEvent{Type: "keepalive"}); err != nil {
				return
			}
			lastSent = time.Now()
		}
	}
}

// ptyLister is the slice of the runtime the event stream needs.
type ptyLister interface {
	List() []ptyruntime.SessionInfo
}

// collectSessionEvents returns an event per session whose activity moved, and
// updates seen in place. Sessions that have gone are reported once.
func collectSessionEvents(pty ptyLister, seen map[string]SessionEvent) []SessionEvent {
	var out []SessionEvent
	live := map[string]struct{}{}

	for _, info := range pty.List() {
		live[info.ID] = struct{}{}
		ev := SessionEvent{
			Type:        "session_activity",
			ID:          info.ID,
			Title:       info.Title,
			OutputBytes: info.OutputBytes,
			Status:      string(info.Status),
		}
		if !info.LastOutput.IsZero() {
			ev.LastOutput = info.LastOutput.UTC().Format(time.RFC3339Nano)
		}
		if !info.TitleChanged.IsZero() {
			ev.TitleChanged = info.TitleChanged.UTC().Format(time.RFC3339Nano)
		}
		if prev, ok := seen[info.ID]; ok && prev == ev {
			continue
		}
		seen[info.ID] = ev
		out = append(out, ev)
	}

	for id := range seen {
		if _, still := live[id]; still {
			continue
		}
		delete(seen, id)
		out = append(out, SessionEvent{Type: "session_gone", ID: id})
	}
	return out
}
