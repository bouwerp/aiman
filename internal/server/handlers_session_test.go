package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/ptyruntime/ptyfake"
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
	remote := &fakeRemote{}
	pty := ptyfake.New()
	pty.Add(ptyruntime.SessionInfo{ID: "id-b", Name: "rev"})
	pty.SetScreen("id-b", "Allow execution of `rm -rf build/`? [y/N]")
	dir := shortTempDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = New(ln, repo, remote, nil, nil, pty, "t").Serve(cctx) }()
	sock := SocketPath(dir)

	resp, err := Call(sock, "session.prompt", map[string]any{"id": "reviewer", "text": "yes"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != CodeAgentBlocked {
		t.Fatalf("want agent_blocked, got %+v", resp.Error)
	}
	// No prompt was delivered, through the runtime or the CLI.
	if len(pty.Writes()) != 0 {
		t.Fatalf("must not deliver a prompt when blocked: %v", pty.Writes())
	}
	for _, cmd := range remote.execs {
		if strings.Contains(cmd, "pty input") {
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

func TestSessionCreateNestsUnderCaller(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "parent-id", Name: "impl", Status: domain.SessionStatusActive, CreatedAt: time.Now(),
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
		"quick": true, "agent": "claude", "name": "reviewer",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("create: %+v", resp.Error)
	}
	if creator.cfg.ParentID != "parent-id" {
		t.Fatalf("cfg.ParentID=%q", creator.cfg.ParentID)
	}
	got, err := repo.Get(ctx, "new-id")
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentID != "parent-id" {
		t.Fatalf("saved parent_id=%q", got.ParentID)
	}
}

func TestSessionCreateOrphanIsRoot(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "parent-id", Name: "impl", Status: domain.SessionStatusActive, CreatedAt: time.Now(),
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
		"quick": true, "agent": "claude", "name": "sibling", "orphan": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("create: %+v", resp.Error)
	}
	if creator.cfg.ParentID != "" {
		t.Fatalf("orphan cfg.ParentID=%q", creator.cfg.ParentID)
	}
	got, err := repo.Get(ctx, "new-id")
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentID != "" {
		t.Fatalf("orphan saved parent_id=%q", got.ParentID)
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
	pty := ptyfake.New()
	pty.Add(ptyruntime.SessionInfo{ID: "live-id-1", Name: "treasury@WTB-1896", Dir: "/home/code/repos/treasury@WTB-1896"})
	// An exited PTY is not a live session.
	pty.Add(ptyruntime.SessionInfo{ID: "done-1", Name: "done", Status: ptyruntime.StatusExited})
	dir := shortTempDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = New(ln, repo, nil, nil, nil, pty, "t").Serve(ctx) }()
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
	// Named after the terminal, not the creation-flow's "impl": six
	// discovered sessions all called impl-N are indistinguishable and useless
	// for addressing a specific sibling.
	if got.Name != "treasury@WTB-1896" {
		t.Errorf("Name = %q, want the terminal name", got.Name)
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

func sessionByName(t *testing.T, sock, name string) SessionInfo {
	t.Helper()
	resp, err := Call(sock, "session.get", map[string]any{"id": name})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("get %q: %+v", name, resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	var one struct {
		Session SessionInfo `json:"session"`
	}
	if err := json.Unmarshal(raw, &one); err != nil {
		t.Fatal(err)
	}
	return one.Session
}

func TestSessionRegisterPersistsLaptopSession(t *testing.T) {
	repo := testRepo(t)
	sock := startTestServer(t, repo)
	resp, err := Call(sock, "session.register", map[string]any{"sessions": []domain.Session{
		{ID: "lap-1", Name: "impl", Group: "g", Branch: "feat-x", BaseBranch: "main",
			WorktreePath: "/w/x", Status: domain.SessionStatusActive, CreatedAt: time.Now()},
		{ID: "pending-abc", Name: "skip"},
		{ID: ""},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("%+v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	if !strings.Contains(string(raw), "sessions_registered") || !strings.Contains(string(raw), "lap-1") || strings.Contains(string(raw), "pending-abc") {
		t.Fatalf("result: %s", raw)
	}
	got := sessionByName(t, sock, "impl")
	if got.Branch != "feat-x" || got.WorktreePath != "/w/x" || got.BaseBranch != "main" {
		t.Fatalf("%+v", got)
	}
	if _, err := repo.Get(context.Background(), "lap-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(context.Background(), "pending-abc"); err == nil {
		t.Fatal("pending id was stored")
	}
}

func TestSessionRegisterKeepsServeRuntimeState(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "lap-1", Name: "impl", Group: "g", HookState: domain.AgentStateWorking,
		HookStateSource: "lifecycle", Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	sock := startTestServer(t, repo)
	if _, err := Call(sock, "session.register", map[string]any{"sessions": []domain.Session{{
		ID: "lap-1", Name: "impl", Group: "g", Branch: "b", HookState: domain.AgentStateIdle,
		LocalPath: "/laptop/only", MutagenSyncID: "sync", Status: domain.SessionStatusActive,
	}}}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, "lap-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.HookState != domain.AgentStateWorking {
		t.Fatalf("HookState = %q, want working", got.HookState)
	}
	if got.LocalPath != "" || got.MutagenSyncID != "" {
		t.Fatalf("laptop-only fields stored: %+v", got)
	}
	if got.Branch != "b" {
		t.Fatalf("identity not updated: %+v", got)
	}
}

func TestReportAgentSessionPersistsLiveUnregistered(t *testing.T) {
	repo := testRepo(t)
	pty := ptyfake.New()
	pty.Add(ptyruntime.SessionInfo{ID: "live-1", Name: "live-sess"})
	dir := shortTempDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = New(ln, repo, nil, nil, nil, pty, "t").Serve(ctx) }()
	resp, err := Call(SocketPath(dir), "session.report_agent_session", map[string]any{
		"id": "live-1", "state": "idle", "source": "lifecycle",
	})
	if err != nil || resp.Error != nil {
		t.Fatalf("%v %+v", err, resp)
	}
	got, err := repo.Get(context.Background(), "live-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.HookState != domain.AgentStateIdle {
		t.Fatalf("HookState = %q", got.HookState)
	}
}

func TestReportAfterEndClearsLatch(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "id-a", Name: "impl", Group: "g",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	sock := startTestServer(t, repo)
	if _, err := Call(sock, "session.report_agent_session", map[string]any{"id": "id-a", "ended": true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.Get(ctx, "id-a"); !got.AgentEnded {
		t.Fatal("ended not stored")
	}
	if _, err := Call(sock, "session.report_agent_session", map[string]any{
		"id": "id-a", "state": "working", "source": "lifecycle",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, "id-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentEnded {
		t.Fatal("AgentEnded still latched after a live report")
	}
}

func TestSessionInfoCarriesRole(t *testing.T) {
	if got := sessionInfo(domain.Session{Role: domain.SessionRoleReviewer}, "").Role; got != "reviewer" {
		t.Fatalf("role=%q", got)
	}
	if got := sessionInfo(domain.Session{}, "").Role; got != "" {
		t.Fatalf("worker role=%q", got)
	}
}

// pickServer is a serve instance with a fake creator and an agents.preferred
// list, where only the installed commands resolve on the host.
func pickServer(t *testing.T, preferred []config.PreferredAgent, installed ...string) (*Server, domain.SessionRepository, *fakeCreator) {
	t.Helper()
	repo := testRepo(t)
	creator := &fakeCreator{}
	srv := New(nil, repo, nil, creator, nil, nil, "test")
	have := map[string]bool{}
	for _, c := range installed {
		have[c] = true
	}
	srv.SetAgentChoice(preferred, func(_ context.Context, command string) bool { return have[command] })
	return srv, repo, creator
}

func TestCreateUsesPreferredWhenAgentEmpty(t *testing.T) {
	srv, repo, creator := pickServer(t, config.PreferredFromCommands("codex", "claude"), "claude")
	resp := rpc(t, srv, "", "session.create", map[string]any{"quick": true})
	if resp.Error != nil {
		t.Fatalf("create: %+v", resp.Error)
	}
	if creator.cfg.Agent == nil || creator.cfg.Agent.Command != "claude" || creator.cfg.Agent.Name != "claude" {
		t.Fatalf("agent = %+v, want claude", creator.cfg.Agent)
	}
	got, err := repo.Get(context.Background(), "new-id")
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentName != "claude" {
		t.Fatalf("saved agent = %q, want claude", got.AgentName)
	}
}

// A picked entry hands its model and effort to the launch; an explicit agent
// carries none.
func TestCreateCarriesPreferredModelAndEffort(t *testing.T) {
	preferred := []config.PreferredAgent{
		{Agent: "codex", Model: "gpt-5"},
		{Agent: "pi", Model: "openrouter/qwen/qwen3-coder", Effort: "high"},
		{Agent: "pi", Model: "deepseek/deepseek-flash"},
	}
	srv, _, creator := pickServer(t, preferred, "pi")
	resp := rpc(t, srv, "", "session.create", map[string]any{"quick": true})
	if resp.Error != nil {
		t.Fatalf("create: %+v", resp.Error)
	}
	a := creator.cfg.Agent
	if a == nil || a.Command != "pi" || a.LaunchModel != "openrouter/qwen/qwen3-coder" || a.LaunchEffort != "high" {
		t.Fatalf("agent = %+v", a)
	}
	resp = rpc(t, srv, "", "session.create", map[string]any{"quick": true, "agent": "pi"})
	if resp.Error != nil {
		t.Fatalf("create: %+v", resp.Error)
	}
	if a := creator.cfg.Agent; a.LaunchModel != "" || a.LaunchEffort != "" {
		t.Fatalf("explicit agent took a preferred model: %+v", a)
	}
}

func TestExplicitAgentBeatsPreferred(t *testing.T) {
	srv, _, creator := pickServer(t, config.PreferredFromCommands("claude"), "claude", "grok")
	resp := rpc(t, srv, "", "session.create", map[string]any{"quick": true, "agent": "grok"})
	if resp.Error != nil {
		t.Fatalf("create: %+v", resp.Error)
	}
	if creator.cfg.Agent == nil || creator.cfg.Agent.Command != "grok" {
		t.Fatalf("agent = %+v, want grok", creator.cfg.Agent)
	}
}

func TestCreateReportsNoPreferredAgent(t *testing.T) {
	srv, repo, creator := pickServer(t, config.PreferredFromCommands("codex", "claude"))
	resp := rpc(t, srv, "", "session.create", map[string]any{"quick": true})
	if resp.Error == nil || resp.Error.Code != CodeNoPreferredAgent {
		t.Fatalf("error = %+v, want %s", resp.Error, CodeNoPreferredAgent)
	}
	if !strings.Contains(resp.Error.Message, "codex") || !strings.Contains(resp.Error.Message, "claude") {
		t.Fatalf("message %q must name the tried agents", resp.Error.Message)
	}
	if creator.cfg.Agent != nil {
		t.Fatalf("create ran with %+v", creator.cfg.Agent)
	}
	list, err := repo.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("saved %d sessions, want none", len(list))
	}
}

// forServer is a todo-enabled serve with a fake creator, preferred agent
// claude installed, and parent "p" on feat/proj-1 holding items t1..t3.
func forServer(t *testing.T, t3Ref string) (*Server, *fakeCreator) {
	t.Helper()
	srv, _, _ := todoServer(t, domain.Session{
		ID: "p", Name: "parent", Branch: "feat/proj-1", BaseBranch: "main",
		RepoName: "owner/r", Backend: domain.BackendPTY, TmuxSession: "p",
	})
	creator := &fakeCreator{}
	srv.create = creator
	srv.SetAgentChoice(config.PreferredFromCommands("claude"), func(context.Context, string) bool { return true })
	// Seeded as the agent, which never steers, so a later note is the create's.
	err := srv.TodoAccess().Mutate(context.Background(), "p", domain.AuthorAgent, func(l *domain.TodoList) error {
		l.Items = append(l.Items,
			domain.TodoItem{ID: "t1", Text: "Plan", State: domain.TodoDone},
			domain.TodoItem{ID: "t2", Text: "Spec", State: domain.TodoPending},
			domain.TodoItem{ID: "t3", Text: "Add the parser", State: domain.TodoPending, Ref: t3Ref},
		)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, creator
}

// parentT3 returns the parent p's item t3.
func parentT3(t *testing.T, srv *Server) domain.TodoItem {
	t.Helper()
	l, err := srv.TodoAccess().Get("p")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range l.Items {
		if it.ID == "t3" {
			return it
		}
	}
	t.Fatal("item p/t3 not found")
	return domain.TodoItem{}
}

func TestCreateForLinksParent(t *testing.T) {
	srv, creator := forServer(t, "")
	resp := rpc(t, srv, "", "session.create", map[string]any{"for": "t3", "parent": "p"})
	if resp.Error != nil {
		t.Fatalf("create: %+v", resp.Error)
	}
	cfg := creator.cfg
	if cfg.Branch != "feat/proj-1--t3" || cfg.BaseBranch != "feat/proj-1" {
		t.Fatalf("branch %q base %q, want feat/proj-1--t3 off feat/proj-1", cfg.Branch, cfg.BaseBranch)
	}
	if cfg.InitialPrompt != "Add the parser" {
		t.Fatalf("prompt = %q", cfg.InitialPrompt)
	}
	if cfg.ParentID != "p" || cfg.Repo.Name != "owner/r" || cfg.SessionBackend != "" {
		t.Fatalf("cfg parent %q repo %q backend %q", cfg.ParentID, cfg.Repo.Name, cfg.SessionBackend)
	}
	if cfg.Agent == nil || cfg.Agent.Command != "claude" {
		t.Fatalf("agent = %+v, want the preferred claude", cfg.Agent)
	}
	if it := parentT3(t, srv); it.Ref != "child:new-id" || it.State != domain.TodoPending {
		t.Fatalf("t3 = %+v, want pending with ref child:new-id", it)
	}
	settle()
	if q := srv.Steerer().Status("p").Queued; q != 0 {
		t.Fatalf("linking queued %d steer notes, want none", q)
	}
}

func TestCreateForRefusesLinkedItem(t *testing.T) {
	srv, creator := forServer(t, "child:old")
	resp := rpc(t, srv, "", "session.create", map[string]any{"for": "t3", "parent": "p"})
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "child:old") {
		t.Fatalf("error = %+v, want a refusal naming child:old", resp.Error)
	}
	if creator.calls != 0 {
		t.Fatalf("created %d sessions, want none", creator.calls)
	}
	if it := parentT3(t, srv); it.Ref != "child:old" {
		t.Fatalf("t3 ref = %q, want child:old", it.Ref)
	}
}

func TestCreateForUnknownItem(t *testing.T) {
	srv, creator := forServer(t, "")
	resp := rpc(t, srv, "", "session.create", map[string]any{"for": "t9", "parent": "p"})
	if resp.Error == nil || !strings.Contains(resp.Error.Message, "t9") {
		t.Fatalf("error = %+v, want one naming t9", resp.Error)
	}
	if creator.calls != 0 {
		t.Fatalf("created %d sessions, want none", creator.calls)
	}
}

func TestCreateForRequiresParent(t *testing.T) {
	srv, creator := forServer(t, "")
	resp := rpc(t, srv, "", "session.create", map[string]any{"for": "t3"})
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("error = %+v, want invalid_params", resp.Error)
	}
	if creator.calls != 0 {
		t.Fatalf("created %d sessions, want none", creator.calls)
	}
}

// The item is linked by someone else while the child is being created. The
// child is forgotten, so neither a dangling row nor a second link remains.
func TestCreateForForgetsChildWhenLinkFails(t *testing.T) {
	srv, creator := forServer(t, "")
	creator.onCreate = func() {
		err := srv.TodoAccess().Mutate(context.Background(), "p", domain.AuthorAgent, func(l *domain.TodoList) error {
			for i := range l.Items {
				if l.Items[i].ID == "t3" {
					l.Items[i].Ref = "child:other"
				}
			}
			return nil
		})
		if err != nil {
			t.Error(err)
		}
	}
	resp := rpc(t, srv, "", "session.create", map[string]any{"for": "t3", "parent": "p"})
	if resp.Error == nil || resp.Error.Code != CodeCreateFailed {
		t.Fatalf("error = %+v, want create_failed", resp.Error)
	}
	if _, err := srv.repo.Get(context.Background(), "new-id"); err == nil {
		t.Fatal("the child row survived a failed link")
	}
	if it := parentT3(t, srv); it.Ref != "child:other" {
		t.Fatalf("t3 ref = %q, want child:other", it.Ref)
	}
}

const testBrief = "## Question\n\nDoes the parser accept nested lists?\n\n## Scope\n\ninternal/parse only.\n\n## Report must contain\n\nThe tests you ran.\n"

func TestCreateForCarriesBrief(t *testing.T) {
	srv, creator := forServer(t, "")
	resp := rpc(t, srv, "", "session.create", map[string]any{"for": "t3", "parent": "p", "brief": testBrief})
	if resp.Error != nil {
		t.Fatalf("create: %+v", resp.Error)
	}
	if creator.cfg.Brief != testBrief {
		t.Fatalf("cfg.Brief = %q, want the brief", creator.cfg.Brief)
	}
}

func TestCreateReadsBriefFile(t *testing.T) {
	srv, creator := forServer(t, "")
	path := filepath.Join(t.TempDir(), "brief.md")
	if err := os.WriteFile(path, []byte(testBrief), 0o600); err != nil {
		t.Fatal(err)
	}
	resp := rpc(t, srv, "", "session.create", map[string]any{"for": "t3", "parent": "p", "brief_file": path})
	if resp.Error != nil {
		t.Fatalf("create: %+v", resp.Error)
	}
	if creator.cfg.Brief != testBrief {
		t.Fatalf("cfg.Brief = %q, want the file's brief", creator.cfg.Brief)
	}
}

func TestCreateRejectsBadBrief(t *testing.T) {
	srv, creator := forServer(t, "")
	bad := strings.Replace(testBrief, "## Scope", "## Notes", 1)
	resp := rpc(t, srv, "", "session.create", map[string]any{"for": "t3", "parent": "p", "brief": bad})
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams || !strings.Contains(resp.Error.Message, "## Scope") {
		t.Fatalf("error = %+v, want invalid_params naming ## Scope", resp.Error)
	}
	if creator.calls != 0 {
		t.Fatalf("created %d sessions, want none", creator.calls)
	}
	if it := parentT3(t, srv); it.Ref != "" {
		t.Fatalf("t3 ref = %q, want unlinked", it.Ref)
	}
}

// A child whose link fails is torn down, not just forgotten: its PTY is
// killed and its worktree and branch are removed, so a retry can recreate
// them.
func TestCreateForRollbackTearsDownPTYChild(t *testing.T) {
	srv, creator := forServer(t, "")
	remote := &fakeRemote{}
	srv.remote = remote
	pty := ptyfake.New()
	pty.Add(ptyruntime.SessionInfo{ID: "new-id", Name: "child-term"})
	srv.pty = pty
	creator.sess = &domain.Session{
		ID: "new-id", Name: "child", Status: domain.SessionStatusActive, Backend: domain.BackendPTY,
		TmuxSession: "child-term", WorktreePath: "/r/repo@feat--t3", Branch: "feat--t3",
	}
	creator.onCreate = func() {
		_ = srv.TodoAccess().Mutate(context.Background(), "p", domain.AuthorAgent, func(l *domain.TodoList) error {
			for i := range l.Items {
				if l.Items[i].ID == "t3" {
					l.Items[i].Ref = "child:other"
				}
			}
			return nil
		})
	}
	resp := rpc(t, srv, "", "session.create", map[string]any{"for": "t3", "parent": "p"})
	if resp.Error == nil || resp.Error.Code != CodeCreateFailed {
		t.Fatalf("error = %+v, want create_failed", resp.Error)
	}
	if kills := pty.Kills(); len(kills) == 0 || kills[0] != "new-id" {
		t.Errorf("rollback did not kill the child's pty; kills = %v", kills)
	}
	all := strings.Join(remote.execs, "\n")
	if strings.Contains(all, "tmux") {
		t.Errorf("rollback ran tmux:\n%s", all)
	}
	for _, want := range []string{"worktree remove --force", "'/r/repo@feat--t3'", "branch -D 'feat--t3'"} {
		if !strings.Contains(all, want) {
			t.Errorf("rollback did not run %q; ran:\n%s", want, all)
		}
	}
}

func TestDiscardWorktreeScriptRemovesWorktreeAndBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := shortTempDir(t)
	main := filepath.Join(root, "repo")
	wt := filepath.Join(root, "repo@feat--t3")
	for _, args := range [][]string{
		{"init", "-b", "main", main},
		{"-C", main, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit", "--allow-empty", "-m", "base"},
		{"-C", main, "worktree", "add", "-b", "feat--t3", wt},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if out, err := exec.Command("bash", "-c", discardWorktreeScript(wt, "feat--t3")).CombinedOutput(); err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatalf("worktree still present: %v", err)
	}
	if out, _ := exec.Command("git", "-C", main, "branch", "--list", "feat--t3").Output(); strings.TrimSpace(string(out)) != "" {
		t.Fatalf("branch still present: %q", out)
	}
}
