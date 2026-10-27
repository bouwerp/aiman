package ciwatch

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// openPR is a PR with no checks, so only comments produce work.
func openPR() PRView { return PRView{Number: 42, HeadSHA: "aaaaaaa1", State: "OPEN"} }

// watchedSince saves WatchSince the way SetWatch does.
func (r *rig) watchedSince(t *testing.T, at time.Time) {
	t.Helper()
	if err := r.store.SavePRWatch(context.Background(), domain.PRWatchState{SessionID: "s1", WatchSince: at}); err != nil {
		t.Fatal(err)
	}
}

func TestNewCommentAddsTodo(t *testing.T) {
	r := newRig(t)
	r.gh.viewer = "me"
	r.gh.setPR(openPR())
	r.gh.comments = []ReviewComment{{ID: 9, Body: "Please rename foo\nmore", Path: "a/b.go", Line: 12, Author: "rev", CreatedAt: r.now}}
	r.poll(t, testSession())

	items := r.todos.items()
	if len(items) != 1 || items[0].Text != "Address review: Please rename foo (a/b.go:12)" || items[0].Ref != "review:9" || items[0].UpdatedBy != domain.AuthorAiman {
		t.Fatalf("todos %+v", items)
	}
	notes := r.steers.all()
	if len(notes) != 1 || !strings.HasPrefix(notes[0], "[aiman] New review comment on PR #42 at a/b.go:12 by @rev, to-do t1:\n") || !strings.Contains(notes[0], "more") {
		t.Fatalf("notes %q", notes)
	}
}

func TestCommentDedupeAcrossPolls(t *testing.T) {
	r := newRig(t)
	r.gh.viewer = "me"
	r.gh.setPR(openPR())
	r.gh.comments = []ReviewComment{{ID: 9, Body: "x", Path: "a.go", Line: 1, Author: "rev", CreatedAt: r.now}}
	r.poll(t, testSession())
	r.poll(t, testSession())
	if len(r.todos.items()) != 1 || len(r.steers.all()) != 1 {
		t.Fatalf("todos %d steers %d", len(r.todos.items()), len(r.steers.all()))
	}
}

func TestViewerReplyIgnored(t *testing.T) {
	r := newRig(t)
	r.gh.viewer = "me"
	r.gh.setPR(openPR())
	r.gh.comments = []ReviewComment{
		{ID: 10, Body: "Fixed.", Path: "a.go", Line: 1, Author: "me", InReplyTo: 9, CreatedAt: r.now},
		{ID: 11, Body: "Note to self", Path: "a.go", Line: 2, Author: "me", CreatedAt: r.now},
	}
	r.poll(t, testSession())
	items := r.todos.items()
	if len(items) != 1 || items[0].Ref != "review:11" {
		t.Fatalf("todos %+v", items)
	}
	seen, _ := r.store.SeenComments(context.Background(), "s1")
	if !seen[10] || !seen[11] {
		t.Fatalf("seen %v", seen)
	}
}

func TestUnknownViewerDefersComments(t *testing.T) {
	r := newRig(t)
	r.gh.viewer = ""
	r.gh.setPR(openPR())
	r.gh.comments = []ReviewComment{{ID: 10, Body: "Fixed.", Path: "a.go", Line: 1, Author: "me", InReplyTo: 9, CreatedAt: r.now}}
	r.poll(t, testSession())
	if len(r.todos.items()) != 0 {
		t.Fatal("comments processed before the viewer was known")
	}
	r.gh.viewer = "me"
	r.poll(t, testSession())
	if len(r.todos.items()) != 0 {
		t.Fatal("the viewer's reply produced a to-do")
	}
	seen, _ := r.store.SeenComments(context.Background(), "s1")
	if !seen[10] {
		t.Fatal("reply not marked seen once the viewer was known")
	}
}

func TestCommentsBeforeWatchSinceBaselined(t *testing.T) {
	r := newRig(t)
	r.gh.viewer = "me"
	r.watchedSince(t, r.now)
	r.gh.setPR(openPR())
	r.gh.comments = []ReviewComment{
		{ID: 1, Body: "old", Path: "a.go", Line: 1, Author: "rev", CreatedAt: r.now.Add(-time.Hour)},
		{ID: 2, Body: "new", Path: "a.go", Line: 2, Author: "rev", CreatedAt: r.now.Add(30 * time.Second)},
	}
	r.poll(t, testSession())
	items := r.todos.items()
	if len(items) != 1 || items[0].Ref != "review:2" {
		t.Fatalf("todos %+v", items)
	}
	seen, _ := r.store.SeenComments(context.Background(), "s1")
	if !seen[1] || !seen[2] {
		t.Fatalf("seen %v", seen)
	}
}

func TestZeroWatchSinceUsesSessionCreatedAt(t *testing.T) {
	r := newRig(t)
	r.gh.viewer = "me"
	r.gh.setPR(openPR())
	s := testSession()
	r.gh.comments = []ReviewComment{
		{ID: 1, Body: "before", Path: "a.go", Line: 1, Author: "bot", CreatedAt: s.CreatedAt.Add(-time.Minute)},
		{ID: 2, Body: "after", Path: "a.go", Line: 2, Author: "bot", CreatedAt: s.CreatedAt.Add(30 * time.Second)},
	}
	r.poll(t, s)
	items := r.todos.items()
	if len(items) != 1 || items[0].Ref != "review:2" {
		t.Fatalf("todos %+v", items)
	}
}

func TestCommentWithoutLineAndLongFirstLine(t *testing.T) {
	r := newRig(t)
	r.gh.viewer = "me"
	r.gh.setPR(openPR())
	r.gh.comments = []ReviewComment{{ID: 3, Body: "\n\n" + strings.Repeat("é", 100), Path: "a.go", Author: "rev", CreatedAt: r.now}}
	r.poll(t, testSession())
	items := r.todos.items()
	want := "Address review: " + strings.Repeat("é", 80) + "… (a.go)"
	if len(items) != 1 || items[0].Text != want {
		t.Fatalf("todos %+v", items)
	}
	if notes := r.steers.all(); len(notes) != 1 || !strings.HasPrefix(notes[0], "[aiman] New review comment on PR #42 at a.go by @rev, to-do t1:\n") {
		t.Fatalf("notes %q", notes)
	}
}

func TestLongBodyTruncated(t *testing.T) {
	r := newRig(t)
	r.gh.viewer = "me"
	r.gh.setPR(openPR())
	r.gh.comments = []ReviewComment{{ID: 4, Body: strings.Repeat("x", 10*1024), Path: "a.go", Line: 1, Author: "rev", CreatedAt: r.now}}
	r.poll(t, testSession())
	notes := r.steers.all()
	if len(notes) != 1 {
		t.Fatalf("notes %d", len(notes))
	}
	_, body, _ := strings.Cut(notes[0], ":\n")
	if !strings.HasSuffix(body, "\n[truncated]") || len(strings.TrimSuffix(body, "\n[truncated]")) > 4000 {
		t.Fatalf("body len %d, tail %q", len(body), body[len(body)-20:])
	}
}

func TestOpenReviewTodoNotDuplicated(t *testing.T) {
	r := newRig(t)
	r.gh.viewer = "me"
	r.gh.setPR(openPR())
	r.gh.comments = []ReviewComment{{ID: 9, Body: "x", Path: "a.go", Line: 1, Author: "rev", CreatedAt: r.now}}
	r.poll(t, testSession())
	// A crash between writing the to-do and marking it seen re-reads the comment.
	if err := r.store.ResetPRWatch(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	r.poll(t, testSession())
	if len(r.todos.items()) != 1 || len(r.steers.all()) != 1 {
		t.Fatalf("todos %d steers %d", len(r.todos.items()), len(r.steers.all()))
	}
}
