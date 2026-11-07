package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/ptyruntime/ptyfake"
	"github.com/bouwerp/aiman/internal/worktree"
)

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func putFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// gitFixture is a real repository with an origin, a main checkout and one
// linked worktree on branch feature.
type gitFixture struct{ base, main, linked string }

func newGitFixture(t *testing.T) gitFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := gitFixture{base: base, main: filepath.Join(base, "repo"), linked: filepath.Join(base, "repo@feature")}
	if err := os.MkdirAll(f.main, 0o755); err != nil {
		t.Fatal(err)
	}
	origin := filepath.Join(base, "origin.git")
	gitRun(t, base, "init", "--bare", "-q", origin)
	gitRun(t, f.main, "init", "-q", "-b", "main")
	putFile(t, filepath.Join(f.main, "a.txt"), "a\n")
	gitRun(t, f.main, "add", ".")
	gitRun(t, f.main, "commit", "-qm", "init")
	gitRun(t, f.main, "remote", "add", "origin", origin)
	gitRun(t, f.main, "push", "-q", "origin", "main")
	gitRun(t, f.main, "worktree", "add", "-q", "-b", "feature", f.linked)
	return f
}

func (f gitFixture) commitUnpushed(t *testing.T) {
	t.Helper()
	putFile(t, filepath.Join(f.linked, "committed.txt"), "c\n")
	gitRun(t, f.linked, "add", ".")
	gitRun(t, f.linked, "commit", "-qm", "unpushed")
}

// lifecycleRig is a server over a real database and a fake PTY runtime.
type lifecycleRig struct {
	t      *testing.T
	srv    *Server
	repo   domain.SessionRepository
	pty    *ptyfake.FakePTY
	remote *recordingRemote
}

// recordingRemote records commands and keeps written files, so a test can read
// the pty params and prompt a restart delivered.
type recordingRemote struct {
	fakeRemote
	mu    sync.Mutex
	files map[string]string
}

func (r *recordingRemote) WriteFile(_ context.Context, path string, b []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.files == nil {
		r.files = map[string]string{}
	}
	r.files[path] = string(b)
	return nil
}

func (r *recordingRemote) file(path string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.files[path]
}

func (r *recordingRemote) ran(sub string) bool {
	r.fakeRemote.mu.Lock()
	defer r.fakeRemote.mu.Unlock()
	return slices.ContainsFunc(r.execs, func(c string) bool { return strings.Contains(c, sub) })
}

func newLifecycleRig(t *testing.T) *lifecycleRig {
	t.Helper()
	repo := testRepo(t)
	pty := ptyfake.New()
	remote := &recordingRemote{}
	ln, err := Listen(shortTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := New(ln, repo, remote, nil, nil, pty, "test")
	srv.SetAgentChoice(config.AgentsConfig{}, func(_ context.Context, command string) bool {
		return command == "claude" || command == "codex"
	})
	return &lifecycleRig{t: t, srv: srv, repo: repo, pty: pty, remote: remote}
}

func (r *lifecycleRig) session(s domain.Session) domain.Session {
	r.t.Helper()
	if s.Name == "" {
		s.Name = s.ID
	}
	if s.Group == "" {
		s.Group = "g"
	}
	s.Status = domain.SessionStatusActive
	s.CreatedAt = time.Now()
	if s.Backend == "" {
		s.Backend = domain.BackendPTY
	}
	if err := r.repo.Save(context.Background(), &s); err != nil {
		r.t.Fatal(err)
	}
	if s.Backend == domain.BackendPTY {
		r.pty.Add(ptyruntime.SessionInfo{ID: s.ID, Name: s.Name, Dir: s.WorkingDirectory, Status: ptyruntime.StatusRunning})
	}
	return s
}

func (r *lifecycleRig) callCtx(ctx context.Context, caller, method string, params map[string]any) Response {
	r.t.Helper()
	b, err := json.Marshal(params)
	if err != nil {
		r.t.Fatal(err)
	}
	line, err := json.Marshal(Request{ID: "1", Method: method, Params: b, Caller: caller})
	if err != nil {
		r.t.Fatal(err)
	}
	return r.srv.dispatch(ctx, line)
}

func (r *lifecycleRig) call(method string, params map[string]any) Response {
	r.t.Helper()
	return r.callCtx(context.Background(), "", method, params)
}

func (r *lifecycleRig) terminate(session string, discard bool) Response {
	r.t.Helper()
	return r.call("session.terminate", map[string]any{"session": session, "discard": discard})
}

func (r *lifecycleRig) rowExists(id string) bool {
	r.t.Helper()
	list, err := r.repo.List(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	return slices.ContainsFunc(list, func(s domain.Session) bool { return s.ID == id })
}

func (r *lifecycleRig) row() domain.Session {
	r.t.Helper()
	list, err := r.repo.List(context.Background())
	if err != nil {
		r.t.Fatal(err)
	}
	for _, s := range list {
		if s.ID == "s1" {
			return s
		}
	}
	r.t.Fatal("no row s1")
	return domain.Session{}
}

func (r *lifecycleRig) eventKinds() []domain.EventKind {
	r.t.Helper()
	reader, ok := r.repo.(sessionEventLister)
	if !ok {
		r.t.Fatal("repo reads no events")
	}
	events, err := reader.ListSessionEvents(context.Background(), "s1", 0, 100)
	if err != nil {
		r.t.Fatal(err)
	}
	kinds := make([]domain.EventKind, 0, len(events))
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

func lifecycleResult(t *testing.T, resp Response) map[string]any {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("error: %+v", resp.Error)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestTerminateCleanWorktreeRemovesItKeepsBranch(t *testing.T) {
	r := newLifecycleRig(t)
	f := newGitFixture(t)
	s := r.session(domain.Session{ID: "s1", WorktreePath: f.linked, WorkingDirectory: f.linked, Branch: "feature", RepoName: "o/repo"})
	// Scaffolding every real worktree holds must not read as work.
	putFile(t, filepath.Join(f.linked, ".claude/skills/aiman/SKILL.md"), "skill")

	got := lifecycleResult(t, r.terminate(s.ID, false))
	want := map[string]any{"type": "session_terminated", "session": "s1", "worktree_removed": true, "branch": "feature"}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("%s = %v, want %v (%v)", k, got[k], v, got)
		}
	}
	if pathExists(f.linked) {
		t.Fatal("worktree still there")
	}
	if gitRun(t, f.main, "rev-parse", "--verify", "refs/heads/feature") == "" {
		t.Fatal("branch deleted")
	}
	if !pathExists(filepath.Join(f.main, "a.txt")) {
		t.Fatal("main checkout damaged")
	}
	if r.rowExists("s1") {
		t.Fatal("row survived")
	}
	if !slices.Contains(r.pty.Kills(), "s1") {
		t.Fatalf("terminal not stopped: %v", r.pty.Kills())
	}
	kinds := r.eventKinds()
	hi, ei := slices.Index(kinds, domain.EventHandoverWritten), slices.Index(kinds, domain.EventSessionEnded)
	if hi < 0 || ei < hi {
		t.Fatalf("handover must come before the end: %v", kinds)
	}
}

func TestTerminateRefusesDirtyWorktreeBeforeStoppingAnything(t *testing.T) {
	r := newLifecycleRig(t)
	f := newGitFixture(t)
	s := r.session(domain.Session{ID: "s1", WorktreePath: f.linked, WorkingDirectory: f.linked, Branch: "feature"})
	putFile(t, filepath.Join(f.linked, "a.txt"), "changed\n")
	putFile(t, filepath.Join(f.linked, "new.txt"), "new\n")
	f.commitUnrelated(t)

	resp := r.terminate(s.ID, false)
	wantCode(t, resp, CodeWorktreeDirty)
	data, ok := resp.Error.Data.(WorktreeDirtyData)
	if !ok {
		t.Fatalf("data %T", resp.Error.Data)
	}
	if data.ChangedFiles != 2 || data.UnpushedCommits != 1 || !slices.Contains(data.Paths, "new.txt") {
		t.Fatalf("%+v", data)
	}
	if len(r.pty.Kills()) != 0 {
		t.Fatalf("stopped the terminal: %v", r.pty.Kills())
	}
	if !r.rowExists("s1") || !pathExists(f.linked) {
		t.Fatal("row or worktree gone")
	}
	if slices.Contains(r.eventKinds(), domain.EventHandoverWritten) || pathExists(filepath.Join(f.linked, domain.AimanHandoverFileName)) {
		t.Fatal("handover written before the refusal")
	}
}

// commitUnrelated commits an unrelated file, leaving the rest of the tree dirty.
func (f gitFixture) commitUnrelated(t *testing.T) {
	t.Helper()
	putFile(t, filepath.Join(f.linked, "committed.txt"), "c\n")
	gitRun(t, f.linked, "add", "committed.txt")
	gitRun(t, f.linked, "commit", "-qm", "unpushed")
}

func TestWorktreeDirtyDataCrossesTheSocket(t *testing.T) {
	repo := testRepo(t)
	f := newGitFixture(t)
	if err := repo.Save(context.Background(), &domain.Session{
		ID: "s1", Name: "s1", Group: "g", WorktreePath: f.linked, WorkingDirectory: f.linked,
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	putFile(t, filepath.Join(f.linked, "new.txt"), "new\n")
	sock := startTestServer(t, repo)
	t.Setenv("AIMAN_ID", "")
	resp, err := Call(sock, "session.terminate", map[string]any{"session": "s1"})
	if err != nil {
		t.Fatal(err)
	}
	wantCode(t, resp, CodeWorktreeDirty)
	data, ok := resp.Error.Data.(map[string]any)
	if !ok {
		t.Fatalf("data %T", resp.Error.Data)
	}
	if data["changed_files"] != float64(1) || data["unpushed_commits"] != float64(0) {
		t.Fatalf("%v", data)
	}
	if paths, _ := data["paths"].([]any); len(paths) != 1 || paths[0] != "new.txt" {
		t.Fatalf("%v", data)
	}
	if len(data) != 3 {
		t.Fatalf("the app reads exactly three fields: %v", data)
	}
}

func TestTerminateCapsListedPathsAtTwenty(t *testing.T) {
	r := newLifecycleRig(t)
	f := newGitFixture(t)
	s := r.session(domain.Session{ID: "s1", WorktreePath: f.linked, WorkingDirectory: f.linked})
	for i := range 25 {
		putFile(t, filepath.Join(f.linked, "n"+strings.Repeat("x", i)+".txt"), "x")
	}
	resp := r.terminate(s.ID, false)
	wantCode(t, resp, CodeWorktreeDirty)
	data := resp.Error.Data.(WorktreeDirtyData)
	if data.ChangedFiles != 25 || len(data.Paths) != 20 {
		t.Fatalf("%d files, %d paths", data.ChangedFiles, len(data.Paths))
	}
}

func TestTerminateRefusesUnpushedCommitsThenDiscardKeepsBranch(t *testing.T) {
	r := newLifecycleRig(t)
	f := newGitFixture(t)
	s := r.session(domain.Session{ID: "s1", WorktreePath: f.linked, WorkingDirectory: f.linked, Branch: "feature"})
	f.commitUnpushed(t)

	resp := r.terminate(s.ID, false)
	wantCode(t, resp, CodeWorktreeDirty)
	if data := resp.Error.Data.(WorktreeDirtyData); data.UnpushedCommits != 1 || data.ChangedFiles != 0 {
		t.Fatalf("%+v", data)
	}

	got := lifecycleResult(t, r.terminate(s.ID, true))
	if got["worktree_removed"] != true {
		t.Fatalf("%v", got)
	}
	if pathExists(f.linked) {
		t.Fatal("worktree still there")
	}
	if gitRun(t, f.main, "show", "feature:committed.txt") != "c" {
		t.Fatal("branch or its commit lost")
	}
}

func TestTerminateDiscardRemovesDirtyLinkedWorktree(t *testing.T) {
	r := newLifecycleRig(t)
	f := newGitFixture(t)
	s := r.session(domain.Session{ID: "s1", WorktreePath: f.linked, WorkingDirectory: f.linked})
	putFile(t, filepath.Join(f.linked, "a.txt"), "changed\n")
	got := lifecycleResult(t, r.terminate(s.ID, true))
	if got["worktree_removed"] != true || pathExists(f.linked) {
		t.Fatalf("%v", got)
	}
	gitRun(t, f.main, "rev-parse", "--verify", "refs/heads/feature")
}

// Real sessions have worktree_path equal to the main checkout. Neither a dirty
// tree nor discard may touch it, and terminating loses nothing there, so a dirty
// tree is not refused either.
func TestTerminateNeverRemovesMainCheckout(t *testing.T) {
	for _, discard := range []bool{false, true} {
		r := newLifecycleRig(t)
		f := newGitFixture(t)
		s := r.session(domain.Session{ID: "s1", WorktreePath: f.main, WorkingDirectory: f.main, Branch: "main"})
		putFile(t, filepath.Join(f.main, "dirty.txt"), "keep me\n")

		got := lifecycleResult(t, r.terminate(s.ID, discard))
		if got["worktree_removed"] != false || got["branch"] != "main" {
			t.Fatalf("discard=%v: %v", discard, got)
		}
		if note, _ := got["worktree_note"].(string); !strings.Contains(note, "main checkout") {
			t.Fatalf("note %q", note)
		}
		if b, err := os.ReadFile(filepath.Join(f.main, "dirty.txt")); err != nil || string(b) != "keep me\n" {
			t.Fatalf("dirty file: %q %v", b, err)
		}
		if !pathExists(filepath.Join(f.main, ".git")) || !pathExists(f.linked) {
			t.Fatal("repository damaged")
		}
		if r.rowExists("s1") || !slices.Contains(r.pty.Kills(), "s1") {
			t.Fatal("session not stopped and forgotten")
		}
	}
}

func TestTerminateWithoutAWorktreeStopsAndForgets(t *testing.T) {
	plain := t.TempDir()
	putFile(t, filepath.Join(plain, "keep.txt"), "keep")
	cases := map[string]domain.Session{
		"no path":        {ID: "s1", WorkingDirectory: plain},
		"missing path":   {ID: "s1", WorktreePath: filepath.Join(plain, "gone"), WorkingDirectory: plain},
		"not a git repo": {ID: "s1", WorktreePath: plain, WorkingDirectory: plain},
	}
	for name, row := range cases {
		t.Run(name, func(t *testing.T) {
			for _, discard := range []bool{false, true} {
				r := newLifecycleRig(t)
				s := r.session(row)
				got := lifecycleResult(t, r.terminate(s.ID, discard))
				if got["worktree_removed"] != false {
					t.Fatalf("%v", got)
				}
				if r.rowExists("s1") || !slices.Contains(r.pty.Kills(), "s1") {
					t.Fatal("session not stopped and forgotten")
				}
				if !pathExists(filepath.Join(plain, "keep.txt")) {
					t.Fatal("directory touched")
				}
			}
		})
	}
}

func TestTerminateSubdirectoryOfALinkedWorktreeRemovesNothing(t *testing.T) {
	r := newLifecycleRig(t)
	f := newGitFixture(t)
	sub := filepath.Join(f.linked, "sub")
	putFile(t, filepath.Join(sub, "x"), "x")
	s := r.session(domain.Session{ID: "s1", WorktreePath: sub, WorkingDirectory: sub})
	got := lifecycleResult(t, r.terminate(s.ID, true))
	if got["worktree_removed"] != false || !pathExists(filepath.Join(sub, "x")) {
		t.Fatalf("%v", got)
	}
}

func TestTerminateKeepsWorktreeSharedWithAnotherSession(t *testing.T) {
	sharers := map[string]func(linked string) domain.Session{
		"same worktree path": func(l string) domain.Session { return domain.Session{ID: "s2", WorktreePath: l, WorkingDirectory: l} },
		"working inside it": func(l string) domain.Session {
			return domain.Session{ID: "s2", WorkingDirectory: filepath.Join(l, "sub")}
		},
		"a child": func(l string) domain.Session {
			return domain.Session{ID: "s2", ParentID: "s1", WorktreePath: l, WorkingDirectory: l}
		},
	}
	for name, mk := range sharers {
		t.Run(name, func(t *testing.T) {
			r := newLifecycleRig(t)
			f := newGitFixture(t)
			s := r.session(domain.Session{ID: "s1", WorktreePath: f.linked, WorkingDirectory: f.linked})
			r.session(mk(f.linked))
			putFile(t, filepath.Join(f.linked, "work.txt"), "the other session's work\n")

			got := lifecycleResult(t, r.terminate(s.ID, true))
			if got["worktree_removed"] != false {
				t.Fatalf("%v", got)
			}
			if !pathExists(filepath.Join(f.linked, "work.txt")) {
				t.Fatal("shared tree removed")
			}
			if r.rowExists("s1") || !r.rowExists("s2") {
				t.Fatal("wrong rows forgotten")
			}
			if slices.Contains(r.pty.Kills(), "s2") {
				t.Fatal("the sharer was stopped")
			}
		})
	}
}

func TestTerminateFollowsSymlinksWhenComparingWorktrees(t *testing.T) {
	r := newLifecycleRig(t)
	f := newGitFixture(t)
	link := filepath.Join(f.base, "alias")
	if err := os.Symlink(f.linked, link); err != nil {
		t.Fatal(err)
	}
	s := r.session(domain.Session{ID: "s1", WorktreePath: f.linked, WorkingDirectory: f.linked})
	r.session(domain.Session{ID: "s2", WorktreePath: link, WorkingDirectory: link})
	if got := lifecycleResult(t, r.terminate(s.ID, true)); got["worktree_removed"] != false {
		t.Fatalf("%v", got)
	}
}

func TestSessionCallersCannotTerminateOrRestart(t *testing.T) {
	r := newLifecycleRig(t)
	f := newGitFixture(t)
	s := r.session(domain.Session{ID: "s1", WorktreePath: f.linked, WorkingDirectory: f.linked, AgentName: "claude"})
	r.srv.procInSession = func(pid int) bool { return pid == 4242 }
	calls := map[string]map[string]any{
		"session.terminate": {"session": s.ID, "discard": true},
		"session.restart":   {"session": s.ID, "agent": "codex"},
	}
	for method, params := range calls {
		wantCode(t, r.callCtx(context.Background(), "s1", method, params), CodeNeedsOperator)
		// An agent that clears AIMAN_ID is still a session.
		wantCode(t, r.callCtx(withPeerPID(context.Background(), 4242, true), "", method, params), CodeNeedsOperator)
	}
	if len(r.pty.Kills()) != 0 || !pathExists(f.linked) || !r.rowExists("s1") {
		t.Fatal("a refused call touched the session")
	}
	// The gateway calls with no caller from outside any holder.
	if got := lifecycleResult(t, r.callCtx(withPeerPID(context.Background(), 77, true), "", "session.terminate", map[string]any{"session": s.ID})); got["worktree_removed"] != true {
		t.Fatalf("operator refused: %v", got)
	}
}

func TestTerminateRefusesLegacyAndHiddenAndUnknown(t *testing.T) {
	r := newLifecycleRig(t)
	f := newGitFixture(t)
	r.session(domain.Session{ID: "old", WorktreePath: f.linked, WorkingDirectory: f.linked, Backend: domain.BackendLegacyTmux, TerminalName: "old"})
	r.session(domain.Session{ID: "helper", Role: domain.SessionRoleConsult, ParentID: "p"})
	wantCode(t, r.terminate("old", true), CodeLegacyTerminal)
	wantCode(t, r.terminate("helper", true), CodeInvalidParams)
	wantCode(t, r.terminate("nope", true), CodeNotFound)
	wantCode(t, r.terminate("", true), CodeInvalidParams)
	if !pathExists(f.linked) || !r.rowExists("old") {
		t.Fatal("a refused call touched the session")
	}
}

// The TUI asks with a tick box, off by default: left alone, children keep
// running with no parent. Hidden helpers are aiman's own and go by their rules.
func TestTerminateChildrenAndHelpers(t *testing.T) {
	r := newLifecycleRig(t)
	f := newGitFixture(t)
	scratch := t.TempDir()
	consultBase := t.TempDir()
	r.srv.SetConsultDir(consultBase)
	consultDir := filepath.Join(consultBase, "consult-1")
	putFile(t, filepath.Join(consultDir, "brief"), "x")
	s := r.session(domain.Session{ID: "s1", WorktreePath: f.linked, WorkingDirectory: f.linked})
	r.session(domain.Session{ID: "kid", ParentID: "s1", WorkingDirectory: scratch})
	r.session(domain.Session{ID: "ended", ParentID: "s1", WorkingDirectory: scratch, AgentEnded: true})
	r.session(domain.Session{ID: "consult-1", ParentID: "s1", Role: domain.SessionRoleConsult, WorkingDirectory: consultDir})
	r.session(domain.Session{ID: "handover-1", ParentID: "s1", Role: domain.SessionRoleHandover, WorkingDirectory: scratch})
	r.session(domain.Session{ID: "rev-1", ParentID: "s1", Role: domain.SessionRoleReviewer, WorkingDirectory: scratch})

	got := lifecycleResult(t, r.terminate(s.ID, false))
	if got["worktree_removed"] != true {
		t.Fatalf("%v", got)
	}
	left, _ := got["children_left"].([]any)
	if len(left) != 1 || left[0] != "kid" {
		t.Fatalf("children_left %v", got["children_left"])
	}
	if !r.rowExists("kid") || slices.Contains(r.pty.Kills(), "kid") {
		t.Fatal("a visible child was stopped")
	}
	if r.rowExists("consult-1") || pathExists(consultDir) || !slices.Contains(r.pty.Kills(), "consult-1") {
		t.Fatal("consult not retired")
	}
	if !r.rowExists("handover-1") || slices.Contains(r.pty.Kills(), "handover-1") {
		t.Fatal("the handover child must finish on its own")
	}
	if !slices.Contains(r.pty.Kills(), "rev-1") {
		t.Fatal("reviewer not stopped")
	}
}

// stuckPTY never lets a session die.
type stuckPTY struct{ *ptyfake.FakePTY }

func (stuckPTY) Kill(string) error { return errors.New("pty: session did not stop in time") }

func TestTerminateKeepsEverythingWhenTheAgentWontStop(t *testing.T) {
	r := newLifecycleRig(t)
	f := newGitFixture(t)
	s := r.session(domain.Session{ID: "s1", WorktreePath: f.linked, WorkingDirectory: f.linked})
	r.srv.pty = stuckPTY{r.pty}
	wantCode(t, r.terminate(s.ID, true), CodeTimeout)
	if !pathExists(f.linked) || !r.rowExists("s1") {
		t.Fatal("worktree or row removed under a running agent")
	}
}

// The agent runs until the stop, so what it did after the first check counts.
func TestRemoveWorktreeRechecksAfterTheStop(t *testing.T) {
	ctx := context.Background()
	r := newLifecycleRig(t)
	f := newGitFixture(t)
	plan := worktreePlan{info: worktree.Inspect(ctx, f.linked)}
	f.commitUnpushed(t) // a commit worktree remove would not itself refuse
	removed, note := r.srv.removeWorktree(ctx, plan, false)
	if removed || !strings.Contains(note, "unpushed") {
		t.Fatalf("removed=%v note=%q", removed, note)
	}
	if !pathExists(f.linked) {
		t.Fatal("worktree removed")
	}
}

func TestTerminateReportsALockedWorktree(t *testing.T) {
	r := newLifecycleRig(t)
	f := newGitFixture(t)
	s := r.session(domain.Session{ID: "s1", WorktreePath: f.linked, WorkingDirectory: f.linked})
	gitRun(t, f.main, "worktree", "lock", f.linked)
	got := lifecycleResult(t, r.terminate(s.ID, true))
	if got["worktree_removed"] != false || !pathExists(f.linked) || got["worktree_note"] == nil {
		t.Fatalf("%v", got)
	}
	if r.rowExists("s1") {
		t.Fatal("row survived")
	}
}

// launchEngine records what a restart prepared.
type launchEngine struct {
	mu     sync.Mutex
	agents []domain.Agent
}

func (e *launchEngine) Sync(context.Context) error          { return nil }
func (e *launchEngine) ListSkills() ([]domain.Skill, error) { return nil, nil }
func (e *launchEngine) PrepareSession(_ context.Context, _ domain.RemoteExecutor, _ string, a domain.Agent, _ []domain.Skill, _ bool, _ *domain.Issue, _ *domain.SessionSnapshot, _ domain.QualityKit) (domain.PreparedSession, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.agents = append(e.agents, a)
	cmd := a.Command
	if a.LaunchModel != "" {
		cmd += " --model " + a.LaunchModel
	}
	return domain.PreparedSession{Command: cmd}, nil
}

func (e *launchEngine) last(t *testing.T) domain.Agent {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.agents) != 1 {
		t.Fatalf("PrepareSession calls: %d", len(e.agents))
	}
	return e.agents[0]
}

func (r *lifecycleRig) restartable(agent string) (domain.Session, *launchEngine) {
	r.t.Helper()
	dir := r.t.TempDir()
	eng := &launchEngine{}
	r.srv.SetRestartDeps(RestartDeps{Engine: eng})
	return r.session(domain.Session{
		ID: "s1", TerminalName: "term-s1", WorktreePath: dir, WorkingDirectory: dir, AgentName: agent,
		AgentSessionID: "vendor-1", AgentSessionPath: "/x/.claude/projects/t.jsonl", AgentTitle: "title",
		LaunchModel: "old-model", LaunchEffort: "high",
	}), eng
}

func TestRestartSwitchClearsIdentityAndModel(t *testing.T) {
	r := newLifecycleRig(t)
	s, eng := r.restartable("claude")
	got := lifecycleResult(t, r.call("session.restart", map[string]any{"session": s.ID, "agent": "codex"}))
	if got["type"] != "session" {
		t.Fatalf("%v", got)
	}
	info := got["session"].(map[string]any)
	if info["id"] != "s1" || info["agent_name"] != "codex" || info["agent_session_id"] != nil || info["launch_model"] != nil {
		t.Fatalf("%v", info)
	}
	if a := eng.last(t); a.Command != "codex" || a.LaunchModel != "" || a.LaunchEffort != "" {
		t.Fatalf("launch agent %+v", a)
	}
	row := r.row()
	if row.AgentName != "codex" || row.AgentSessionID != "" || row.AgentSessionPath != "" || row.LaunchModel != "" || row.LaunchEffort != "" {
		t.Fatalf("row %+v", row)
	}
	if !r.remote.ran("native-sessions") || !r.remote.ran("aiman pty create") {
		t.Fatal("sidecar not cleared or terminal not recreated")
	}
	params := r.remote.file("$HOME/.aiman/tmp/aiman-pty-s1.json")
	if !strings.Contains(params, `"id":"s1"`) || strings.Contains(params, "resume") {
		t.Fatalf("pty params %s", params)
	}
	if !strings.Contains(r.remote.file("/tmp/aiman-prompt-s1"), domain.AimanHandoverFileName) {
		t.Fatalf("handoff prompt: %q", r.remote.file("/tmp/aiman-prompt-s1"))
	}
	if !pathExists(filepath.Join(s.WorkingDirectory, domain.AimanHandoverFileName)) {
		t.Fatal("handover not written")
	}
	if !slices.Contains(r.eventKinds(), domain.EventSessionRestarted) {
		t.Fatalf("%v", r.eventKinds())
	}
}

func TestRestartSwitchTakesANewModel(t *testing.T) {
	r := newLifecycleRig(t)
	s, eng := r.restartable("claude")
	lifecycleResult(t, r.call("session.restart", map[string]any{"session": s.ID, "agent": "codex", "model": "gpt-5", "effort": "low"}))
	if a := eng.last(t); a.LaunchModel != "gpt-5" || a.LaunchEffort != "low" {
		t.Fatalf("%+v", a)
	}
	if row := r.row(); row.LaunchModel != "gpt-5" || row.LaunchEffort != "low" {
		t.Fatalf("%+v", row)
	}
}

func TestRestartSameAgentKeepsIdentityAndModel(t *testing.T) {
	// The display name and the command are one agent.
	for _, stored := range []string{"claude", "Claude Code"} {
		r := newLifecycleRig(t)
		s, eng := r.restartable(stored)
		lifecycleResult(t, r.call("session.restart", map[string]any{"session": s.ID, "agent": "claude"}))
		if a := eng.last(t); a.LaunchModel != "old-model" || a.LaunchEffort != "high" {
			t.Fatalf("stored %q: %+v", stored, a)
		}
		row := r.row()
		if row.AgentSessionID != "vendor-1" || row.LaunchModel != "old-model" {
			t.Fatalf("stored %q: row %+v", stored, row)
		}
		if !strings.Contains(r.remote.file("$HOME/.aiman/tmp/aiman-pty-s1.json"), "--resume vendor-1") {
			t.Fatalf("stored %q: not resumed: %s", stored, r.remote.file("$HOME/.aiman/tmp/aiman-pty-s1.json"))
		}
	}
}

func TestRestartSameAgentOverridesModelOnly(t *testing.T) {
	r := newLifecycleRig(t)
	s, eng := r.restartable("claude")
	lifecycleResult(t, r.call("session.restart", map[string]any{"session": s.ID, "agent": "claude", "model": "new-model"}))
	if a := eng.last(t); a.LaunchModel != "new-model" || a.LaunchEffort != "high" {
		t.Fatalf("%+v", a)
	}
	if row := r.row(); row.LaunchModel != "new-model" || row.LaunchEffort != "high" || row.AgentSessionID != "vendor-1" {
		t.Fatalf("%+v", row)
	}
}

func TestRestartClearsAStoredSessionEnd(t *testing.T) {
	r := newLifecycleRig(t)
	s, _ := r.restartable("claude")
	s.AgentEnded = true
	if err := r.repo.Save(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	lifecycleResult(t, r.call("session.restart", map[string]any{"session": s.ID, "agent": "claude"}))
	if r.row().AgentEnded {
		t.Fatal("restarted agent still reads as ended")
	}
}

func TestRestartRefusals(t *testing.T) {
	r := newLifecycleRig(t)
	s, eng := r.restartable("claude")
	r.session(domain.Session{ID: "old", AgentName: "claude", WorkingDirectory: s.WorkingDirectory, Backend: domain.BackendLegacyTmux, TerminalName: "old"})
	r.session(domain.Session{ID: "helper", Role: domain.SessionRoleReviewer, ParentID: "s1", WorkingDirectory: s.WorkingDirectory})
	cases := []struct {
		name   string
		params map[string]any
		code   string
	}{
		{"not installed", map[string]any{"session": s.ID, "agent": "grok"}, CodeInvalidParams},
		{"no agent", map[string]any{"session": s.ID}, CodeInvalidParams},
		{"shell in agent", map[string]any{"session": s.ID, "agent": "claude; rm -rf /"}, CodeInvalidParams},
		{"shell in model", map[string]any{"session": s.ID, "agent": "claude", "model": "x; rm -rf /"}, CodeInvalidParams},
		{"unknown session", map[string]any{"session": "nope", "agent": "claude"}, CodeNotFound},
		{"legacy tmux", map[string]any{"session": "old", "agent": "claude"}, CodeLegacyTerminal},
		{"hidden helper", map[string]any{"session": "helper", "agent": "claude"}, CodeInvalidParams},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantCode(t, r.call("session.restart", c.params), c.code)
		})
	}
	if len(eng.agents) != 0 || r.remote.ran("pty create") || len(r.pty.Kills()) != 0 {
		t.Fatal("a refused restart touched something")
	}
}

func TestSlowMethodsWaitPastTheirHandlers(t *testing.T) {
	for method, handler := range map[string]time.Duration{
		"session.restart":   SessionRestartTimeout,
		"session.terminate": SessionTerminateTimeout,
		"repo.list":         RepoListTimeout,
	} {
		if got := callDeadline(method); got <= handler {
			t.Errorf("%s: deadline %s is not past its handler's %s", method, got, handler)
		}
	}
	if callDeadline("session.create") != SessionCreateWait {
		t.Errorf("session.create waits %s", callDeadline("session.create"))
	}
}

// scriptedRepoLister answers repo.list from a script.
type scriptedRepoLister struct {
	mu    sync.Mutex
	calls int
	repos []domain.Repo
	err   error
}

func (f *scriptedRepoLister) ListRepos(context.Context) ([]domain.Repo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.repos, f.err
}

func TestRepoListCachesAndServesStaleOnFailure(t *testing.T) {
	r := newLifecycleRig(t)
	at := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	now := at
	lister := &scriptedRepoLister{repos: []domain.Repo{{Name: "o/a", LastActivityAt: at}, {Name: "o/b"}}}
	r.srv.SetRepoLister(lister)
	r.srv.repos.now = func() time.Time { return now }

	got := lifecycleResult(t, r.call("repo.list", nil))
	if got["type"] != "repos" {
		t.Fatalf("%v", got)
	}
	repos := got["repos"].([]any)
	first, second := repos[0].(map[string]any), repos[1].(map[string]any)
	if first["slug"] != "o/a" || first["last_activity_at"] != "2026-11-03T10:00:00Z" || second["last_activity_at"] != "" {
		t.Fatalf("%v", repos)
	}

	now = now.Add(4 * time.Minute)
	lifecycleResult(t, r.call("repo.list", nil))
	if lister.calls != 1 {
		t.Fatalf("not cached: %d calls", lister.calls)
	}

	now = now.Add(2 * time.Minute)
	lister.err, lister.repos = errors.New("gh down"), nil
	if got := lifecycleResult(t, r.call("repo.list", nil)); len(got["repos"].([]any)) != 2 {
		t.Fatalf("stale list not served: %v", got)
	}
	if lister.calls != 2 {
		t.Fatalf("no refresh attempt: %d", lister.calls)
	}
	lifecycleResult(t, r.call("repo.list", nil))
	if lister.calls != 2 {
		t.Fatalf("failing refresh retried at once: %d", lister.calls)
	}

	now = now.Add(2 * time.Minute)
	lister.err, lister.repos = nil, []domain.Repo{{Name: "o/c"}}
	if got := lifecycleResult(t, r.call("repo.list", nil)); len(got["repos"].([]any)) != 1 {
		t.Fatalf("not refreshed: %v", got)
	}
}

func TestRepoListFailureWithNothingCached(t *testing.T) {
	r := newLifecycleRig(t)
	wantCode(t, r.call("repo.list", nil), CodeUnavailable)
	r.srv.SetRepoLister(&scriptedRepoLister{err: errors.New("gh down")})
	wantCode(t, r.call("repo.list", nil), CodeUnavailable)
}

func TestRepoListEmptyIsAnArray(t *testing.T) {
	r := newLifecycleRig(t)
	r.srv.SetRepoLister(&scriptedRepoLister{})
	raw, err := json.Marshal(r.call("repo.list", nil).Result)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"repos":[]`) {
		t.Fatalf("%s", raw)
	}
}

func TestHostStatsShape(t *testing.T) {
	r := newLifecycleRig(t)
	t.Cleanup(r.srv.hostStats.Close)
	r.pty.Add(ptyruntime.SessionInfo{ID: "s1", Name: "term", Status: ptyruntime.StatusRunning, PID: os.Getpid()})
	r.pty.Add(ptyruntime.SessionInfo{ID: "stale", Name: "stale", Status: ptyruntime.StatusRunning, PID: os.Getpid()})
	r.pty.Add(ptyruntime.SessionInfo{ID: "gone", Name: "gone", Status: ptyruntime.StatusExited, PID: os.Getpid()})
	r.pty.Add(ptyruntime.SessionInfo{ID: "pty-helper", Status: ptyruntime.StatusRunning, Role: string(domain.SessionRoleHandover)})
	r.session(domain.Session{ID: "row-helper", ParentID: "s1", Role: domain.SessionRoleConsult})
	r.srv.holderHeld = func(int) bool { return false }
	got := lifecycleResult(t, r.call("host.stats", nil))
	if got["type"] != "host_stats" {
		t.Fatalf("%v", got)
	}
	for _, key := range []string{"host", "uptime_seconds", "load", "cpu_count", "cpu_percent", "memory", "swap", "disks", "processes", "sessions"} {
		if _, ok := got[key]; !ok {
			t.Errorf("no %s in %v", key, got)
		}
	}
	sessions := got["sessions"].([]any)
	if len(sessions) != 2 {
		t.Fatalf("exited session listed: %v", sessions)
	}
	// A PID no holder runs may have been reused: it reports nothing.
	for _, s := range sessions {
		if m := s.(map[string]any); m["processes"] != float64(0) || m["rss"] != float64(0) {
			t.Fatalf("stranger's process counted: %v", m)
		}
	}

	r.srv.holderHeld = func(int) bool { return true }
	got = lifecycleResult(t, r.call("host.stats", nil))
	first := got["sessions"].([]any)[0].(map[string]any)
	if first["session"] == "" || first["name"] == "" || first["processes"].(float64) < 1 {
		t.Fatalf("%v", first)
	}
}

// The phone starts a session with a repo, a branch, an agent, a model and a prompt.
func TestSessionCreateTakesWhatThePhoneSends(t *testing.T) {
	repo := testRepo(t)
	ln, err := Listen(shortTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	creator := &fakeCreator{}
	srv := New(ln, repo, &fakeRemote{}, creator, nil, nil, "test")
	b, _ := json.Marshal(map[string]any{
		"repo": "o/r", "branch": "feature", "agent": "codex", "model": "gpt-5", "effort": "high", "prompt": "fix it",
	})
	line, _ := json.Marshal(Request{ID: "1", Method: "session.create", Params: b})
	if resp := srv.dispatch(context.Background(), line); resp.Error != nil {
		t.Fatal(resp.Error)
	}
	c := creator.cfg
	if c.Repo.Name != "o/r" || c.Branch != "feature" || c.InitialPrompt != "fix it" || c.Agent == nil {
		t.Fatalf("%+v", c)
	}
	if c.Agent.Command != "codex" || c.Agent.LaunchModel != "gpt-5" || c.Agent.LaunchEffort != "high" {
		t.Fatalf("%+v", c.Agent)
	}
}

func createOnFakeCreator(t *testing.T, creator *fakeCreator, params map[string]any) Response {
	t.Helper()
	repo := testRepo(t)
	ln, err := Listen(shortTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	srv := New(ln, repo, &fakeRemote{}, creator, nil, nil, "test")
	b, _ := json.Marshal(params)
	line, _ := json.Marshal(Request{ID: "1", Method: "session.create", Params: b})
	return srv.dispatch(context.Background(), line)
}

// A name is free text; the branch comes from it, as in the TUI's wizard.
func TestSessionCreateDerivesBranchFromName(t *testing.T) {
	creator := &fakeCreator{}
	resp := createOnFakeCreator(t, creator, map[string]any{
		"repo": "o/r", "name": "Deploy API: phase 2!", "agent": "codex",
	})
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if creator.cfg.Name != "Deploy API: phase 2!" || creator.cfg.Branch != "Deploy-API-phase-2" {
		t.Fatalf("name %q branch %q", creator.cfg.Name, creator.cfg.Branch)
	}
}

func TestSessionCreateNameWithNoBranchCharactersStillGetsABranch(t *testing.T) {
	creator := &fakeCreator{}
	resp := createOnFakeCreator(t, creator, map[string]any{"repo": "o/r", "name": "日本語", "agent": "codex"})
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if creator.cfg.Name != "日本語" || !strings.HasPrefix(creator.cfg.Branch, "session-") {
		t.Fatalf("name %q branch %q", creator.cfg.Name, creator.cfg.Branch)
	}
}

// A derived branch whose worktree exists takes a suffix: a new session must not
// land in an old session's files.
func TestSessionCreateDerivedBranchAvoidsAnExistingWorktree(t *testing.T) {
	creator := &fakeCreator{failWith: func(cfg domain.SessionConfig) error {
		if cfg.Branch == "fix-login" || cfg.Branch == "fix-login-2" {
			return fmt.Errorf("worktree: %w", errors.New("WORKTREE_EXISTS"))
		}
		return nil
	}}
	resp := createOnFakeCreator(t, creator, map[string]any{"repo": "o/r", "name": "fix login", "agent": "codex"})
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if got := strings.Join(creator.branches, " "); got != "fix-login fix-login-2 fix-login-3" {
		t.Fatalf("branches tried: %s", got)
	}
}

func TestSessionCreateDerivedBranchGivesUpOnOtherFailures(t *testing.T) {
	creator := &fakeCreator{failWith: func(domain.SessionConfig) error { return errors.New("no such repo") }}
	resp := createOnFakeCreator(t, creator, map[string]any{"repo": "o/r", "name": "fix login", "agent": "codex"})
	wantCode(t, resp, CodeCreateFailed)
	if creator.calls != 1 {
		t.Fatalf("calls = %d", creator.calls)
	}
}

// A branch the caller named means that branch, so a collision is its to resolve.
func TestSessionCreateExplicitBranchIsNotSuffixed(t *testing.T) {
	creator := &fakeCreator{failWith: func(domain.SessionConfig) error { return errors.New("WORKTREE_EXISTS") }}
	resp := createOnFakeCreator(t, creator, map[string]any{"repo": "o/r", "branch": "feature", "agent": "codex"})
	wantCode(t, resp, CodeCreateFailed)
	if creator.calls != 1 {
		t.Fatalf("calls = %d", creator.calls)
	}
}

func TestSessionCreateRefusesWhatItCannotName(t *testing.T) {
	for _, params := range []map[string]any{
		{"repo": "o/r", "agent": "codex"},
		{"name": "x", "agent": "codex"},
		{"repo": "o/r", "name": "x", "existing_branch": true, "agent": "codex"},
	} {
		creator := &fakeCreator{}
		resp := createOnFakeCreator(t, creator, params)
		wantCode(t, resp, CodeInvalidParams)
		if creator.calls != 0 {
			t.Fatalf("%v: created", params)
		}
	}
}

// With no name, a create from a branch is named after it, whoever asked. The
// first session on a host used to be called impl, children included.
func TestSessionCreateWithNoNameUsesTheBranch(t *testing.T) {
	creator := &fakeCreator{}
	resp := createOnFakeCreator(t, creator, map[string]any{"repo": "o/r", "branch": "feat--t3", "agent": "codex"})
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if creator.cfg.Name != "feat--t3" {
		t.Fatalf("name %q", creator.cfg.Name)
	}
}

// session.terminate and session.restart take id or session, like the rest.
func TestLifecycleMethodsTakeIDOrSession(t *testing.T) {
	r := newLifecycleRig(t)
	wantCode(t, r.call("session.terminate", map[string]any{"id": "nope"}), CodeNotFound)
	s := r.session(domain.Session{ID: "s1", WorkingDirectory: t.TempDir()})
	if got := lifecycleResult(t, r.call("session.terminate", map[string]any{"id": s.ID})); got["session"] != "s1" {
		t.Fatalf("%v", got)
	}
	r.session(domain.Session{ID: "s2", WorkingDirectory: t.TempDir(), Name: "two"})
	got := lifecycleResult(t, r.call("session.rename", map[string]any{"session": "s2", "name": "renamed"}))
	if got["session"].(map[string]any)["name"] != "renamed" {
		t.Fatalf("%v", got)
	}
	got = lifecycleResult(t, r.call("session.rename", map[string]any{"id": "s2", "session": "ignored", "name": "again"}))
	if got["session"].(map[string]any)["name"] != "again" {
		t.Fatalf("%v", got)
	}
}

func TestUnknownMethodWordingIsWhatOldClientsMatch(t *testing.T) {
	r := newLifecycleRig(t)
	resp := r.call("no.such", nil)
	wantCode(t, resp, CodeInvalidParams)
	if resp.Error.Message != "unknown method no.such" {
		t.Fatalf("%q", resp.Error.Message)
	}
}
