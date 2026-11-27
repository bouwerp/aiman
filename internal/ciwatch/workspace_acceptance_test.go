package ciwatch

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
)

// Acceptance tests for multi-repo sessions, piece P6: the PR watcher watches
// one PR per member of a workspace session, running gh in each member's
// worktree, and its to-do refs carry the member's slug. Children must not
// edit this file.

const (
	wsDir  = "/r/host-tab@ws"
	wsDirA = "/r/host-tab@ws/a"
	wsDirB = "/r/host-tab@ws/b"
)

// wsGH answers per directory, as gh does when it resolves the repository and
// branch from the directory it runs in. A directory with no entry has no PR.
type wsGH struct {
	mu       sync.Mutex
	prs      map[string]PRView
	prErrs   map[string]error
	comments map[string][]ReviewComment
	viewed   []string
	logDirs  []string
}

func (g *wsGH) PRView(_ context.Context, dir string) (PRView, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.viewed = append(g.viewed, dir)
	if err := g.prErrs[dir]; err != nil {
		return PRView{}, err
	}
	pr, ok := g.prs[dir]
	if !ok {
		return PRView{}, ErrNoPR
	}
	return pr, nil
}

func (g *wsGH) FailedJobLog(_ context.Context, dir string, _ int64) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.logDirs = append(g.logDirs, dir)
	return "log of " + dir, nil
}

func (g *wsGH) ReviewComments(_ context.Context, dir string, _ int) ([]ReviewComment, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]ReviewComment(nil), g.comments[dir]...), nil
}

func (g *wsGH) ViewerLogin(context.Context) (string, error) { return "me", nil }

func (g *wsGH) dirs() (viewed, logs []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.viewed...), append([]string(nil), g.logDirs...)
}

type wsRig struct {
	w      *Watcher
	gh     *wsGH
	todos  *fakeTodos
	notify *fakeNotifier
	steers *steerRecorder
	store  *sqlite.Repository
	now    time.Time
}

func newWSRig(t *testing.T) *wsRig {
	t.Helper()
	repo, err := sqlite.NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	r := &wsRig{
		gh:     &wsGH{prs: map[string]PRView{}, prErrs: map[string]error{}, comments: map[string][]ReviewComment{}},
		todos:  newFakeTodos(),
		notify: &fakeNotifier{},
		steers: &steerRecorder{},
		store:  repo,
		now:    time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC),
	}
	r.w = New(Deps{
		Sessions: fakeSessions{},
		GitHub:   r.gh,
		Todos:    r.todos,
		Steer:    r.steers.steer,
		Notify:   r.notify,
		State:    repo,
		Now:      func() time.Time { return r.now },
	})
	return r
}

// wsSession is a workspace session: its worktree is the workspace directory,
// which is not a git repository, and it has two members.
func wsSession() domain.Session {
	return domain.Session{
		ID: "ws1", Name: "host-tab", Branch: "host-tab", WorktreePath: wsDir, WorkingDirectory: wsDir,
		Mode: domain.SessionModeAutonomous, Status: domain.SessionStatusActive,
		CreatedAt: time.Date(2026, 11, 27, 11, 0, 0, 0, time.UTC),
		Members: []domain.SessionRepo{
			{Repo: "o/a", Branch: "host-tab", BaseBranch: "main", WorktreePath: wsDirA, Ord: 0},
			{Repo: "o/b", Branch: "host-tab", BaseBranch: "develop", WorktreePath: wsDirB, Ord: 1},
		},
	}
}

func (r *wsRig) poll(t *testing.T, s domain.Session) {
	t.Helper()
	if err := r.w.PollSession(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}

func (r *wsRig) items(id string) []domain.TodoItem {
	l, _ := r.todos.Get(id)
	return l.Items
}

func itemByRef(items []domain.TodoItem, ref string) (domain.TodoItem, bool) {
	for _, it := range items {
		if it.Ref == ref {
			return it, true
		}
	}
	return domain.TodoItem{}, false
}

func failing(number int, sha string, names ...string) PRView {
	pr := PRView{Number: number, HeadSHA: sha, State: "OPEN"}
	for i, n := range names {
		pr.Checks = append(pr.Checks, Check{Name: n, Conclusion: ConclusionFailure, DetailsURL: "https://example.com/" + n, JobID: int64(100 + i)})
	}
	return pr
}

func TestWatchWorkspaceLooksUpPRPerMember(t *testing.T) {
	r := newWSRig(t)
	r.gh.prs[wsDirA] = PRView{Number: 11, HeadSHA: "aaaaaaa1", State: "OPEN"}
	r.gh.prs[wsDirB] = PRView{Number: 22, HeadSHA: "bbbbbbb1", State: "OPEN"}
	r.poll(t, wsSession())

	viewed, _ := r.gh.dirs()
	sort.Strings(viewed)
	if strings.Join(viewed, ",") != wsDirA+","+wsDirB {
		t.Fatalf("gh ran in %v, want once in each member worktree and never in the workspace directory", viewed)
	}
	ctx := context.Background()
	a, err := r.store.LoadPRWatchRepo(ctx, "ws1", "o/a")
	if err != nil || a.PRNumber != 11 || a.HeadSHA != "aaaaaaa1" || a.PRState != "OPEN" {
		t.Fatalf("o/a state = %+v, %v", a, err)
	}
	b, err := r.store.LoadPRWatchRepo(ctx, "ws1", "o/b")
	if err != nil || b.PRNumber != 22 || b.HeadSHA != "bbbbbbb1" {
		t.Fatalf("o/b state = %+v, %v", b, err)
	}
	if plain, _ := r.store.LoadPRWatch(ctx, "ws1"); plain.PRNumber != 0 {
		t.Fatalf("workspace session wrote a repo-less PR: %+v", plain)
	}
}

func TestWatchWorkspaceCheckRefsCarryRepo(t *testing.T) {
	r := newWSRig(t)
	r.gh.prs[wsDirA] = PRView{Number: 11, HeadSHA: "aaaaaaa1", State: "OPEN"}
	r.gh.prs[wsDirB] = failing(22, "bbbbbbb1", "build")
	r.poll(t, wsSession())

	items := r.items("ws1")
	if len(items) != 1 {
		t.Fatalf("todos %+v, want one", items)
	}
	if items[0].Ref != "check:o/b:build" || items[0].Text != "Fix CI: build" || items[0].State != domain.TodoPending {
		t.Fatalf("todo %+v, want Fix CI: build with ref check:o/b:build", items[0])
	}
	_, logs := r.gh.dirs()
	if len(logs) != 1 || logs[0] != wsDirB {
		t.Fatalf("failed job log read in %v, want %s", logs, wsDirB)
	}
	notes := r.steers.all()
	if len(notes) != 1 || !strings.Contains(notes[0], "PR #22") {
		t.Fatalf("notes %q, want one run note naming PR #22", notes)
	}
}

func TestWatchWorkspaceSameCheckTwoMembers(t *testing.T) {
	r := newWSRig(t)
	r.gh.prs[wsDirA] = failing(11, "aaaaaaa1", "build")
	r.gh.prs[wsDirB] = failing(22, "bbbbbbb1", "build")
	r.poll(t, wsSession())

	items := r.items("ws1")
	a, okA := itemByRef(items, "check:o/a:build")
	b, okB := itemByRef(items, "check:o/b:build")
	if len(items) != 2 || !okA || !okB || a.ID == b.ID {
		t.Fatalf("todos %+v, want one per member", items)
	}

	// A second poll at the same heads adds nothing.
	r.poll(t, wsSession())
	if got := r.items("ws1"); len(got) != 2 {
		t.Fatalf("todos after repoll %+v", got)
	}

	// build passing on o/a closes only o/a's to-do.
	r.gh.prs[wsDirA] = PRView{Number: 11, HeadSHA: "aaaaaaa2", State: "OPEN", Checks: []Check{{Name: "build", Conclusion: ConclusionSuccess}}}
	r.poll(t, wsSession())
	items = r.items("ws1")
	a, _ = itemByRef(items, "check:o/a:build")
	b, _ = itemByRef(items, "check:o/b:build")
	if a.State != domain.TodoDone || b.State != domain.TodoPending {
		t.Fatalf("after o/a passed: o/a %q, o/b %q; want done, pending", a.State, b.State)
	}
}

func TestWatchWorkspaceFailuresCountPerMember(t *testing.T) {
	r := newWSRig(t)
	s := wsSession()
	// o/a fails build on MaxFixAttempts+1 distinct SHAs; o/b fails it once.
	r.gh.prs[wsDirB] = failing(22, "bbbbbbb1", "build")
	for i := 0; i <= MaxFixAttempts; i++ {
		r.gh.prs[wsDirA] = failing(11, strings.Repeat(string(rune('c'+i)), 8), "build")
		r.poll(t, s)
		for _, it := range r.items("ws1") {
			if it.Ref == "check:o/a:build" && it.State == domain.TodoPending {
				r.todos.setState("ws1", it.ID, domain.TodoDone)
			}
		}
	}
	r.notify.mu.Lock()
	sent := append([]emitted(nil), r.notify.sent...)
	r.notify.mu.Unlock()
	if len(sent) != 1 || sent[0].kind != domain.NotifyCIEscalation {
		t.Fatalf("notifications %+v, want one escalation for o/a", sent)
	}
	ctx := context.Background()
	if a, _ := r.store.LoadPRWatchRepo(ctx, "ws1", "o/a"); len(a.Escalated) != 1 || a.Escalated[0] != "build" {
		t.Fatalf("o/a escalated = %v", a.Escalated)
	}
	if b, _ := r.store.LoadPRWatchRepo(ctx, "ws1", "o/b"); len(b.Escalated) != 0 {
		t.Fatalf("o/b escalated = %v, want none", b.Escalated)
	}
	if b, ok := itemByRef(r.items("ws1"), "check:o/b:build"); !ok || b.State != domain.TodoPending {
		t.Fatalf("o/b to-do %+v, want still pending", b)
	}
}

func TestWatchWorkspaceReviewRefsCarryRepo(t *testing.T) {
	r := newWSRig(t)
	r.gh.prs[wsDirA] = PRView{Number: 11, HeadSHA: "aaaaaaa1", State: "OPEN"}
	r.gh.prs[wsDirB] = PRView{Number: 22, HeadSHA: "bbbbbbb1", State: "OPEN"}
	r.gh.comments[wsDirA] = []ReviewComment{{ID: 501, Body: "rename a", Path: "x.go", Line: 3, Author: "rev", Association: "OWNER", CreatedAt: r.now}}
	r.gh.comments[wsDirB] = []ReviewComment{{ID: 777, Body: "rename b", Path: "y.ts", Line: 9, Author: "rev", Association: "MEMBER", CreatedAt: r.now}}
	r.poll(t, wsSession())

	items := r.items("ws1")
	a, okA := itemByRef(items, "review:o/a:501")
	b, okB := itemByRef(items, "review:o/b:777")
	if len(items) != 2 || !okA || !okB {
		t.Fatalf("todos %+v, want review:o/a:501 and review:o/b:777", items)
	}
	if !strings.HasPrefix(a.Text, "Address review: rename a") || !strings.HasPrefix(b.Text, "Address review: rename b") {
		t.Fatalf("texts %q, %q", a.Text, b.Text)
	}
	notes := r.steers.all()
	if len(notes) != 2 {
		t.Fatalf("notes %q, want one per comment", notes)
	}
	// Seen comments are not steered again.
	r.poll(t, wsSession())
	if len(r.items("ws1")) != 2 || len(r.steers.all()) != 2 {
		t.Fatalf("repoll added work: todos %+v notes %d", r.items("ws1"), len(r.steers.all()))
	}
}

// A gh failure on one member is that member's LastError and does not stop
// the other member's poll.
func TestWatchWorkspaceMemberErrorIsolated(t *testing.T) {
	r := newWSRig(t)
	r.gh.prs[wsDirA] = failing(11, "aaaaaaa1", "build")
	r.gh.prErrs[wsDirB] = errors.New("gh: rate limited")
	_ = r.w.PollSession(context.Background(), wsSession())

	if _, ok := itemByRef(r.items("ws1"), "check:o/a:build"); !ok {
		t.Fatalf("todos %+v, want o/a's check despite o/b failing", r.items("ws1"))
	}
	ctx := context.Background()
	b, _ := r.store.LoadPRWatchRepo(ctx, "ws1", "o/b")
	if !strings.Contains(b.LastError, "rate limited") {
		t.Fatalf("o/b last error %q", b.LastError)
	}
	if a, _ := r.store.LoadPRWatchRepo(ctx, "ws1", "o/a"); a.LastError != "" || a.PRNumber != 11 {
		t.Fatalf("o/a state %+v", a)
	}
}

func TestWatchWorkspacePlainRefsUnchanged(t *testing.T) {
	r := newWSRig(t)
	plain := domain.Session{ID: "p1", WorktreePath: "/w/p1", RepoName: "o/a", Mode: domain.SessionModeAutonomous,
		Status: domain.SessionStatusActive, CreatedAt: time.Date(2026, 11, 27, 11, 0, 0, 0, time.UTC)}
	r.gh.prs["/w/p1"] = failing(5, "ddddddd1", "build")
	r.gh.comments["/w/p1"] = []ReviewComment{{ID: 9, Body: "x", Path: "a.go", Line: 1, Author: "rev", Association: "OWNER", CreatedAt: r.now}}
	r.poll(t, plain)

	items := r.items("p1")
	if _, ok := itemByRef(items, "check:build"); !ok {
		t.Fatalf("todos %+v, want ref check:build", items)
	}
	if _, ok := itemByRef(items, "review:9"); !ok {
		t.Fatalf("todos %+v, want ref review:9", items)
	}
	if st, _ := r.store.LoadPRWatch(context.Background(), "p1"); st.PRNumber != 5 {
		t.Fatalf("plain state %+v, want PR 5 under repo \"\"", st)
	}
}
