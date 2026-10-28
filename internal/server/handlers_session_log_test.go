package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
	"github.com/bouwerp/aiman/internal/pane"
)

func logRepo(t *testing.T) *sqlite.Repository {
	t.Helper()
	repo, err := sqlite.NewRepository(filepath.Join(t.TempDir(), "log.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

func TestSessionLogReturnsEventsAfterSequence(t *testing.T) {
	ctx := context.Background()
	repo := logRepo(t)
	if err := repo.Save(ctx, &domain.Session{
		ID: "s1", Name: "fix-login", Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []domain.SessionEvent{
		{SessionID: "s1", Actor: domain.EventActorUser, Kind: domain.EventSessionCreated, Payload: json.RawMessage(`{"issue":"A"}`)},
		{SessionID: "s1", Actor: domain.EventActorAgent, Kind: domain.EventHookReported, Payload: json.RawMessage(`{"state":"idle"}`)},
	} {
		if _, err := repo.AppendSessionEvent(ctx, ev); err != nil {
			t.Fatal(err)
		}
	}
	sock := startTestServer(t, repo)
	resp, err := Call(sock, "session.log", map[string]any{"session": "fix-login", "since_seq": 1})
	if err != nil {
		t.Fatal(err)
	}
	m := resultMap(t, resp)
	if m["type"] != "session_log" || m["session"] != "s1" {
		t.Fatalf("result = %#v", m)
	}
	events, ok := m["events"].([]any)
	if !ok || len(events) != 1 {
		t.Fatalf("events = %#v", m["events"])
	}
	first, _ := events[0].(map[string]any)
	if first["kind"] != "hook.reported" || first["seq"] != float64(2) {
		t.Fatalf("event = %#v", first)
	}
}

func TestSessionLogLimit(t *testing.T) {
	ctx := context.Background()
	repo := logRepo(t)
	if err := repo.Save(ctx, &domain.Session{
		ID: "s1", Name: "s1", Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := repo.AppendSessionEvent(ctx, domain.SessionEvent{
			SessionID: "s1", Actor: domain.EventActorUser, Kind: domain.EventTodoChanged,
		}); err != nil {
			t.Fatal(err)
		}
	}
	sock := startTestServer(t, repo)
	resp, err := Call(sock, "session.log", map[string]any{"session": "s1", "limit": 2})
	if err != nil {
		t.Fatal(err)
	}
	m := resultMap(t, resp)
	events, _ := m["events"].([]any)
	if len(events) != 2 {
		t.Fatalf("events = %#v", m["events"])
	}
}

func TestSessionLogUnknownSession(t *testing.T) {
	sock := startTestServer(t, logRepo(t))
	resp, err := Call(sock, "session.log", map[string]any{"session": "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != CodeNotFound {
		t.Fatalf("error = %+v", resp.Error)
	}
}

func TestSessionLogRequiresSession(t *testing.T) {
	sock := startTestServer(t, logRepo(t))
	resp, err := Call(sock, "session.log", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("error = %+v", resp.Error)
	}
}

type listerlessRepo struct {
	domain.SessionRepository
}

func TestSessionLogWithoutLedger(t *testing.T) {
	repo := logRepo(t)
	if err := repo.Save(context.Background(), &domain.Session{
		ID: "s1", Name: "s1", Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	sock := startTestServer(t, listerlessRepo{SessionRepository: repo})
	resp, err := Call(sock, "session.log", map[string]any{"session": "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("error = %+v", resp.Error)
	}
}

func findEvent(t *testing.T, repo *sqlite.Repository, kind domain.EventKind) *domain.SessionEvent {
	t.Helper()
	events, err := repo.ListSessionEvents(context.Background(), "s1", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for i := range events {
		if events[i].Kind == kind {
			return &events[i]
		}
	}
	t.Fatalf("no %s event; got %+v", kind, events)
	return nil
}

func TestHandoverWriteAppendsEvent(t *testing.T) {
	repo := logRepo(t)
	dir := t.TempDir()
	if err := repo.Save(context.Background(), &domain.Session{
		ID: "s1", Name: "s1", WorkingDirectory: dir, Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	sock := startTestServer(t, repo)
	resp, err := Call(sock, "handover.write", map[string]any{"session": "s1", "reason": "restart"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("write: %+v", resp.Error)
	}
	ev := findEvent(t, repo, domain.EventHandoverWritten)
	if ev.Actor != domain.EventActorAiman {
		t.Fatalf("actor = %q", ev.Actor)
	}
}

func TestReportAppendsHookEvent(t *testing.T) {
	repo := logRepo(t)
	if err := repo.Save(context.Background(), &domain.Session{
		ID: "s1", Name: "s1", Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	sock := startTestServer(t, repo)
	resp, err := Call(sock, "session.report_agent_session", map[string]any{
		"id": "s1", "event": "Stop", "state": "idle", "seq": 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("report: %+v", resp.Error)
	}
	ev := findEvent(t, repo, domain.EventHookReported)
	if ev.Actor != domain.EventActorAgent {
		t.Fatalf("actor = %q", ev.Actor)
	}
}

func TestRegisterAppendsSessionCreated(t *testing.T) {
	repo := logRepo(t)
	sock := startTestServer(t, repo)
	resp, err := Call(sock, "session.register", map[string]any{
		"sessions": []map[string]any{{"id": "s1", "name": "n", "group": "g"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("register: %+v", resp.Error)
	}
	ev := findEvent(t, repo, domain.EventSessionCreated)
	if ev.Actor != domain.EventActorAiman {
		t.Fatalf("actor = %q", ev.Actor)
	}
}

func TestForgetAppendsSessionEnded(t *testing.T) {
	repo := logRepo(t)
	dir := t.TempDir()
	if err := repo.Save(context.Background(), &domain.Session{
		ID: "s1", Name: "s1", WorkingDirectory: dir, Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	sock := startTestServer(t, repo)
	resp, err := Call(sock, "session.forget", map[string]any{"id": "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("forget: %+v", resp.Error)
	}
	ev := findEvent(t, repo, domain.EventSessionEnded)
	if ev.Actor != domain.EventActorUser {
		t.Fatalf("actor = %q", ev.Actor)
	}
}

func TestSessionReplayWritesBundle(t *testing.T) {
	repo := logRepo(t)
	if err := repo.Save(context.Background(), &domain.Session{
		ID: "s1", Name: "s1", WorkingDirectory: t.TempDir(), Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AppendSessionEvent(context.Background(), domain.SessionEvent{
		SessionID: "s1", Actor: domain.EventActorUser, Kind: domain.EventSessionCreated,
	}); err != nil {
		t.Fatal(err)
	}
	sock := startTestServer(t, repo)
	out := t.TempDir()
	resp, err := Call(sock, "session.replay", map[string]any{"session": "s1", "out": out})
	if err != nil {
		t.Fatal(err)
	}
	m := resultMap(t, resp)
	bundle, ok := m["bundle"].(map[string]any)
	if !ok || bundle["manifest"] == "" || bundle["events"] == "" {
		t.Fatalf("bundle = %#v", m["bundle"])
	}
}

func TestSessionReplayRequiresOut(t *testing.T) {
	sock := startTestServer(t, logRepo(t))
	resp, err := Call(sock, "session.replay", map[string]any{"session": "s1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("error = %+v", resp.Error)
	}
}

func TestUncertainPaneRecordedOncePerPane(t *testing.T) {
	ctx := context.Background()
	repo := logRepo(t)
	s := &Server{repo: repo}
	sess := domain.Session{ID: "s1", AgentName: "deepcode", Backend: domain.BackendPTY}
	low := pane.Result{State: domain.AgentStateUnknown, Confidence: pane.Low}

	s.recordUncertainPane(ctx, sess, "thinking...", low)
	s.recordUncertainPane(ctx, sess, "thinking...", low)
	s.recordUncertainPane(ctx, sess, "thinking...", pane.Result{State: domain.AgentStateIdle, Confidence: pane.High})
	s.recordUncertainPane(ctx, sess, "still thinking...", low)

	events, err := repo.ListSessionEvents(ctx, "s1", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (one per distinct low-confidence pane)", len(events))
	}
	var p map[string]string
	if err := json.Unmarshal(events[0].Payload, &p); err != nil {
		t.Fatal(err)
	}
	if events[0].Kind != domain.EventPaneUncertain || p["agent"] != "deepcode" || p["backend"] != "pty" || len(p["pane_hash"]) != 64 {
		t.Fatalf("event = %+v payload = %v", events[0], p)
	}
	if _, ok := p["pane"]; ok {
		t.Fatal("payload must not carry pane text")
	}
}
