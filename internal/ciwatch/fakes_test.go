package ciwatch

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
)

// fakeGH returns scripted GitHub answers and records which dirs were viewed.
type fakeGH struct {
	mu       sync.Mutex
	pr       PRView
	prErr    error
	logs     map[int64]string
	logErr   error
	comments []ReviewComment
	commErr  error
	viewer   string
	viewErr  error
	viewed   []string
	onView   func(dir string)
}

func (f *fakeGH) PRView(_ context.Context, dir string) (PRView, error) {
	f.mu.Lock()
	f.viewed = append(f.viewed, dir)
	pr, err, hook := f.pr, f.prErr, f.onView
	f.mu.Unlock()
	if hook != nil {
		hook(dir)
	}
	return pr, err
}

func (f *fakeGH) FailedJobLog(_ context.Context, _ string, jobID int64) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.logErr != nil {
		return "", f.logErr
	}
	return TailLines(f.logs[jobID], LogTailLines), nil
}

func (f *fakeGH) ReviewComments(context.Context, string, int) ([]ReviewComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ReviewComment(nil), f.comments...), f.commErr
}

func (f *fakeGH) ViewerLogin(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.viewer, f.viewErr
}

func (f *fakeGH) setPR(pr PRView) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pr, f.prErr = pr, nil
}

// fakeTodos is an in-memory Todos that numbers new items t1, t2, … the way
// serve's store does.
type fakeTodos struct {
	mu      sync.Mutex
	lists   map[string]*domain.TodoList
	next    int
	authors []domain.TodoAuthor
}

func newFakeTodos() *fakeTodos { return &fakeTodos{lists: map[string]*domain.TodoList{}} }

func (f *fakeTodos) Get(id string) (domain.TodoList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if l, ok := f.lists[id]; ok {
		cp := *l
		cp.Items = append([]domain.TodoItem(nil), l.Items...)
		return cp, nil
	}
	return domain.TodoList{SessionID: id}, nil
}

func (f *fakeTodos) Mutate(_ context.Context, id string, author domain.TodoAuthor, fn func(*domain.TodoList) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.lists[id]
	if !ok {
		l = &domain.TodoList{SessionID: id}
		f.lists[id] = l
	}
	work := *l
	work.Items = append([]domain.TodoItem(nil), l.Items...)
	if err := fn(&work); err != nil {
		return err
	}
	for i := range work.Items {
		if work.Items[i].ID == "" {
			f.next++
			work.Items[i].ID = fmt.Sprintf("t%d", f.next)
			work.Items[i].UpdatedBy = author
		}
		if work.Items[i] != findItem(l.Items, work.Items[i].ID) {
			work.Items[i].UpdatedBy = author
		}
	}
	*l = work
	f.authors = append(f.authors, author)
	return nil
}

func findItem(items []domain.TodoItem, id string) domain.TodoItem {
	for _, it := range items {
		if it.ID == id {
			return it
		}
	}
	return domain.TodoItem{}
}

// setState changes an item as the agent would.
func (f *fakeTodos) setState(sessionID, itemID string, st domain.TodoState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, it := range f.lists[sessionID].Items {
		if it.ID == itemID {
			f.lists[sessionID].Items[i].State = st
			f.lists[sessionID].Items[i].UpdatedBy = domain.AuthorAgent
		}
	}
}

// items is the test session's list.
func (f *fakeTodos) items() []domain.TodoItem {
	l, _ := f.Get(testSession().ID)
	return l.Items
}

type emitted struct {
	kind   domain.NotifyKind
	detail string
}

type fakeNotifier struct {
	mu   sync.Mutex
	sent []emitted
}

func (f *fakeNotifier) Emit(_ context.Context, _, _ string, kind domain.NotifyKind, detail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, emitted{kind, detail})
	return nil
}

type steerRecorder struct {
	mu    sync.Mutex
	notes []string
}

func (r *steerRecorder) steer(_ string, author domain.TodoAuthor, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if author != domain.AuthorAiman {
		panic("steer author " + author)
	}
	r.notes = append(r.notes, text)
}

func (r *steerRecorder) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.notes...)
}

type fakeSessions struct{ list []domain.Session }

func (f fakeSessions) List(context.Context) ([]domain.Session, error) { return f.list, nil }

type rig struct {
	w      *Watcher
	gh     *fakeGH
	todos  *fakeTodos
	notify *fakeNotifier
	steers *steerRecorder
	store  *sqlite.Repository
	now    time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	repo, err := sqlite.NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	r := &rig{gh: &fakeGH{logs: map[int64]string{}}, todos: newFakeTodos(), notify: &fakeNotifier{}, steers: &steerRecorder{}, store: repo,
		now: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
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

func testSession() domain.Session {
	return domain.Session{ID: "s1", IssueKey: "PROJ-1", WorktreePath: "/w/s1", Mode: domain.SessionModeAutonomous,
		Status: domain.SessionStatusActive, CreatedAt: time.Date(2026, 10, 27, 11, 0, 0, 0, time.UTC)}
}

func numberedLines(n int) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func failingLint(sha string) PRView {
	return PRView{Number: 42, HeadSHA: sha, State: "OPEN", Checks: []Check{
		{Name: "lint", Conclusion: ConclusionFailure, DetailsURL: "https://github.com/o/r/actions/runs/111/job/222", JobID: 222},
	}}
}

func (r *rig) poll(t *testing.T, s domain.Session) {
	t.Helper()
	if err := r.w.PollSession(context.Background(), s); err != nil {
		t.Fatal(err)
	}
}
