package verify

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/quality"
	"github.com/bouwerp/aiman/internal/todo"
)

const sampleReview = `# Review

## [REQUIRED] Nil map write
internal/a.go:42
The map is never initialised.

## [SUGGESTION] Name the constant
internal/a.go:7
`

// running starts a review and waits until its reviewer is up.
func (r *mrig) running(t *testing.T) Review {
	t.Helper()
	rv, err := r.m.Start(context.Background(), StartRequest{WorkerID: "w1"})
	if err != nil {
		t.Fatal(err)
	}
	return r.waitStatus(t, rv.ID, StatusRunning)
}

func writeReview(t *testing.T, rv Review, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(rv.Worktree, rv.Dir, domain.AimanReviewFileName), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (r *mrig) waitCleaned(t *testing.T, id string) Review {
	t.Helper()
	var got Review
	eventually(t, "cleanup", func() bool {
		rv, err := r.m.Status(id)
		got = rv
		return err == nil && rv.CleanedUp
	})
	return got
}

func TestWatchCompletesOnMarker(t *testing.T) {
	r := newMRig(t, Config{})
	r.term.stopKills = true
	rv := r.running(t)
	text := sampleReview + CompleteMarker + "\n"
	writeReview(t, rv, text)

	done := r.waitStatus(t, rv.ID, StatusComplete)
	if done.Required != 1 || done.Suggestions != 1 {
		t.Fatalf("counts %+v", done)
	}
	items := r.todos.items("w1")
	authors := r.todos.authorList()
	if len(items) != 1 || items[0].Text != "Review: Nil map write (internal/a.go:42)" || items[0].Ref != "finding:"+rv.ID+"/f1" ||
		items[0].State != domain.TodoPending || len(authors) != 1 || authors[0] != domain.AuthorVerifier {
		t.Fatalf("todos %+v authors %v", items, authors)
	}
	puts := r.ctx.all()
	if len(puts) != 1 || puts[0].Type != domain.ContextTypeReview || puts[0].Namespace != "group" || puts[0].Key != "PROJ-1" ||
		puts[0].SessionID != "w1" || puts[0].Body != text || puts[0].Title != "Review of impl: 1 required, 1 suggestion(s)" {
		t.Fatalf("context %+v", puts)
	}
	fin := r.notify.all()
	if len(fin) != 1 || fin[0].Required != 1 || fin[0].Suggestions != 1 {
		t.Fatalf("notified %+v", fin)
	}
	cleaned := r.waitCleaned(t, rv.ID)
	if !slices.Contains(r.term.stoppedIDs(), "rev-1") || !slices.Contains(r.sessions.forgottenIDs(), "rev-1") {
		t.Fatalf("stopped %v forgotten %v", r.term.stoppedIDs(), r.sessions.forgottenIDs())
	}
	if _, err := os.Stat(cleaned.Worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree left: %v", err)
	}
}

func TestWatchProcessExitWithoutMarkerIsIncomplete(t *testing.T) {
	r := newMRig(t, Config{})
	rv := r.running(t)
	writeReview(t, rv, sampleReview) // partial, no marker
	r.term.kill("rev-1")
	done := r.waitStatus(t, rv.ID, StatusIncomplete)
	if done.Passed() || done.Required != 1 {
		t.Fatalf("review %+v", done)
	}
	if n := len(r.todos.items("w1")); n != 1 {
		t.Fatalf("partial findings not added: %d", n)
	}
	r.waitCleaned(t, rv.ID)
}

func TestWatchProcessExitUsesAgentEnded(t *testing.T) {
	r := newMRig(t, Config{})
	r.term.stopKills = true
	rv := r.running(t)
	r.sessions.setEnded("rev-1")
	r.waitStatus(t, rv.ID, StatusIncomplete)
}

func TestWatchTimesOut(t *testing.T) {
	r := newMRig(t, Config{Timeout: 50 * time.Millisecond})
	r.term.stopKills = true
	rv := r.running(t)
	r.waitStatus(t, rv.ID, StatusTimedOut)
	r.waitCleaned(t, rv.ID)
	if !slices.Contains(r.term.stoppedIDs(), "rev-1") {
		t.Fatal("reviewer not stopped on timeout")
	}
	if fin := r.notify.all(); len(fin) != 1 || fin[0].Status != StatusTimedOut {
		t.Fatalf("notified %+v", fin)
	}
}

func TestCompleteSkipsDuplicateOpenFinding(t *testing.T) {
	r := newMRig(t, Config{})
	r.term.stopKills = true
	r.todos.lists["w1"] = &domain.TodoList{SessionID: "w1", Items: []domain.TodoItem{
		{ID: "t9", Text: "Review: Nil map write (internal/a.go:42)", State: domain.TodoPending, UpdatedBy: domain.AuthorVerifier},
	}}
	rv := r.running(t)
	writeReview(t, rv, sampleReview+CompleteMarker)
	r.waitStatus(t, rv.ID, StatusComplete)
	if items := r.todos.items("w1"); len(items) != 1 {
		t.Fatalf("duplicate finding added: %+v", items)
	}
}

func TestResumeWatchesRunningReviews(t *testing.T) {
	r := newMRig(t, Config{})
	r.term.stopKills = true
	rv := r.running(t)
	r.m.Close() // serve stops mid-review

	m2 := NewManager(Config{Agents: []string{"claude", "codex"}, TestCommand: "true"}, r.m.d)
	t.Cleanup(m2.Close)
	r.m = m2
	m2.Resume(context.Background())
	writeReview(t, rv, sampleReview+CompleteMarker)
	r.waitStatus(t, rv.ID, StatusComplete)
	r.waitCleaned(t, rv.ID)
}

func TestResumeCleansFinishedButNotCleaned(t *testing.T) {
	r := newMRig(t, Config{})
	rv := r.running(t)
	r.m.Close()
	rv, _ = r.store.Get(rv.ID)
	rv.Status, rv.CleanedUp = StatusComplete, false
	if err := r.store.Put(rv); err != nil {
		t.Fatal(err)
	}
	r.term.kill("rev-1")

	m2 := NewManager(Config{}, r.m.d)
	t.Cleanup(m2.Close)
	r.m = m2
	m2.Resume(context.Background())
	cleaned := r.waitCleaned(t, rv.ID)
	if _, err := os.Stat(cleaned.Worktree); !os.IsNotExist(err) {
		t.Fatalf("worktree left: %v", err)
	}
}

func doneItemList(state domain.TodoState) *domain.TodoList {
	return &domain.TodoList{SessionID: "w1", Items: []domain.TodoItem{
		{ID: "t9", Text: todo.DoneItemText, Ref: todo.DoneRef, State: state, UpdatedBy: domain.AuthorAgent},
	}}
}

func doneItem(t *testing.T, r *mrig) domain.TodoItem {
	t.Helper()
	l, _ := r.todos.Get("w1")
	for _, it := range l.Items {
		if it.Ref == todo.DoneRef {
			return it
		}
	}
	t.Fatal("done item gone")
	return domain.TodoItem{}
}

func TestCompleteReopensDoneItem(t *testing.T) {
	r := newMRig(t, Config{})
	r.term.stopKills = true
	r.todos.lists["w1"] = doneItemList(domain.TodoDone)
	rv := r.running(t)
	writeReview(t, rv, "## [REQUIRED] "+quality.DoneWhenFindingPrefix+"tests added\nNo test covers x.\n\n"+CompleteMarker)
	r.waitStatus(t, rv.ID, StatusComplete)
	if it := doneItem(t, r); it.State != domain.TodoPending || it.UpdatedBy != domain.AuthorVerifier {
		t.Fatalf("done item %+v", it)
	}
	if authors := r.todos.authorList(); len(authors) != 1 {
		t.Fatalf("want one mutation, got %v", authors)
	}
}

func TestCompleteLeavesDoneItemOnCleanPass(t *testing.T) {
	r := newMRig(t, Config{})
	r.term.stopKills = true
	r.todos.lists["w1"] = doneItemList(domain.TodoDone)
	rv := r.running(t)
	writeReview(t, rv, "# Review\n\nNothing to report.\n\n"+CompleteMarker)
	r.waitStatus(t, rv.ID, StatusComplete)
	if it := doneItem(t, r); it.State != domain.TodoDone || it.UpdatedBy != domain.AuthorAgent {
		t.Fatalf("done item %+v", it)
	}
	if authors := r.todos.authorList(); len(authors) != 0 {
		t.Fatalf("clean pass mutated the list: %v", authors)
	}
}

func TestCompleteOtherFindingLeavesDoneItem(t *testing.T) {
	r := newMRig(t, Config{})
	r.term.stopKills = true
	r.todos.lists["w1"] = doneItemList(domain.TodoDone)
	rv := r.running(t)
	writeReview(t, rv, sampleReview+CompleteMarker)
	r.waitStatus(t, rv.ID, StatusComplete)
	if it := doneItem(t, r); it.State != domain.TodoDone {
		t.Fatalf("done item reopened by an unrelated finding: %+v", it)
	}
}
