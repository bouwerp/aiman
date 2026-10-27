package ciwatch

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestFailedCheckAddsTodoAndSteersLog(t *testing.T) {
	r := newRig(t)
	r.gh.setPR(failingLint("aaaaaaa1"))
	r.gh.logs[222] = numberedLines(200)
	r.poll(t, testSession())

	items := r.todos.items()
	if len(items) != 1 || items[0].Text != "Fix CI: lint" || items[0].Ref != "check:lint" || items[0].UpdatedBy != domain.AuthorAiman {
		t.Fatalf("todos %+v", items)
	}
	notes := r.steers.all()
	if len(notes) != 1 {
		t.Fatalf("steers %q", notes)
	}
	note := notes[0]
	if !strings.HasPrefix(note, `[aiman] CI check "lint" failed on aaaaaaa (PR #42), to-do t1.`+"\nLast 150 lines of `gh run view --job 222 --log-failed`:\n") {
		t.Fatalf("note %q", note)
	}
	if !strings.Contains(note, "line 51\n") || !strings.HasSuffix(note, "line 200") || strings.Contains(note, "line 50\n") {
		t.Fatalf("excerpt wrong: %q", note)
	}
}

func TestSamePollRepeatedDoesNotDuplicate(t *testing.T) {
	r := newRig(t)
	r.gh.setPR(failingLint("aaaaaaa1"))
	r.poll(t, testSession())
	r.poll(t, testSession())
	if n := len(r.todos.items()); n != 1 {
		t.Fatalf("todos %d", n)
	}
	if n := len(r.steers.all()); n != 1 {
		t.Fatalf("steers %d", n)
	}
	if n, _ := r.store.RecordCheckFailure(context.Background(), "s1", "lint", "aaaaaaa1"); n != 1 {
		t.Fatalf("failure count %d", n)
	}
}

func TestOpenTodoSuppressesNewNotes(t *testing.T) {
	r := newRig(t)
	r.gh.setPR(failingLint("aaaaaaa1"))
	r.poll(t, testSession())
	r.gh.setPR(failingLint("bbbbbbb2"))
	r.poll(t, testSession())
	if n := len(r.todos.items()); n != 1 {
		t.Fatalf("todos %d", n)
	}
	if n := len(r.steers.all()); n != 1 {
		t.Fatalf("steers %d", n)
	}
}

func TestEscalatesAfterThreeFailedFixes(t *testing.T) {
	r := newRig(t)
	for i, sha := range []string{"aaaaaaa1", "bbbbbbb2", "ccccccc3", "ddddddd4"} {
		r.gh.setPR(failingLint(sha))
		r.poll(t, testSession())
		for _, it := range r.todos.items() {
			r.todos.setState("s1", it.ID, domain.TodoDone)
		}
		want := min(i+1, 3)
		if n := len(r.steers.all()); n != want {
			t.Fatalf("poll %d: steers %d, want %d", i+1, n, want)
		}
	}
	if len(r.notify.sent) != 1 || r.notify.sent[0].kind != domain.NotifyCIEscalation ||
		r.notify.sent[0].detail != "CI check lint still failing after 3 fix attempts on PR #42" {
		t.Fatalf("notifications %+v", r.notify.sent)
	}
	r.gh.setPR(failingLint("eeeeeee5"))
	r.poll(t, testSession())
	if len(r.notify.sent) != 1 || len(r.steers.all()) != 3 {
		t.Fatalf("after escalation: %d notifications, %d steers", len(r.notify.sent), len(r.steers.all()))
	}
	st, err := r.store.LoadPRWatch(context.Background(), "s1")
	if err != nil || len(st.Escalated) != 1 || st.Escalated[0] != "lint" {
		t.Fatalf("escalated %v %v", st.Escalated, err)
	}
}

func TestPassClearsAttemptsAndClosesTodo(t *testing.T) {
	r := newRig(t)
	r.gh.setPR(failingLint("aaaaaaa1"))
	r.poll(t, testSession())
	r.gh.setPR(PRView{Number: 42, HeadSHA: "bbbbbbb2", State: "OPEN", Checks: []Check{{Name: "lint", Conclusion: ConclusionSuccess}}})
	r.poll(t, testSession())
	items := r.todos.items()
	if len(items) != 1 || items[0].State != domain.TodoDone || items[0].UpdatedBy != domain.AuthorAiman {
		t.Fatalf("todo after pass %+v", items)
	}
	r.gh.setPR(failingLint("ccccccc3"))
	r.poll(t, testSession())
	items = r.todos.items()
	if len(items) != 2 || items[1].Ref != "check:lint" || items[1].State != domain.TodoPending {
		t.Fatalf("todos after new failure %+v", items)
	}
	if n := len(r.steers.all()); n != 2 {
		t.Fatalf("steers %d", n)
	}
	if n, _ := r.store.RecordCheckFailure(context.Background(), "s1", "lint", "ccccccc3"); n != 1 {
		t.Fatalf("failure count after pass %d", n)
	}
}

func TestLogUnavailableNote(t *testing.T) {
	r := newRig(t)
	r.gh.setPR(PRView{Number: 42, HeadSHA: "aaaaaaa1", State: "OPEN", Checks: []Check{
		{Name: "ci/ext", Conclusion: ConclusionFailure, DetailsURL: "https://ci.example/9"},
	}})
	r.poll(t, testSession())
	notes := r.steers.all()
	if len(notes) != 1 || !strings.HasSuffix(notes[0], "to-do t1.\nLog unavailable: https://ci.example/9") {
		t.Fatalf("notes %q", notes)
	}
}

func TestLogErrorFallsBackToURL(t *testing.T) {
	r := newRig(t)
	r.gh.setPR(failingLint("aaaaaaa1"))
	r.gh.logErr = errors.New("run in progress")
	r.poll(t, testSession())
	notes := r.steers.all()
	if len(notes) != 1 || !strings.HasSuffix(notes[0], "Log unavailable: https://github.com/o/r/actions/runs/111/job/222") {
		t.Fatalf("notes %q", notes)
	}
}

func TestGHErrorChangesNothing(t *testing.T) {
	r := newRig(t)
	r.gh.prErr = errors.New("gh: auth required")
	if err := r.w.PollSession(context.Background(), testSession()); err == nil {
		t.Fatal("want error")
	}
	st, err := r.store.LoadPRWatch(context.Background(), "s1")
	if err != nil || st.LastError != "gh: auth required" {
		t.Fatalf("state %+v %v", st, err)
	}
	if len(r.todos.items()) != 0 || len(r.steers.all()) != 0 {
		t.Fatal("a gh error changed to-dos or steered")
	}
	if n, _ := r.store.RecordCheckFailure(context.Background(), "s1", "lint", "x"); n != 1 {
		t.Fatalf("a gh error recorded failures: %d", n)
	}
}

func TestMergedPRIsQuiet(t *testing.T) {
	r := newRig(t)
	pr := failingLint("aaaaaaa1")
	pr.State = "MERGED"
	r.gh.setPR(pr)
	r.poll(t, testSession())
	if len(r.todos.items()) != 0 || len(r.steers.all()) != 0 {
		t.Fatal("a merged PR steered")
	}
}

func TestNoPRRecordsZero(t *testing.T) {
	r := newRig(t)
	if err := r.store.SavePRWatch(context.Background(), domain.PRWatchState{SessionID: "s1", PRNumber: 7, LastError: "old"}); err != nil {
		t.Fatal(err)
	}
	r.gh.prErr = ErrNoPR
	r.poll(t, testSession())
	st, err := r.store.LoadPRWatch(context.Background(), "s1")
	if err != nil || st.PRNumber != 0 || st.LastError != "" {
		t.Fatalf("state %+v %v", st, err)
	}
}
