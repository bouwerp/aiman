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

func startTestServer(t *testing.T, repo domain.SessionRepository) (string, context.CancelFunc) {
	t.Helper()
	dir := t.TempDir()
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv := New(ln, repo, nil, nil, "test")
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(cancel)
	return SocketPath(dir), cancel
}

func testRepo(t *testing.T) domain.SessionRepository {
	t.Helper()
	repo, err := sqlite.NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
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
	sock, _ := startTestServer(t, repo)

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

func TestSessionListBackfillsName(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "legacy", TmuxSession: "old-tmux", IssueKey: "WTB-9",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	sock, _ := startTestServer(t, repo)
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
	dir := t.TempDir()
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = New(ln, repo, remote, nil, "t").Serve(cctx) }()
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
	dir := t.TempDir()
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	creator := &fakeCreator{}
	go func() { _ = New(ln, repo, nil, creator, "t").Serve(ctx) }()
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
	_, err := Call(filepath.Join(t.TempDir(), "aiman.sock"), "ping", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}
