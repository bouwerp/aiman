package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/contextstore"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/local"
	"github.com/bouwerp/aiman/internal/todo"
)

func paneText(s string) string { return s }

func handoverCallSession(t *testing.T, dir string) string {
	t.Helper()
	repo := testRepo(t)
	if err := repo.Save(context.Background(), &domain.Session{
		ID: "s1", Name: "s1", Group: "g", WorkingDirectory: dir,
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return startTestServer(t, repo)
}

func resultMap(t *testing.T, resp Response) map[string]any {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("rpc: %v", resp.Error)
	}
	m, ok := resp.Result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v", resp.Result)
	}
	return m
}

func todosWith(id, text, state string) todoStore {
	return &stubTodos{list: domain.TodoList{
		Rev:   1,
		Items: []domain.TodoItem{{ID: id, Text: text, State: domain.TodoState(state)}},
	}}
}

func pending(text string) string { return text }

type stubTodos struct {
	list domain.TodoList
	err  error
}

func (s *stubTodos) Get(string) (domain.TodoList, error) { return s.list, s.err }
func (s *stubTodos) Mutate(string, domain.TodoAuthor, func(*domain.TodoList) error) (domain.TodoList, todo.Change, error) {
	return domain.TodoList{}, todo.Change{}, nil
}
func (s *stubTodos) Mirror(string, []domain.TodoSnapshotItem, time.Time) (domain.TodoList, todo.Change, error) {
	return domain.TodoList{}, todo.Change{}, nil
}

type countingContext struct {
	domain.ContextStore
	puts int
}

func (c *countingContext) Put(ctx context.Context, e domain.ContextEntry) (domain.ContextEntry, error) {
	c.puts++
	return c.ContextStore.Put(ctx, e)
}

// errPane fails pane capture. Execute stays the embedded remote's empty result.
type errPane struct{ *fakeRemote }

func (errPane) CaptureTmuxPane(context.Context, string) (string, error) {
	return "", errors.New("capture failed")
}

func newHandoverTestServer(t *testing.T, pane string, todos todoStore, note string) *Server {
	t.Helper()
	srv := &Server{
		remote:   &fakeRemote{pane: pane},
		ctxStore: contextstore.NewFiles(t.TempDir()),
		todos:    todos,
	}
	if note == "" {
		return srv
	}
	st := newSteerer(&fakeSteerStore{}, &fakeSteerTarget{}, nil, time.Millisecond)
	srv.steer = st
	st.Enqueue(SteerNote{SessionID: "s1", Author: domain.AuthorAiman, Text: note})
	waitQueued(t, st, "s1", 1)
	return srv
}

func TestWriteHandoverWritesFileAndNote(t *testing.T) {
	dir := t.TempDir()
	srv := newHandoverTestServer(t, paneText("$ go test\nFAIL parser"), todosWith("t2", "Fix parser", "active"), pending("[aiman] note A"))
	sess := domain.Session{
		ID: "s1", Name: "WTB-12", WorkingDirectory: dir, TmuxSession: "wtb", AgentTitle: "Fixing parser",
	}
	res, err := srv.writeHandover(context.Background(), sess, "restart")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !res.Written {
		t.Fatal("Written is false on the first write")
	}
	wantPath := filepath.Join(dir, domain.AimanHandoverFileName)
	if res.Path != wantPath {
		t.Fatalf("path %q, want %q", res.Path, wantPath)
	}
	if res.NoteID != "handover-s1" {
		t.Fatalf("note id %q", res.NoteID)
	}
	body, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"t2 ▸ Fix parser", "[aiman] note A", "FAIL parser", "## Goal", "## Evidence", "## Verification"} {
		if !strings.Contains(text, want) {
			t.Errorf("file missing %q\n%s", want, text)
		}
	}
	note, err := srv.ctxStore.Get(context.Background(), "handover-s1")
	if err != nil {
		t.Fatalf("note: %v", err)
	}
	if note.Title != "Handover: WTB-12" {
		t.Errorf("title %q", note.Title)
	}
	if note.Type != domain.ContextTypeHandover {
		t.Errorf("type %q, want handover", note.Type)
	}
	if note.Abstract != "Fix parser" {
		t.Errorf("abstract %q", note.Abstract)
	}
	if note.Namespace != domain.ContextNSHost || note.Key != "host" {
		t.Errorf("namespace %s/%s, want host/host", note.Namespace, note.Key)
	}
	if note.SessionID != "s1" {
		t.Errorf("session %q", note.SessionID)
	}
	if strings.Contains(note.Body, "DO NOT COMMIT") {
		t.Error("note body carries the file scaffold header")
	}
}

func TestWriteHandoverUnchangedIsNotRewritten(t *testing.T) {
	dir := t.TempDir()
	store := &countingContext{ContextStore: contextstore.NewFiles(t.TempDir())}
	srv := newHandoverTestServer(t, paneText("idle"), todosWith("t2", "Fix parser", "active"), pending("[aiman] note A"))
	srv.ctxStore = store
	sess := domain.Session{ID: "s1", Name: "WTB-12", WorkingDirectory: dir, TmuxSession: "wtb", AgentTitle: "Fixing parser"}
	res, err := srv.writeHandover(context.Background(), sess, "restart")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Written || store.puts != 1 {
		t.Fatalf("first written=%v puts=%d", res.Written, store.puts)
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(res.Path, past, past); err != nil {
		t.Fatal(err)
	}
	res, err = srv.writeHandover(context.Background(), sess, "restart")
	if err != nil {
		t.Fatal(err)
	}
	if res.Written {
		t.Fatal("second write reported a content change")
	}
	if store.puts != 1 {
		t.Fatalf("note Put called again: puts=%d", store.puts)
	}
	info, err := os.Stat(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(past) {
		t.Fatalf("mtime changed to %v", info.ModTime())
	}
}

func TestWriteHandoverPutsMissingNoteWhenFileUnchanged(t *testing.T) {
	dir := t.TempDir()
	srv := newHandoverTestServer(t, paneText("idle"), nil, "")
	sess := domain.Session{ID: "s1", Name: "WTB-12", WorkingDirectory: dir}
	if _, err := srv.writeHandover(context.Background(), sess, "restart"); err != nil {
		t.Fatal(err)
	}
	store := &countingContext{ContextStore: contextstore.NewFiles(t.TempDir())}
	srv.ctxStore = store
	res, err := srv.writeHandover(context.Background(), sess, "restart")
	if err != nil {
		t.Fatal(err)
	}
	if res.Written {
		t.Fatal("unchanged file reported written")
	}
	if store.puts != 1 {
		t.Fatalf("puts=%d, want the missing note written once", store.puts)
	}
	if _, err := store.Get(context.Background(), "handover-s1"); err != nil {
		t.Fatalf("note: %v", err)
	}
}

func TestWriteHandoverNoteNamespace(t *testing.T) {
	cases := []struct {
		name    string
		sess    domain.Session
		ns, key string
	}{
		{name: "group", sess: domain.Session{ID: "s1", Name: "N", Group: "WTB", RepoName: "acme/widgets"}, ns: domain.ContextNSGroup, key: "WTB"},
		{name: "issue", sess: domain.Session{ID: "s1", Name: "N", IssueKey: "WTB-9", RepoName: "acme/widgets"}, ns: domain.ContextNSGroup, key: "WTB-9"},
		{name: "repo", sess: domain.Session{ID: "s1", Name: "N", RepoName: "acme/widgets"}, ns: domain.ContextNSRepo, key: "acme__widgets"},
		{name: "host", sess: domain.Session{ID: "s1", Name: "N"}, ns: domain.ContextNSHost, key: "host"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			srv := newHandoverTestServer(t, "", nil, "")
			tc.sess.WorkingDirectory = dir
			if _, err := srv.writeHandover(context.Background(), tc.sess, "forget"); err != nil {
				t.Fatal(err)
			}
			note, err := srv.ctxStore.Get(context.Background(), "handover-s1")
			if err != nil {
				t.Fatal(err)
			}
			if note.Namespace != tc.ns || note.Key != tc.key {
				t.Fatalf("namespace %s/%s, want %s/%s", note.Namespace, note.Key, tc.ns, tc.key)
			}
		})
	}
}

func TestWriteHandoverLabel(t *testing.T) {
	cases := []struct {
		sess  domain.Session
		title string
	}{
		{domain.Session{ID: "s1", Name: "WTB-12", IssueKey: "OTHER-1"}, "Handover: WTB-12"},
		{domain.Session{ID: "s1", IssueKey: "PROJ-1"}, "Handover: PROJ-1"},
		{domain.Session{ID: "s1"}, "Handover: s1"},
	}
	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			dir := t.TempDir()
			srv := newHandoverTestServer(t, "", nil, "")
			tc.sess.WorkingDirectory = dir
			if _, err := srv.writeHandover(context.Background(), tc.sess, "archive"); err != nil {
				t.Fatal(err)
			}
			note, err := srv.ctxStore.Get(context.Background(), "handover-s1")
			if err != nil {
				t.Fatal(err)
			}
			if note.Title != tc.title {
				t.Fatalf("title %q, want %q", note.Title, tc.title)
			}
		})
	}
}

func TestWriteHandoverNoWorkingDirectory(t *testing.T) {
	srv := newHandoverTestServer(t, "", nil, "")
	_, err := srv.writeHandover(context.Background(), domain.Session{ID: "s1", Name: "WTB-12"}, "restart")
	if err == nil || err.Error() != "session has no working directory" {
		t.Fatalf("err %v", err)
	}
}

func TestWriteHandoverUsesWorktreePath(t *testing.T) {
	dir := t.TempDir()
	srv := newHandoverTestServer(t, paneText("from worktree"), nil, "")
	res, err := srv.writeHandover(context.Background(), domain.Session{ID: "s1", Name: "n", WorktreePath: dir}, "restart")
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != filepath.Join(dir, domain.AimanHandoverFileName) {
		t.Fatalf("path %q", res.Path)
	}
	body, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "from worktree") {
		t.Fatalf("body:\n%s", body)
	}
}

func TestWriteHandoverRevZeroIsNoList(t *testing.T) {
	dir := t.TempDir()
	srv := newHandoverTestServer(t, "", &stubTodos{list: domain.TodoList{
		Rev:   0,
		Items: []domain.TodoItem{{ID: "t2", Text: "Fix parser", State: domain.TodoActive}},
	}}, "")
	res, err := srv.writeHandover(context.Background(), domain.Session{ID: "s1", Name: "n", WorkingDirectory: dir}, "restart")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "No to-do list.") || strings.Contains(string(body), "Fix parser") {
		t.Fatalf("rev 0 list leaked:\n%s", body)
	}
}

func TestWriteHandoverNilDeps(t *testing.T) {
	dir := t.TempDir()
	srv := &Server{remote: &fakeRemote{pane: "still here"}}
	res, err := srv.writeHandover(context.Background(), domain.Session{
		ID: "s1", Name: "n", WorkingDirectory: dir, Backend: domain.BackendPTY,
	}, "archive")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Written || res.NoteID != "handover-s1" {
		t.Fatalf("result %+v", res)
	}
	body, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "still here") || !strings.Contains(text, "No to-do list.") {
		t.Fatalf("body:\n%s", text)
	}
	if strings.Contains(text, "## Undelivered notes") {
		t.Fatalf("undelivered section with a nil steerer:\n%s", text)
	}
}

func TestWriteHandoverCaptureErrorIsEmptyTail(t *testing.T) {
	dir := t.TempDir()
	srv := newHandoverTestServer(t, "SECRET", nil, "")
	srv.remote = errPane{fakeRemote: srv.remote.(*fakeRemote)}
	res, err := srv.writeHandover(context.Background(), domain.Session{ID: "s1", Name: "n", WorkingDirectory: dir, TmuxSession: "wtb"}, "restart")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Written {
		t.Fatal("capture error failed the write")
	}
	body, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "SECRET") {
		t.Fatalf("capture error leaked the pane:\n%s", body)
	}
}

func TestWriteHandoverNotePutErrorIsNotReturned(t *testing.T) {
	dir := t.TempDir()
	srv := newHandoverTestServer(t, "", nil, "")
	srv.ctxStore = putErrStore{}
	res, err := srv.writeHandover(context.Background(), domain.Session{ID: "s1", Name: "n", WorkingDirectory: dir}, "restart")
	if err != nil {
		t.Fatalf("note error returned: %v", err)
	}
	if !res.Written {
		t.Fatal("file was not written")
	}
}

type putErrStore struct{}

func (putErrStore) Put(context.Context, domain.ContextEntry) (domain.ContextEntry, error) {
	return domain.ContextEntry{}, errors.New("disk full")
}
func (putErrStore) Get(context.Context, string) (*domain.ContextEntry, error) {
	return nil, errors.New("missing")
}
func (putErrStore) List(context.Context, domain.ContextQuery) ([]domain.ContextEntry, error) {
	return nil, nil
}
func (putErrStore) Find(context.Context, domain.ContextQuery) ([]domain.ContextEntry, error) {
	return nil, nil
}
func (putErrStore) Pack(context.Context, domain.ContextQuery) (string, error) { return "", nil }
func (putErrStore) Stats(context.Context) (domain.ContextStats, error) {
	return domain.ContextStats{}, nil
}

func TestWriteHandoverEmptyBaseLabel(t *testing.T) {
	dir := t.TempDir()
	srv := newHandoverTestServer(t, "", nil, "")
	res, err := srv.writeHandover(context.Background(), domain.Session{ID: "s1", Name: "n", WorkingDirectory: dir}, "restart")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, "(base origin/HEAD)") || !strings.Contains(text, "(unavailable)") {
		t.Fatalf("base label:\n%s", text)
	}
}

func TestDiffStatNonGitDir(t *testing.T) {
	dir := t.TempDir()
	got := diffStat(context.Background(), localExecutor(t, dir), dir, "main")
	if got != "" {
		t.Fatalf("diff stat %q", got)
	}
}

func TestDiffStatAgainstBase(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	gitCmd(t, dir, "init", "-b", "main")
	gitCmd(t, dir, "config", "user.email", "aiman@example.com")
	gitCmd(t, dir, "config", "user.name", "Aiman Test")
	name := "parser.go"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, dir, "add", name)
	gitCmd(t, dir, "commit", "-m", "seed")
	gitCmd(t, dir, "checkout", "-b", "fix")
	if err := os.WriteFile(filepath.Join(dir, name), []byte("package p\n\nfunc F() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := diffStat(context.Background(), localExecutor(t, dir), dir, "main")
	if !strings.Contains(got, name) {
		t.Fatalf("diff stat %q", got)
	}
}

func localExecutor(t *testing.T, dir string) interface {
	Execute(context.Context, string) (string, error)
} {
	t.Helper()
	return local.NewExecutor(dir)
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func TestPaneTailKeepsLast40Lines(t *testing.T) {
	var lines []string
	for i := 1; i <= 100; i++ {
		lines = append(lines, fmt.Sprintf("L%03d", i))
	}
	got := paneTail(strings.Join(lines, "\n") + "\n\n")
	parts := strings.Split(got, "\n")
	if len(parts) != 40 {
		t.Fatalf("len %d\n%s", len(parts), got)
	}
	if parts[0] != "L061" || parts[len(parts)-1] != "L100" {
		t.Fatalf("window %q .. %q", parts[0], parts[len(parts)-1])
	}
}

func TestHandoverWriteMethod(t *testing.T) {
	dir := t.TempDir()
	sock := handoverCallSession(t, dir)
	resp, err := Call(sock, "handover.write", map[string]any{"session": "s1", "reason": "archive"})
	if err != nil {
		t.Fatal(err)
	}
	m := resultMap(t, resp)
	wantPath := filepath.Join(dir, domain.AimanHandoverFileName)
	if m["type"] != "handover" || m["written"] != true || m["exists"] != true || m["note_id"] != "handover-s1" || m["path"] != wantPath {
		t.Fatalf("result = %#v", m)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatal(err)
	}
}

func TestHandoverWriteRejectsUnknownReason(t *testing.T) {
	sock := handoverCallSession(t, t.TempDir())
	for _, reason := range []string{"nap", ""} {
		resp, err := Call(sock, "handover.write", map[string]any{"session": "s1", "reason": reason})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Error == nil || resp.Error.Code != CodeInvalidParams || !strings.Contains(resp.Error.Message, "reason") || strings.Contains(resp.Error.Message, "unknown method") {
			t.Fatalf("reason %q: %+v", reason, resp.Error)
		}
	}
}

// A missing session is not_found and must not wait out the handover budget.
func TestHandoverWriteUnknownSession(t *testing.T) {
	sock := startTestServer(t, testRepo(t))
	start := time.Now()
	resp, err := Call(sock, "handover.write", map[string]any{"session": "missing", "reason": "archive"})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) >= 2*time.Second {
		t.Fatalf("unknown session took %s", time.Since(start))
	}
	if resp.Error == nil || resp.Error.Code != CodeNotFound {
		t.Fatalf("got %+v, want %s", resp.Error, CodeNotFound)
	}
}

func TestHandoverWriteNoWorkingDirectory(t *testing.T) {
	sock := handoverCallSession(t, "")
	resp, err := Call(sock, "handover.write", map[string]any{"session": "s1", "reason": "restart"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams || !strings.Contains(resp.Error.Message, "session has no working directory") {
		t.Fatalf("got %+v", resp.Error)
	}
}

func TestPreCompactReportWritesHandover(t *testing.T) {
	dir := t.TempDir()
	sock := handoverCallSession(t, dir)
	resp, err := Call(sock, "session.report_agent_session", map[string]any{
		"id": "s1", "agent_session_id": "c1", "event": "PreCompact",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("report: %+v", resp.Error)
	}
	if _, err := os.Stat(filepath.Join(dir, domain.AimanHandoverFileName)); err != nil {
		t.Fatal(err)
	}
}

// ctxBlockedRemote's Execute waits until ctx ends. A diff with no deadline
// never returns.
type ctxBlockedRemote struct{ *fakeRemote }

func (ctxBlockedRemote) Execute(ctx context.Context, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func TestPreCompactReportReturnsWhileDiffIsBlocked(t *testing.T) {
	if preCompactHandoverTimeout != 20*time.Second {
		t.Fatalf("production budget %s, want 20s", preCompactHandoverTimeout)
	}
	preCompactHandoverTimeout = 50 * time.Millisecond
	t.Cleanup(func() { preCompactHandoverTimeout = 20 * time.Second })

	dir := t.TempDir()
	repo := testRepo(t)
	if err := repo.Save(context.Background(), &domain.Session{
		ID: "s1", Name: "s1", Group: "g", WorkingDirectory: dir, TmuxSession: "s1",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	srv := New(nil, repo, ctxBlockedRemote{fakeRemote: &fakeRemote{}}, nil, nil, nil, "test")
	start := time.Now()
	done := make(chan Response, 1)
	go func() {
		done <- srv.handleReportAgentSession(context.Background(), Request{
			ID: "1", Method: "session.report_agent_session",
			Params: []byte(`{"id":"s1","agent_session_id":"c1","event":"PreCompact"}`),
		})
	}()
	select {
	case resp := <-done:
		if time.Since(start) >= 2*time.Second {
			t.Fatalf("report took %s, want well under 20s", time.Since(start))
		}
		if resp.Error != nil {
			t.Fatalf("report: %v", resp.Error)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("report still blocked; PreCompact handover has no deadline")
	}
	if _, err := os.Stat(filepath.Join(dir, domain.AimanHandoverFileName)); err != nil {
		t.Fatal(err)
	}
}

// diffBlocksRemote's Execute waits until ctx is done. CaptureTmuxPane fails
// once that context is already cancelled, which is what a real tmux capture
// does when git has used up the deadline.
type diffBlocksRemote struct{ *fakeRemote }

func (diffBlocksRemote) Execute(ctx context.Context, _ string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func (r diffBlocksRemote) CaptureTmuxPane(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return r.fakeRemote.CaptureTmuxPane(ctx, name)
}

func TestHandoverKeepsTmuxPaneWhenDiffBlocks(t *testing.T) {
	dir := t.TempDir()
	const pane = "still on the last screen"
	srv := &Server{remote: diffBlocksRemote{fakeRemote: &fakeRemote{pane: pane}}}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	res, err := srv.writeHandover(ctx, domain.Session{
		ID: "s1", Name: "s1", WorkingDirectory: dir, TmuxSession: "s1", Backend: domain.BackendTmux,
	}, "pre_compact")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !res.Written {
		t.Fatal("blocked diff must still be a successful write")
	}
	body, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, pane) {
		t.Fatalf("tmux pane missing after diff blocked until ctx was done:\n%s", text)
	}
	if !strings.Contains(text, "(unavailable)") {
		t.Fatalf("cancelled diff must stay an empty stat:\n%s", text)
	}
	screen := strings.Index(text, "## Evidence")
	if screen < 0 || strings.Index(text, pane) < screen {
		t.Fatalf("pane is not in the evidence section:\n%s", text)
	}
}

func TestSessionEndReportDoesNotWriteHandover(t *testing.T) {
	dir := t.TempDir()
	sock := handoverCallSession(t, dir)
	resp, err := Call(sock, "session.report_agent_session", map[string]any{
		"id": "s1", "ended": true, "event": "SessionEnd",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("report: %+v", resp.Error)
	}
	if _, err := os.Stat(filepath.Join(dir, domain.AimanHandoverFileName)); !os.IsNotExist(err) {
		t.Fatalf("handover file err = %v, want not exist", err)
	}
}

func TestHandoverPointQueuesOnlyWhenFileExists(t *testing.T) {
	dir := t.TempDir()
	srv, _, _ := todoServer(t, domain.Session{
		ID: "s1", Name: "s1", Group: "g", WorkingDirectory: dir, TmuxSession: "s1", Backend: domain.BackendTmux,
	})
	var absent struct {
		Type   string `json:"type"`
		Queued bool   `json:"queued"`
	}
	rpcOK(t, srv, "", "handover.point", map[string]any{"session": "s1"}, &absent)
	if absent.Type != "handover_point" || absent.Queued {
		t.Fatalf("absent file: %+v", absent)
	}
	// Enqueue coalesces before Status.Pending shows the note. Wait that window
	// out so a pointer queued with no file cannot hide in the buffer.
	settle()
	if st := srv.steer.Status("s1"); len(st.Pending) != 0 || st.Queued != 0 {
		t.Fatalf("enqueued without a file: %+v", st)
	}

	path := filepath.Join(dir, domain.AimanHandoverFileName)
	if err := os.WriteFile(path, []byte("handover\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var present struct {
		Type   string `json:"type"`
		Queued bool   `json:"queued"`
	}
	rpcOK(t, srv, "", "handover.point", map[string]any{"session": "s1"}, &present)
	if present.Type != "handover_point" || !present.Queued {
		t.Fatalf("file present: %+v", present)
	}
	eventually(t, 2*time.Second, func() bool { return len(srv.steer.Status("s1").Pending) == 1 })
	note := srv.steer.Status("s1").Pending[0]
	if note.Text != HandoverRestartNote || note.Author != domain.AuthorAiman {
		t.Fatalf("note = %+v", note)
	}
}

func TestHandoverPointPreCompactUsesCompactionNote(t *testing.T) {
	dir := t.TempDir()
	srv, _, _ := todoServer(t, domain.Session{
		ID: "s1", Name: "s1", Group: "g", WorkingDirectory: dir, TmuxSession: "s1", Backend: domain.BackendTmux,
	})
	path := filepath.Join(dir, domain.AimanHandoverFileName)
	if err := os.WriteFile(path, []byte("handover\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var res struct {
		Type   string `json:"type"`
		Queued bool   `json:"queued"`
	}
	rpcOK(t, srv, "", "handover.point", map[string]any{"session": "s1", "reason": "pre_compact"}, &res)
	if res.Type != "handover_point" || !res.Queued {
		t.Fatalf("result = %+v", res)
	}
	eventually(t, 2*time.Second, func() bool { return len(srv.steer.Status("s1").Pending) == 1 })
	note := srv.steer.Status("s1").Pending[0]
	if note.Text != HandoverCompactionNote || note.Author != domain.AuthorAiman {
		t.Fatalf("note = %+v", note)
	}
}

func TestHandoverPointArchiveQueuesNothing(t *testing.T) {
	dir := t.TempDir()
	srv, _, _ := todoServer(t, domain.Session{
		ID: "s1", Name: "s1", Group: "g", WorkingDirectory: dir, TmuxSession: "s1", Backend: domain.BackendTmux,
	})
	path := filepath.Join(dir, domain.AimanHandoverFileName)
	if err := os.WriteFile(path, []byte("handover\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var res struct {
		Type   string `json:"type"`
		Queued bool   `json:"queued"`
	}
	rpcOK(t, srv, "", "handover.point", map[string]any{"session": "s1", "reason": "archive"}, &res)
	if res.Type != "handover_point" || res.Queued {
		t.Fatalf("result = %+v", res)
	}
	settle()
	if st := srv.steer.Status("s1"); len(st.Pending) != 0 || st.Queued != 0 {
		t.Fatalf("archive queued a pointer: %+v", st)
	}
}

func TestPreCompactReportQueuesCompactionPointer(t *testing.T) {
	dir := t.TempDir()
	srv, _, _ := todoServer(t, domain.Session{
		ID: "s1", Name: "s1", Group: "g", WorkingDirectory: dir, TmuxSession: "s1", Backend: domain.BackendTmux,
	})
	resp := srv.handleReportAgentSession(context.Background(), Request{
		ID: "1", Method: "session.report_agent_session",
		Params: []byte(`{"id":"s1","agent_session_id":"c1","event":"PreCompact"}`),
	})
	if resp.Error != nil {
		t.Fatalf("report: %v", resp.Error)
	}
	if _, err := os.Stat(filepath.Join(dir, domain.AimanHandoverFileName)); err != nil {
		t.Fatal(err)
	}
	eventually(t, 2*time.Second, func() bool { return len(srv.steer.Status("s1").Pending) == 1 })
	note := srv.steer.Status("s1").Pending[0]
	if note.Text != HandoverCompactionNote || note.Author != domain.AuthorAiman {
		t.Fatalf("note = %+v", note)
	}
}
