package server

import (
	"context"
	"encoding/json"
	"net"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
)

// EventPollInterval is how often each stream looks for activity changes and
// new rows to announce. Hub events are forwarded as they arrive, not on this
// tick. Every subscriber reads the session table on each tick, which is why
// this stays at a second.
const EventPollInterval = 1 * time.Second

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
	// Session is filled on session_created so the dashboard can upsert without
	// a second round trip.
	Session *SessionInfo `json:"session,omitempty"`
	// State is the agent state the stateSweep last sampled on session_activity,
	// absent until it has sampled the session, or the review status on verify.
	State string `json:"state,omitempty"`
	// StateConfidence is high or low, beside State on session_activity.
	StateConfidence string `json:"state_confidence,omitempty"`
	// Rev is the todo list revision on todo_changed.
	Rev int64 `json:"rev,omitempty"`
	// Queued is how many steer messages are still waiting on steer.
	Queued *int `json:"queued,omitempty"`
	// DeliveredAt is RFC3339Nano, set on steer once a message reached the agent.
	DeliveredAt string `json:"delivered_at,omitempty"`
	// Kind classifies a notify event.
	Kind string `json:"kind,omitempty"`
	// Subagent names the sub-agent whose transcript moved on transcript.
	Subagent string `json:"subagent,omitempty"`
	// Text is a notify event's formatted text, which never holds terminal text.
	Text string `json:"text,omitempty"`
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
// Serve watches the holders' activity files locally and pushes what changed,
// so clients need no per-session poll over SSH.
func (s *Server) handleSessionEventsConn(ctx context.Context, conn net.Conn, line []byte) {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		writeResponse(conn, errResp("", CodeInvalidParams, "invalid request"))
		return
	}
	// The stream holds an admission slot only while it is set up.
	_, admitted, refused := s.admit(ctx, req)
	if refused != nil {
		writeResponse(conn, *refused)
		return
	}
	defer admitted()
	// Subscribe before replying so nothing published after the attach is lost.
	hubCh, unsubscribe := s.hub.Subscribe()
	defer unsubscribe()
	writeResponse(conn, Response{ID: req.ID, Result: map[string]any{"type": "events_attached"}})
	admitted()

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

	st := &eventStream{
		enc:       json.NewEncoder(conn),
		seen:      map[string]SessionEvent{},
		announced: map[string]string{},
		lastSent:  time.Now(),
		trWatch:   newTranscriptWatch(),
	}
	ticker := time.NewTicker(EventPollInterval)
	defer ticker.Stop()

	for {
		var err error
		select {
		case <-ctx.Done():
			return
		case <-closed:
			return
		case ev := <-hubCh:
			err = st.send(ev)
		case <-ticker.C:
			err = s.pollEvents(ctx, st)
		}
		if err != nil {
			return
		}
	}
}

// eventStream is one subscriber's state: what it has been told so far.
type eventStream struct {
	enc       *json.Encoder
	seen      map[string]SessionEvent
	announced map[string]string
	lastSent  time.Time
	trWatch   *transcriptWatch
}

func (st *eventStream) send(ev SessionEvent) error {
	if err := st.enc.Encode(ev); err != nil {
		return err
	}
	st.lastSent = time.Now()
	return nil
}

// announceRows reads the stored rows through the session list cache, sends
// session_created for new or changed rows, and returns the rows it read (nil
// when the read failed).
func (s *Server) announceRows(ctx context.Context, st *eventStream) ([]domain.Session, error) {
	if s.repo == nil {
		return nil, nil
	}
	listed, err := s.storedSessions(ctx)
	if err != nil {
		return nil, nil
	}
	for _, ev := range collectRepoCreatedEvents(listed, st.announced) {
		if err := st.send(ev); err != nil {
			return nil, err
		}
	}
	return listed, nil
}

// pollEvents is one EventPollInterval tick: activity changes from the holders,
// new rows, transcript moves, and a keepalive after a long silence.
func (s *Server) pollEvents(ctx context.Context, st *eventStream) error {
	sentBefore := st.lastSent
	if s.pty != nil {
		for _, ev := range collectSessionEvents(s.pty, st.seen, s.cachedSessionState) {
			if err := st.send(ev); err != nil {
				return err
			}
		}
	}
	listed, err := s.announceRows(ctx, st)
	if err != nil {
		return err
	}
	// A transcript event is marked seen only once encoded, so a failed
	// write never swallows a change.
	if s.transcripts != nil {
		for _, p := range s.collectTranscriptEvents(listed, st.trWatch, time.Now()) {
			if err := st.send(p.ev); err != nil {
				return err
			}
			st.trWatch.commit(p, st.lastSent)
		}
	}
	if st.lastSent.Equal(sentBefore) && time.Since(st.lastSent) >= eventKeepalive {
		return st.send(SessionEvent{Type: "keepalive"})
	}
	return nil
}

// ptyLister is the slice of the runtime the event stream needs.
type ptyLister interface {
	List() []ptyruntime.SessionInfo
}

// collectSessionEvents returns an event per session whose activity moved, and
// updates seen in place. Sessions that have gone are reported once. State comes
// from states, the sweep's cache: this runs every EventPollInterval per
// subscriber, so it never captures a screen itself.
func collectSessionEvents(pty ptyLister, seen map[string]SessionEvent, states func(string) (cachedState, bool)) []SessionEvent {
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
		if st, ok := states(info.ID); ok {
			ev.State, ev.StateConfidence = st.State, st.Confidence
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

// collectRepoCreatedEvents announces each session the first time it appears in
// serve's store so a CLI create reaches event-stream clients without a scan.
// announced keeps an identity fingerprint per id, and a row whose role, parent
// or name changed is announced again: a helper first stored without its role
// must still reach the dashboard as hidden.
func collectRepoCreatedEvents(sessions []domain.Session, announced map[string]string) []SessionEvent {
	var out []SessionEvent
	for _, s := range sessions {
		if s.ID == "" {
			continue
		}
		fp := string(s.Role) + "\x00" + s.ParentID + "\x00" + s.Name
		if prev, ok := announced[s.ID]; ok && prev == fp {
			continue
		}
		announced[s.ID] = fp
		info := sessionInfo(s, "")
		out = append(out, SessionEvent{
			Type:    "session_created",
			ID:      s.ID,
			Status:  string(s.Status),
			Session: &info,
		})
	}
	return out
}
