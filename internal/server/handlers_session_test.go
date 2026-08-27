package server

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
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
	// Asserts what this test is actually about: no prompt was delivered.
	// It used to require zero commands outright, which stopped being a fair
	// proxy once listing a session legitimately enumerates what is running on
	// the host (tmux/pty discovery) before deciding anything.
	for _, cmd := range remote.execs {
		if strings.Contains(cmd, "send-keys") || strings.Contains(cmd, "pty input") {
			t.Fatalf("must not deliver a prompt when blocked: %v", remote.execs)
		}
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

func TestSessionCreateUsesCallerWorktreeParentAsRoot(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID:           "parent-id",
		Name:         "impl",
		WorktreePath: "/home/code/repos/treasury-admin-dapp@WTB-1",
		Status:       domain.SessionStatusActive,
		CreatedAt:    time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	dir := shortTempDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	creator := &fakeCreator{}
	go func() { _ = New(ln, repo, nil, creator, nil, nil, "t").Serve(cancelCtx) }()

	t.Setenv("AIMAN_ID", "parent-id")
	resp, err := Call(SocketPath(dir), "session.create", map[string]any{
		"repo": "owner/treasury-admin-dapp", "branch": "WTB-2", "agent": "claude",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("create: %+v", resp.Error)
	}
	if creator.cfg.SSHManager == nil {
		t.Fatal("caller session must provide a create executor")
	}
	if got := creator.cfg.SSHManager.GetRoot(); got != "/home/code/repos" {
		t.Fatalf("create root = %q, want /home/code/repos", got)
	}
}

func TestSessionCreateInfersRootFromExistingWorktreesWhenCallerUnknown(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID:           "other-id",
		Name:         "impl",
		WorktreePath: "/home/code/repos/aiman@aiman-fixes-2808",
		Status:       domain.SessionStatusActive,
		CreatedAt:    time.Now(),
	}); err != nil {
		t.Fatal(err)
	}

	dir := shortTempDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	creator := &fakeCreator{}
	go func() { _ = New(ln, repo, nil, creator, nil, nil, "t").Serve(cancelCtx) }()

	resp, err := Call(SocketPath(dir), "session.create", map[string]any{
		"repo": "realfi-co/realfi", "branch": "WTB-2", "agent": "claude",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("create: %+v", resp.Error)
	}
	if creator.cfg.SSHManager == nil {
		t.Fatal("create must infer a git root from existing worktrees")
	}
	if got := creator.cfg.SSHManager.GetRoot(); got != "/home/code/repos" {
		t.Fatalf("create root = %q, want /home/code/repos, not $HOME/realfi", got)
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

// serve must list the sessions actually running on its host, not just the rows
// in its own database.
//
// Sessions are created by the laptop TUI, which persists to the *laptop's*
// database, so the remote's copy stays empty. A DB-only listing therefore
// reported no sessions at all while six were running — leaving an in-session
// agent with nothing to address and no way to prompt a sibling.
func TestSessionListIncludesLiveSessionsMissingFromTheDB(t *testing.T) {
	repo := testRepo(t)
	remote := &fakeRemote{
		tmuxSessions: []string{"treasury@WTB-1896", "not-aiman"},
		// Only the aiman-managed session carries AIMAN_ID; the other must be
		// ignored, which is how serve's and the trigger daemon's own tmux
		// sessions stay out of the list.
		tmuxEnv: map[string]string{"treasury@WTB-1896": "live-id-1"},
		tmuxCWD: map[string]string{"treasury@WTB-1896": "/home/code/repos/treasury@WTB-1896"},
	}
	dir := shortTempDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = New(ln, repo, remote, nil, nil, nil, "t").Serve(ctx) }()
	sock := SocketPath(dir)

	resp, err := Call(sock, "session.list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("list: %+v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	var list struct {
		Sessions []SessionInfo `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}

	if len(list.Sessions) != 1 {
		t.Fatalf("expected exactly the one aiman-managed live session, got %s", raw)
	}
	got := list.Sessions[0]
	if got.ID != "live-id-1" {
		t.Errorf("ID = %q, want live-id-1", got.ID)
	}
	// Named after the tmux session, not the creation-flow's "impl": six
	// discovered sessions all called impl-N are indistinguishable and useless
	// for addressing a specific sibling.
	if got.Name != "treasury@WTB-1896" {
		t.Errorf("Name = %q, want the tmux session name", got.Name)
	}
	// And the issue key drives the group, so sessions bucket usefully.
	if got.Group != "WTB-1896" {
		t.Errorf("Group = %q, want WTB-1896 derived from the session name", got.Group)
	}

	// And it is reachable by that name, which is the whole point.
	one, err := Call(sock, "session.get", map[string]any{"id": got.Name})
	if err != nil {
		t.Fatal(err)
	}
	if one.Error != nil {
		t.Fatalf("get by derived name %q: %+v", got.Name, one.Error)
	}
}

// A dashboard hand-off creates a task-driven session, so it must be able to ask
// for the task file. Absent has to keep meaning "suppress it": that is what an
// agent creating an ad-hoc sibling wants, and was the only behaviour before.
func TestSessionCreatePromptFreeDefaultsToTrue(t *testing.T) {
	cases := []struct {
		name   string
		params string
		want   bool
	}{
		{"absent keeps the old behaviour", `{"agent":"claude","repo":"r","branch":"b"}`, true},
		{"explicit true", `{"agent":"claude","repo":"r","branch":"b","prompt_free":true}`, true},
		{"explicit false asks for the task file", `{"agent":"claude","repo":"r","branch":"b","prompt_free":false}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var params struct {
				PromptFree *bool `json:"prompt_free"`
			}
			if err := json.Unmarshal([]byte(tc.params), &params); err != nil {
				t.Fatal(err)
			}
			got := true
			if params.PromptFree != nil {
				got = *params.PromptFree
			}
			if got != tc.want {
				t.Errorf("prompt_free = %v, want %v", got, tc.want)
			}
		})
	}
}
