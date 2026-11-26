//go:build feedaccept || feedaccept_p3c

package server

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

const (
	eventsSinceCaller  = "sess-events-since"
	eventsSincePeerPID = 6262
)

type eventsSinceRow struct {
	RowID     int64             `json:"rowid"`
	SessionID string            `json:"session_id"`
	Seq       int64             `json:"seq"`
	At        string            `json:"at"`
	Actor     domain.EventActor `json:"actor"`
	Kind      domain.EventKind  `json:"kind"`
	Payload   json.RawMessage   `json:"payload"`
}

type eventsSinceResult struct {
	Type   string           `json:"type"`
	Events []eventsSinceRow `json:"events"`
	Next   int64            `json:"next"`
}

func eventsSinceServer(t *testing.T) *Server {
	t.Helper()
	srv, _, _ := ptyServer(t, todoSession(eventsSinceCaller))
	srv.procInSession = func(pid int) bool { return pid == eventsSincePeerPID }
	return srv
}

func appendLedger(t *testing.T, srv *Server, session string, kind domain.EventKind) {
	t.Helper()
	app, ok := srv.repo.(sessionEventAppender)
	if !ok {
		t.Fatal("repository has no ledger")
	}
	if _, err := app.AppendSessionEvent(context.Background(), domain.SessionEvent{
		SessionID: session, Actor: domain.EventActorAiman, Kind: kind, Payload: json.RawMessage(`{"child":"c1"}`),
	}); err != nil {
		t.Fatal(err)
	}
}

func eventsSince(t *testing.T, srv *Server, params map[string]any) eventsSinceResult {
	t.Helper()
	var got eventsSinceResult
	rpcOK(t, srv, "", "session.events_since", params, &got)
	if got.Type != "session_events_since" {
		t.Fatalf("type = %q, want session_events_since", got.Type)
	}
	return got
}

func TestEventsSinceShapeAndKinds(t *testing.T) {
	srv := eventsSinceServer(t)
	appendLedger(t, srv, "s1", domain.EventSessionCreated)
	appendLedger(t, srv, "s2", domain.EventChildLanded)
	appendLedger(t, srv, "s1", domain.EventChildFinished)

	all := eventsSince(t, srv, map[string]any{"after_rowid": 0})
	if len(all.Events) != 3 {
		t.Fatalf("events = %+v, want 3", all.Events)
	}
	first := all.Events[0]
	if first.RowID == 0 || first.SessionID != "s1" || first.Seq != 1 || first.At == "" ||
		first.Actor != domain.EventActorAiman || first.Kind != domain.EventSessionCreated || string(first.Payload) != `{"child":"c1"}` {
		t.Fatalf("first event = %+v", first)
	}
	if all.Events[1].SessionID != "s2" || all.Events[1].RowID <= first.RowID || all.Events[2].RowID <= all.Events[1].RowID {
		t.Fatalf("events not in rowid order across sessions: %+v", all.Events)
	}
	if all.Next != all.Events[2].RowID {
		t.Fatalf("next = %d, want last rowid %d", all.Next, all.Events[2].RowID)
	}

	kinds := eventsSince(t, srv, map[string]any{
		"after_rowid": 0,
		"kinds":       []string{string(domain.EventChildLanded), string(domain.EventChildFinished)},
	})
	if len(kinds.Events) != 2 || kinds.Events[0].Kind != domain.EventChildLanded || kinds.Events[1].Kind != domain.EventChildFinished {
		t.Fatalf("kind filter = %+v", kinds.Events)
	}

	after := eventsSince(t, srv, map[string]any{"after_rowid": first.RowID, "limit": 1})
	if len(after.Events) != 1 || after.Events[0].RowID != all.Events[1].RowID || after.Next != all.Events[1].RowID {
		t.Fatalf("after %d limit 1 = %+v", first.RowID, after)
	}

	empty := eventsSince(t, srv, map[string]any{"after_rowid": all.Next})
	if len(empty.Events) != 0 || empty.Next != all.Next {
		t.Fatalf("past the end = %+v, want no events and next %d", empty, all.Next)
	}
}

func TestEventsSinceDefaultAndMaxLimit(t *testing.T) {
	srv := eventsSinceServer(t)
	for i := 0; i < 1001; i++ {
		appendLedger(t, srv, "s1", domain.EventHookReported)
	}
	if got := eventsSince(t, srv, map[string]any{"after_rowid": 0}); len(got.Events) != 200 {
		t.Fatalf("default limit returned %d, want 200", len(got.Events))
	}
	if got := eventsSince(t, srv, map[string]any{"after_rowid": 0, "limit": 5000}); len(got.Events) != 1000 {
		t.Fatalf("limit 5000 returned %d, want the max 1000", len(got.Events))
	}
}

func TestEventsSinceRefusesSessionCaller(t *testing.T) {
	srv := eventsSinceServer(t)
	for _, tc := range []struct {
		name   string
		caller string
		pid    int
	}{
		{name: "named caller", caller: eventsSinceCaller, pid: scopeNoPID},
		{name: "peer in session", pid: eventsSincePeerPID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := scopeCall(t, srv, tc.caller, tc.pid, "session.events_since", map[string]any{"after_rowid": 0})
			if resp.Error == nil {
				t.Fatalf("want needs_operator, got result %+v", resp.Result)
			}
			if resp.Error.Code != CodeNeedsOperator {
				t.Fatalf("code = %s (%q), want %s", resp.Error.Code, resp.Error.Message, CodeNeedsOperator)
			}
		})
	}
}
