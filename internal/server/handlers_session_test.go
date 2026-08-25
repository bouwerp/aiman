package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
)

func startTestServer(t *testing.T, repo domain.SessionRepository) string {
	t.Helper()
	dir := shortTempDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv := New(ln, repo, nil, nil, nil, nil, "test")
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(cancel)
	return SocketPath(dir)
}

func testRepo(t *testing.T) domain.SessionRepository {
	t.Helper()
	repo, err := sqlite.NewRepository(filepath.Join(shortTempDir(t), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

func TestSessionListAndGet(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	now := time.Now()
	if err := repo.Save(ctx, &domain.Session{
		ID: "id-b", Name: "reviewer", Group: "WTB-1", TmuxSession: "rev",
		AgentName: "grok", Status: domain.SessionStatusActive, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, &domain.Session{
		ID: "id-a", Name: "impl", Group: "WTB-1", TmuxSession: "imp",
		AgentName: "claude", Status: domain.SessionStatusActive, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	sock := startTestServer(t, repo)

	resp, err := Call(sock, "session.list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("list error: %+v", resp.Error)
	}
	var list struct {
		Type     string        `json:"type"`
		Sessions []SessionInfo `json:"sessions"`
	}
	raw, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if list.Type != "session_list" || len(list.Sessions) != 2 {
		t.Fatalf("list = %s", raw)
	}
	if list.Sessions[0].Name != "impl" || list.Sessions[1].Name != "reviewer" {
		t.Fatalf("sort = %s %s", list.Sessions[0].Name, list.Sessions[1].Name)
	}

	filtered, err := Call(sock, "session.list", map[string]any{"group": "WTB-1"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(filtered.Result)
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 2 {
		t.Fatalf("group filter: %s", raw)
	}

	got, err := Call(sock, "session.get", map[string]any{"id": "reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Error != nil {
		t.Fatalf("get by name: %+v", got.Error)
	}
	raw, _ = json.Marshal(got.Result)
	var one struct {
		Type    string      `json:"type"`
		Session SessionInfo `json:"session"`
	}
	if err := json.Unmarshal(raw, &one); err != nil {
		t.Fatal(err)
	}
	if one.Session.ID != "id-b" || one.Session.Name != "reviewer" {
		t.Fatalf("get = %s", raw)
	}

	byPath, err := Call(sock, "session.get", map[string]any{"id": "WTB-1/impl"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(byPath.Result)
	if err := json.Unmarshal(raw, &one); err != nil {
		t.Fatal(err)
	}
	if one.Session.ID != "id-a" {
		t.Fatalf("group/name = %s", raw)
	}

	miss, err := Call(sock, "session.get", map[string]any{"id": "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if miss.Error == nil || miss.Error.Code != CodeNotFound {
		t.Fatalf("missing: %+v", miss.Error)
	}
}

func TestSessionListMarksSelf(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "id-a", Name: "impl", Group: "g", TmuxSession: "imp",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, &domain.Session{
		ID: "id-b", Name: "reviewer", Group: "g", TmuxSession: "rev",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	sock := startTestServer(t, repo)
	t.Setenv("AIMAN_ID", "id-a")
	resp, err := Call(sock, "session.list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(resp.Result)
	var list struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	var sawSelf, sawOther bool
	for _, s := range list.Sessions {
		switch s.ID {
		case "id-a":
			if !s.Self {
				t.Fatalf("caller session self=false: %s", raw)
			}
			sawSelf = true
		case "id-b":
			if s.Self {
				t.Fatalf("other session self=true: %s", raw)
			}
			sawOther = true
		}
	}
	if !sawSelf || !sawOther {
		t.Fatalf("sessions = %s", raw)
	}
}

func TestSessionListBackfillsName(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "legacy", TmuxSession: "old-tmux", IssueKey: "WTB-9",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	sock := startTestServer(t, repo)
	resp, err := Call(sock, "session.list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(resp.Result)
	var list struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].Name == "" || list.Sessions[0].Group == "" {
		t.Fatalf("backfill = %s", raw)
	}
}

func TestSessionPromptBlocked(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "id-b", Name: "reviewer", Group: "WTB-1", TmuxSession: "rev",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	remote := &fakeRemote{pane: "Allow execution of `rm -rf build/`? [y/N]"}
	dir := shortTempDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = New(ln, repo, remote, nil, nil, nil, "t").Serve(cctx) }()
	sock := SocketPath(dir)

	resp, err := Call(sock, "session.prompt", map[string]any{"id": "reviewer", "text": "yes"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != CodeAgentBlocked {
		t.Fatalf("want agent_blocked, got %+v", resp.Error)
	}
	if len(remote.execs) != 0 {
		t.Fatalf("must not send-keys when blocked: %v", remote.execs)
	}

	resp, err = Call(sock, "session.prompt", map[string]any{"id": "reviewer", "text": "yes", "force": true})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("force: %+v", resp.Error)
	}
}

func TestSessionCreateQuickAndRename(t *testing.T) {
	repo := testRepo(t)
	dir := shortTempDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	creator := &fakeCreator{}
	go func() { _ = New(ln, repo, nil, creator, nil, nil, "t").Serve(ctx) }()
	sock := SocketPath(dir)

	resp, err := Call(sock, "session.create", map[string]any{"quick": true, "agent": "claude"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("create: %+v", resp.Error)
	}
	if !creator.cfg.Quick || !creator.cfg.AdHoc || creator.cfg.Name != "q1" || creator.cfg.Group != "quick" {
		t.Fatalf("cfg = %+v", creator.cfg)
	}
	if creator.cfg.Branch == creator.cfg.Name {
		t.Fatalf("quick create copied display name %q onto Branch; they must stay independent", creator.cfg.Name)
	}

	resp, err = Call(sock, "session.rename", map[string]any{"id": "q1", "name": "spike"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("rename: %+v", resp.Error)
	}
	got, err := repo.Get(context.Background(), "new-id")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "spike" {
		t.Fatalf("saved name %q", got.Name)
	}

	resp, err = Call(sock, "session.create", map[string]any{"quick": true, "agent": "claude", "name": "spike"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != CodeNameTaken {
		t.Fatalf("want name_taken, got %+v", resp.Error)
	}
}

func TestCallServerNotRunning(t *testing.T) {
	_, err := Call(filepath.Join(shortTempDir(t), "aiman.sock"), "ping", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestReportAgentSessionUpdatesExisting(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "id-a", Name: "impl", Group: "g", TmuxSession: "imp",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	sock := startTestServer(t, repo)
	resp, err := Call(sock, "session.report_agent_session", map[string]any{
		"id": "id-a", "agent_session_id": "native-1", "agent_session_path": "/tmp/t.jsonl",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("%+v", resp.Error)
	}
	got, err := repo.Get(ctx, "id-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentSessionID != "native-1" || got.AgentSessionPath != "/tmp/t.jsonl" {
		t.Fatalf("%+v", got)
	}
	listed, err := Call(sock, "session.get", map[string]any{"id": "impl"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(listed.Result)
	var one struct {
		Session SessionInfo `json:"session"`
	}
	if err := json.Unmarshal(raw, &one); err != nil {
		t.Fatal(err)
	}
	if one.Session.AgentSessionID != "native-1" {
		t.Fatalf("get json: %s", raw)
	}
}

func TestReportAgentSessionUnknownIDStillSucceeds(t *testing.T) {
	sock := startTestServer(t, testRepo(t))
	resp, err := Call(sock, "session.report_agent_session", map[string]any{
		"id": "ghost", "agent_session_id": "n1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("missing session must not fail the hook: %+v", resp.Error)
	}
}

func TestReportAgentSessionUsesCaller(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "id-a", Name: "impl", Group: "g",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	sock := startTestServer(t, repo)
	t.Setenv("AIMAN_ID", "id-a")
	resp, err := Call(sock, "session.report_agent_session", map[string]any{
		"agent_session_id": "from-caller",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("%+v", resp.Error)
	}
	got, err := repo.Get(ctx, "id-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentSessionID != "from-caller" {
		t.Fatalf("%q", got.AgentSessionID)
	}
}

func TestReportAgentStateAndWait(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "id-a", Name: "impl", Group: "g", TmuxSession: "imp",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	sock := startTestServer(t, repo)
	resp, err := Call(sock, "session.report_agent_session", map[string]any{
		"id": "id-a", "state": "blocked", "source": "lifecycle",
		"message": "git push", "title": "fix auth",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("%+v", resp.Error)
	}
	got, err := Call(sock, "session.get", map[string]any{"id": "impl"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got.Result)
	var one struct {
		Session SessionInfo `json:"session"`
	}
	if err := json.Unmarshal(raw, &one); err != nil {
		t.Fatal(err)
	}
	if one.Session.State != "waiting_input" || one.Session.StateMessage != "git push" {
		t.Fatalf("blocked: %s", raw)
	}
	if one.Session.Title != "fix auth" || one.Session.StateConfidence != "high" {
		t.Fatalf("title/conf: %s", raw)
	}

	if _, err := Call(sock, "session.report_agent_session", map[string]any{
		"id": "id-a", "state": "idle", "source": "idle_prompt",
	}); err != nil {
		t.Fatal(err)
	}
	waited, err := Call(sock, "session.wait", map[string]any{"id": "impl", "until": "idle", "timeout_ms": 2000})
	if err != nil {
		t.Fatal(err)
	}
	if waited.Error != nil {
		t.Fatalf("wait: %+v", waited.Error)
	}
}

func TestReportSessionEnd(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "id-a", Name: "impl", Group: "g",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	sock := startTestServer(t, repo)
	if _, err := Call(sock, "session.report_agent_session", map[string]any{
		"id": "id-a", "ended": true,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, "id-a")
	if err != nil {
		t.Fatal(err)
	}
	if !got.AgentEnded {
		t.Fatal("ended not stored")
	}
}
