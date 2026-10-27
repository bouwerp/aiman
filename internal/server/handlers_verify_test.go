package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/notify"
	"github.com/bouwerp/aiman/internal/verify"
)

// blockingCreator creates sessions, optionally holding each call until release.
type blockingCreator struct {
	release chan struct{}
	mu      sync.Mutex
	n       int
}

func (c *blockingCreator) CreateSession(ctx context.Context, cfg domain.SessionConfig) (*domain.Session, error) {
	if c.release != nil {
		select {
		case <-c.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	c.mu.Lock()
	c.n++
	id := "rev-" + string(rune('0'+c.n))
	c.mu.Unlock()
	return &domain.Session{ID: id, WorktreePath: cfg.PreparedWorktree, Status: domain.SessionStatusActive, Backend: cfg.SessionBackend}, nil
}

func gitWorktree(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	wt := filepath.Join(shortTempDir(t), "repo@feat")
	for _, args := range [][]string{
		{"init", "-b", "master", wt},
		{"-C", wt, "config", "user.email", "t@example.com"},
		{"-C", wt, "config", "user.name", "T"},
		{"-C", wt, "commit", "--allow-empty", "-m", "base"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(wt, "a.go"), []byte("package a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return wt
}

func TestVerifyStatusByTreeHash(t *testing.T) {
	srv, store := verifyServer(t, &blockingCreator{})
	if err := store.Put(verify.Review{ID: "a", WorkerID: "w1", Tree: "t1", Status: verify.StatusComplete, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// before_pr is off in verifyServer: the gate is told verify is disabled.
	var res reviewResult
	rpcOK(t, srv, "w1", "verify.status", map[string]any{"tree_hash": "t1"}, &res)
	if res.Review.Status != verify.StatusDisabled {
		t.Fatalf("disabled: %+v", res.Review)
	}

	deps := srv.VerifyDeps(store, func(string) bool { return true })
	m := verify.NewManager(verify.Config{BeforePR: true}, deps)
	t.Cleanup(m.Close)
	srv.SetVerifier(m)
	rpcOK(t, srv, "w1", "verify.status", map[string]any{"tree_hash": "t1"}, &res)
	if res.Review.ID != "a" {
		t.Fatalf("by tree: %+v", res.Review)
	}
	if resp := rpc(t, srv, "w1", "verify.status", map[string]any{"tree_hash": "other"}); resp.Error == nil || resp.Error.Code != CodeNotFound {
		t.Fatalf("other tree: %+v", resp)
	}
}

// verifyServer is a serve instance with a worker w1 on a real git worktree, a
// reviewer r1, and a verify manager over its own adapters.
func verifyServer(t *testing.T, creator *blockingCreator) (*Server, *verify.Store) {
	t.Helper()
	wt := gitWorktree(t)
	srv, _, _ := todoServer(t,
		domain.Session{ID: "w1", Name: "impl", AgentName: "claude", BaseBranch: "master", WorktreePath: wt, WorkingDirectory: wt},
		domain.Session{ID: "r1", Name: "impl-review-ab12", Role: domain.SessionRoleReviewer, WorktreePath: wt},
	)
	srv.create = creator
	store := verify.NewStore(filepath.Join(shortTempDir(t), "reviews"))
	deps := srv.VerifyDeps(store, func(c string) bool { return c == "codex" })
	deps.Poll = 10 * time.Millisecond
	m := verify.NewManager(verify.Config{Agents: []string{"claude", "codex"}}, deps)
	t.Cleanup(m.Close)
	srv.SetVerifier(m)
	return srv, store
}

type reviewResult struct {
	Type   string        `json:"type"`
	Review verify.Review `json:"review"`
}

func TestVerifyStartDefaultsToCaller(t *testing.T) {
	srv, _ := verifyServer(t, &blockingCreator{})
	var res reviewResult
	rpcOK(t, srv, "w1", "verify.start", map[string]any{}, &res)
	if res.Type != "review" || res.Review.WorkerID != "w1" || res.Review.Agent != "codex" {
		t.Fatalf("result %+v", res)
	}
}

func TestVerifyStartReviewerTarget(t *testing.T) {
	srv, _ := verifyServer(t, &blockingCreator{})
	resp := rpc(t, srv, "", "verify.start", map[string]any{"session": "r1"})
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("resp %+v", resp)
	}
}

func TestVerifyStartNoReviewer(t *testing.T) {
	srv, _ := verifyServer(t, &blockingCreator{})
	resp := rpc(t, srv, "", "verify.start", map[string]any{"session": "w1", "agent": "grok"})
	if resp.Error == nil || resp.Error.Code != CodeNoReviewer {
		t.Fatalf("resp %+v", resp)
	}
}

func TestVerifyStatusLatestForSession(t *testing.T) {
	srv, store := verifyServer(t, &blockingCreator{})
	now := time.Now()
	for _, r := range []verify.Review{
		{ID: "old", WorkerID: "w1", Status: verify.StatusComplete, StartedAt: now.Add(-time.Hour)},
		{ID: "new", WorkerID: "w1", Status: verify.StatusComplete, StartedAt: now},
	} {
		if err := store.Put(r); err != nil {
			t.Fatal(err)
		}
	}
	var res reviewResult
	rpcOK(t, srv, "", "verify.status", map[string]any{"session": "w1"}, &res)
	if res.Review.ID != "new" {
		t.Fatalf("latest %+v", res.Review)
	}
	rpcOK(t, srv, "", "verify.status", map[string]any{"review": "old", "session": "w1"}, &res)
	if res.Review.ID != "old" {
		t.Fatalf("review id did not win: %+v", res.Review)
	}
	if resp := rpc(t, srv, "", "verify.status", map[string]any{"review": "nope"}); resp.Error == nil || resp.Error.Code != CodeNotFound {
		t.Fatalf("missing review: %+v", resp)
	}
}

func TestVerifyStartReturnsQuickly(t *testing.T) {
	creator := &blockingCreator{release: make(chan struct{})}
	srv, _ := verifyServer(t, creator)
	t.Cleanup(func() { close(creator.release) }) // runs before the manager's Close
	start := time.Now()
	var res reviewResult
	rpcOK(t, srv, "w1", "verify.start", map[string]any{}, &res)
	if time.Since(start) > 5*time.Second || res.Review.Status != verify.StatusStarting {
		t.Fatalf("took %s, status %s", time.Since(start), res.Review.Status)
	}
}

func TestVerifyNotConfigured(t *testing.T) {
	srv, _, _ := todoServer(t, todoSession("s1"))
	for _, method := range []string{"verify.start", "verify.status"} {
		resp := rpc(t, srv, "s1", method, map[string]any{"session": "s1"})
		if resp.Error == nil || resp.Error.Code != CodeInvalidParams || resp.Error.Message != "verify not available" {
			t.Fatalf("%s: %+v", method, resp.Error)
		}
	}
}

type notifyStore struct {
	mu   sync.Mutex
	rows []domain.Notification
}

func (s *notifyStore) AppendIfDue(_ context.Context, n domain.Notification, _ time.Duration) (domain.Notification, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, n)
	return n, true, nil
}

func notifierServer(t *testing.T) (*Server, *notifyStore, <-chan SessionEvent) {
	t.Helper()
	srv, _, _ := todoServer(t, todoSession("s1"))
	store := &notifyStore{}
	rec := notify.NewRecorder(store, notify.Settings{Events: map[domain.NotifyKind]bool{domain.NotifyReviewDone: true}}, nil)
	srv.EnableNotifications(rec, nil)
	ch, cancel := srv.hub.Subscribe()
	t.Cleanup(cancel)
	return srv, store, ch
}

func nextVerifyEvent(t *testing.T, ch <-chan SessionEvent) SessionEvent {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == "verify" {
				return ev
			}
		case <-deadline:
			t.Fatal("no verify event")
		}
	}
}

func TestServeNotifierRequiredFindingsNotify(t *testing.T) {
	srv, store, ch := notifierServer(t)
	w := domain.Session{ID: "s1", Name: "impl"}
	serveNotifier{srv}.ReviewFinished(context.Background(), w, verify.Review{Status: verify.StatusComplete, Required: 1, Suggestions: 2})
	if len(store.rows) != 1 || store.rows[0].Kind != domain.NotifyReviewDone {
		t.Fatalf("notifications %+v", store.rows)
	}
	if ev := nextVerifyEvent(t, ch); ev.ID != "s1" || ev.State != string(verify.StatusComplete) {
		t.Fatalf("event %+v", ev)
	}
}

func TestServeNotifierCleanPassIsQuiet(t *testing.T) {
	srv, store, ch := notifierServer(t)
	serveNotifier{srv}.ReviewFinished(context.Background(), domain.Session{ID: "s1"}, verify.Review{Status: verify.StatusComplete})
	if len(store.rows) != 0 {
		t.Fatalf("a clean pass notified: %+v", store.rows)
	}
	nextVerifyEvent(t, ch)
}

func TestUnprotectedWhenNoHook(t *testing.T) {
	srv, store := verifyServer(t, &blockingCreator{})
	get := func(id string) SessionInfo {
		t.Helper()
		var res struct {
			Session SessionInfo `json:"session"`
		}
		rpcOK(t, srv, "", "session.get", map[string]any{"id": id}, &res)
		return res.Session
	}
	// before_pr off: nothing is unprotected.
	if get("w1").Unprotected {
		t.Fatal("unprotected with before_pr off")
	}
	m := verify.NewManager(verify.Config{BeforePR: true}, srv.VerifyDeps(store, func(string) bool { return true }))
	t.Cleanup(m.Close)
	srv.SetVerifier(m)
	if get("w1").Unprotected {
		t.Fatal("a Claude session has the PreToolUse guard")
	}
	codex := domain.Session{ID: "c1", Name: "cx", Group: "g", AgentName: "codex", Status: domain.SessionStatusActive}
	if err := srv.repo.Save(context.Background(), &codex); err != nil {
		t.Fatal(err)
	}
	if !get("c1").Unprotected {
		t.Fatal("a codex session has no guard and must be unprotected")
	}
}
