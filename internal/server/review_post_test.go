package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/bouwerp/aiman/internal/design"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/review"
	"github.com/bouwerp/aiman/internal/todo"
)

const completeReview = `# Review: o/r#7

## Answers

### What, why, how
> adds a parser

### Blast radius
> only the cli

### Less code
> none to drop

## Findings

### F1 blocking internal/x/y.go:42
Verified: yes (go test fails)
Source: primary
Call: agree "yes, real bug"

Nil deref on empty input.

### F2 nit internal/x/z.go:3
Verified: yes (read it)
Source: secondary
Call: disagree "style only"

Rename this.

### F3 question internal/x/w.go:9
Verified: no
Source: both
Call: agree "ask it"

Why a global here?

## Summary

Looks mostly fine; one bug.
`

// postRig is a review session with a closed gate, a complete review file and
// a fake GitHub that records posts.
type postRig struct {
	reviewRig
	info SessionInfo
	gh   *fakeGitHub
	file string
}

func newPostRig(t *testing.T, body string) postRig {
	t.Helper()
	r := newReviewRig(t)
	gh := &fakeGitHub{prs: map[int]review.PR{7: {Number: 7, Title: "t", BaseRef: "main", HeadRef: "f", HeadSHA: "abc123"}}}
	r.srv.SetReviewGitHub(gh)
	info := createReview(t, r.srv)
	if err := r.srv.TodoAccess().Mutate(t.Context(), info.ID, domain.AuthorAiman, func(l *domain.TodoList) error {
		todo.AddGate(l, todo.ReviewGateRef)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	tickTodo(t, r.srv, info.ID, todo.ReviewGateRef)
	file := filepath.Join(r.stateDir, info.ID, "work", "review.md")
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return postRig{reviewRig: r, info: info, gh: gh, file: "review.md"}
}

// revealReview marks the review's description as revealed so a post can run.
func revealReview(t *testing.T, p postRig) {
	t.Helper()
	st, err := review.LoadState(p.stateDir, p.info.ID)
	if err != nil {
		t.Fatal(err)
	}
	st.Revealed = true
	if err := review.SaveState(p.stateDir, p.info.ID, st); err != nil {
		t.Fatal(err)
	}
}

func (p postRig) post(t *testing.T, params map[string]any) Response {
	t.Helper()
	if _, ok := params["file"]; !ok {
		params["file"] = p.file
	}
	return rpc(t, p.srv, p.info.ID, "review.post", params)
}

func TestPublishAddsReviewGate(t *testing.T) {
	r := newReviewRig(t)
	r.srv.SetDesignPublisher(&design.Publisher{Home: t.TempDir(), Run: design.ExecRun})
	info := createReview(t, r.srv)
	scratch := filepath.Join(r.stateDir, info.ID, "work")
	if err := os.WriteFile(filepath.Join(scratch, "brief.md"), []byte("## Decisions\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rpcOK(t, r.srv, info.ID, "design.publish", map[string]any{"file": "brief.md"}, nil)
	if _, ok := todoStates(t, r.srv, info.ID)[todo.ReviewGateRef]; !ok {
		t.Fatal("first review publish did not add gate:review")
	}
	// A later publish keeps one gate.
	rpcOK(t, r.srv, info.ID, "design.publish", map[string]any{"file": "brief.md"}, nil)
	list, _ := r.srv.TodoAccess().Get(info.ID)
	n := 0
	for _, it := range list.Items {
		if it.Ref == todo.ReviewGateRef {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d review gates", n)
	}

	sess := todoSession("d1")
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, "d.md"), []byte("## Decisions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess.WorkingDirectory, sess.IssueKey = wt, "PROJ-9"
	srv, _, _ := todoServer(t, sess)
	srv.SetDesignPublisher(&design.Publisher{Home: t.TempDir(), Run: design.ExecRun})
	rpcOK(t, srv, "d1", "design.publish", map[string]any{"file": "d.md"}, nil)
	if _, ok := todoStates(t, srv, "d1")[todo.ReviewGateRef]; ok {
		t.Fatal("design session got gate:review")
	}
}

func TestPostRefusals(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		mutate func(t *testing.T, p postRig)
		params map[string]any
		code   string
	}{
		{name: "gate open", body: completeReview, code: CodeGateNeedsOperator, mutate: func(t *testing.T, p postRig) {
			reopen := map[string]bool{todo.ReviewGateRef: true}
			_ = p.srv.TodoAccess().Mutate(t.Context(), p.info.ID, domain.AuthorAiman, func(l *domain.TodoList) error {
				for i := range l.Items {
					if reopen[l.Items[i].Ref] {
						l.Items[i].State = domain.TodoPending
					}
				}
				return nil
			})
		}},
		{name: "incomplete", body: "# Review: o/r#7\n\n## Summary\n\nx\n", code: CodeReviewIncomplete},
		{name: "not revealed", body: completeReview, code: CodeReviewNotReady},
		{name: "head moved", body: completeReview, code: CodeReviewHeadMoved, mutate: func(t *testing.T, p postRig) {
			revealReview(t, p)
			p.gh.prs[7] = review.PR{Number: 7, HeadSHA: "def456"}
		}},
		{name: "approve", body: completeReview, code: CodeInvalidParams, params: map[string]any{"event": "APPROVE"}, mutate: revealReview},
		{name: "approve while gate open", body: completeReview, code: CodeGateNeedsOperator, params: map[string]any{"event": "APPROVE"}, mutate: func(t *testing.T, p postRig) {
			reopen := map[string]bool{todo.ReviewGateRef: true}
			_ = p.srv.TodoAccess().Mutate(t.Context(), p.info.ID, domain.AuthorAiman, func(l *domain.TodoList) error {
				for i := range l.Items {
					if reopen[l.Items[i].Ref] {
						l.Items[i].State = domain.TodoPending
					}
				}
				return nil
			})
		}},
		{name: "outside worktree", body: completeReview, code: CodeInvalidParams, params: map[string]any{"file": "../../../etc/passwd.md"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newPostRig(t, tc.body)
			if tc.mutate != nil {
				tc.mutate(t, p)
			}
			params := map[string]any{}
			for k, v := range tc.params {
				params[k] = v
			}
			resp := p.post(t, params)
			if resp.Error == nil || resp.Error.Code != tc.code {
				t.Fatalf("response %+v, want code %s", resp.Error, tc.code)
			}
			if len(p.gh.posts) != 0 {
				t.Fatalf("posted despite refusal: %+v", p.gh.posts)
			}
		})
	}
}

func TestPostAlreadyPosted(t *testing.T) {
	p := newPostRig(t, completeReview)
	revealReview(t, p)
	if resp := p.post(t, map[string]any{}); resp.Error != nil {
		t.Fatal(resp.Error)
	}
	resp := p.post(t, map[string]any{})
	if resp.Error == nil || resp.Error.Code != CodeReviewAlreadyPosted {
		t.Fatalf("second post %+v, want %s", resp.Error, CodeReviewAlreadyPosted)
	}
	if len(p.gh.posts) != 1 {
		t.Fatalf("%d posts after the repeat", len(p.gh.posts))
	}
	if err := p.srv.reopenReviewTodos(t.Context(), p.info.ID); err != nil {
		t.Fatal(err)
	}
	tickTodo(t, p.srv, p.info.ID, todo.ReviewGateRef)
	if resp := p.post(t, map[string]any{}); resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if len(p.gh.posts) != 2 {
		t.Fatalf("%d posts after reopen, want 2", len(p.gh.posts))
	}
}

func TestPostPostsAgreedOnly(t *testing.T) {
	p := newPostRig(t, completeReview)
	revealReview(t, p)
	resp := p.post(t, map[string]any{})
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	if len(p.gh.posts) != 1 {
		t.Fatalf("%d posts", len(p.gh.posts))
	}
	got := p.gh.posts[0]
	if got.Event != "COMMENT" || got.CommitID != "abc123" {
		t.Fatalf("post %+v", got)
	}
	if len(got.Comments) != 2 || got.Comments[0].Body != "blocking: Nil deref on empty input." || got.Comments[1].Body != "question: Why a global here?" {
		t.Fatalf("comments %+v", got.Comments)
	}
	if st := todoStates(t, p.srv, p.info.ID); st[reviewPostRef] != domain.TodoDone {
		t.Fatalf("post to-do state %v", st)
	}
	i := slices.IndexFunc(ledger(t, p.srv, p.info.ID), func(ev domain.SessionEvent) bool { return ev.Kind == domain.EventReviewPosted })
	if i < 0 {
		t.Fatal("no review.posted row")
	}
	var pl map[string]any
	if err := json.Unmarshal(ledger(t, p.srv, p.info.ID)[i].Payload, &pl); err != nil {
		t.Fatal(err)
	}
	if pl["event"] != "COMMENT" || pl["blocking"] != float64(1) || pl["question"] != float64(1) || pl["nit"] != float64(0) || pl["suggestion"] != float64(0) {
		t.Fatalf("payload %v", pl)
	}
}
