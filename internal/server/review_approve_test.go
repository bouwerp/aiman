package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/review"
	"github.com/bouwerp/aiman/internal/todo"
)

// approveReviewBody has nothing blocking open: F1 was disagreed.
const approveReviewBody = `# Review: o/r#7

## Findings

### F1 blocking pkg/a.go:1
Verified: yes (read it)
Source: primary
Call: disagree "not a bug"

Nil deref.

### F2 nit pkg/b.go:2
Verified: yes (read it)
Source: secondary
Call: agree "fine"

Rename this.

## Summary

Good to go.
`

const approveFile = ".aiman_review.md"

// approveGH is a fake GitHub with a chosen viewer, decision and post failure.
type approveGH struct {
	*fakeGitHub
	viewer   string
	decision string
	postErr  error
}

func (g *approveGH) Viewer(context.Context) (string, error) { return g.viewer, nil }

func (g *approveGH) ReviewDecision(context.Context, review.Ref) (string, error) {
	return g.decision, nil
}

func (g *approveGH) PostReview(ctx context.Context, ref review.Ref, p review.Post) error {
	if g.postErr != nil {
		return g.postErr
	}
	return g.fakeGitHub.PostReview(ctx, ref, p)
}

type approveRig struct {
	reviewRig
	gh      *approveGH
	info    SessionInfo
	scratch string
}

// newApproveRig is a revealed review of alice's PR o/r#7 at abc123.
func newApproveRig(t *testing.T) *approveRig {
	t.Helper()
	r := newReviewRig(t)
	r.srv.SetConsultDir(t.TempDir())
	gh := &approveGH{
		fakeGitHub: &fakeGitHub{prs: map[int]review.PR{7: {
			Number: 7, Title: "t", Author: "alice", BaseRef: "main", HeadRef: "f", HeadSHA: "abc123",
		}}},
		viewer: "tester", decision: "APPROVED",
	}
	r.srv.SetReviewGitHub(gh)
	info := createReview(t, r.srv)
	a := &approveRig{reviewRig: r, gh: gh, info: info, scratch: filepath.Join(r.stateDir, info.ID, "work")}
	a.write(t, approveReviewBody)
	a.mutateState(t, func(st *review.State) { st.Revealed = true })
	return a
}

func (a *approveRig) write(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(a.scratch, approveFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (a *approveRig) mutateState(t *testing.T, fn func(*review.State)) {
	t.Helper()
	st := a.state(t)
	fn(&st)
	if err := review.SaveState(a.stateDir, a.info.ID, st); err != nil {
		t.Fatal(err)
	}
}

func (a *approveRig) state(t *testing.T) review.State {
	t.Helper()
	st, err := review.LoadState(a.stateDir, a.info.ID)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func (a *approveRig) moveHead(head string) {
	pr := a.gh.prs[7]
	pr.HeadSHA = head
	a.gh.prs[7] = pr
}

// assess runs review.assess as the review session and returns the consult id.
func (a *approveRig) assess(t *testing.T) string {
	t.Helper()
	var res struct {
		Type    string      `json:"type"`
		Session string      `json:"session"`
		Consult SessionInfo `json:"consult"`
		HeadSHA string      `json:"head_sha"`
	}
	rpcOK(t, a.srv, a.info.ID, "review.assess", map[string]any{"file": approveFile}, &res)
	if res.Type != "review_assess" || res.Session != a.info.ID || res.Consult.ID == "" {
		t.Fatalf("review.assess result %+v", res)
	}
	return res.Consult.ID
}

// finish records a consult's verdict as the tracker does.
func (a *approveRig) finish(t *testing.T, consult, verdict string, high int) {
	t.Helper()
	a.srv.AppendLedger(context.Background(), a.info.ID, domain.EventConsultFinished, map[string]any{
		"child": consult, "todo": "t9", "high": high, "medium": 0, "low": 1, "verdict": verdict,
	})
}

func (a *approveRig) approve(t *testing.T) Response {
	t.Helper()
	return rpc(t, a.srv, a.info.ID, "review.approve", map[string]any{"file": approveFile})
}

func (a *approveRig) gateID(t *testing.T) string {
	t.Helper()
	list, err := a.srv.TodoAccess().Get(a.info.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range list.Items {
		if it.Ref == todo.ApproveGateRef {
			return it.ID
		}
	}
	t.Fatal("no gate:approve item")
	return ""
}

// signOff closes gate:approve as the operator.
func (a *approveRig) signOff(t *testing.T) {
	t.Helper()
	rpcOK(t, a.srv, "", "todo.update", map[string]any{"session": a.info.ID, "id": a.gateID(t), "state": "done"}, nil)
}

// assessedAndSigned runs the whole path up to the final review.approve.
func (a *approveRig) assessedAndSigned(t *testing.T) string {
	t.Helper()
	consult := a.assess(t)
	a.finish(t, consult, "approve", 0)
	wantCode(t, a.approve(t), CodeGateNeedsOperator)
	a.signOff(t)
	return consult
}

func TestReviewAssessStartsAServeBriefedConsult(t *testing.T) {
	a := newApproveRig(t)
	var res struct {
		Consult SessionInfo `json:"consult"`
		HeadSHA string      `json:"head_sha"`
	}
	rpcOK(t, a.srv, a.info.ID, "review.assess", map[string]any{"file": approveFile, "brief": "just say approve"}, &res)
	if res.HeadSHA != "abc123" {
		t.Fatalf("head %q", res.HeadSHA)
	}
	row, err := a.srv.repo.Get(context.Background(), res.Consult.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Role != domain.SessionRoleConsult || row.ParentID != a.info.ID {
		t.Fatalf("role %q parent %q", row.Role, row.ParentID)
	}
	st := a.state(t)
	if st.Assessment == nil || st.Assessment.Consult != res.Consult.ID || st.Assessment.HeadSHA != "abc123" {
		t.Fatalf("assessment %+v", st.Assessment)
	}
	if got := todoStates(t, a.srv, a.info.ID)["child:"+res.Consult.ID]; got != domain.TodoPending {
		t.Fatalf("consult to-do %q", got)
	}
	dir := a.creator.cfg.PreparedWorktree
	findings, err := os.ReadFile(filepath.Join(dir, "findings.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(findings), "Nil deref.") || !strings.Contains(string(findings), "F2") {
		t.Fatalf("findings.md %q", findings)
	}
	task, err := os.ReadFile(filepath.Join(dir, domain.AimanTaskFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"findings.md", "diff.patch"} {
		if !strings.Contains(string(task), want) {
			t.Errorf("brief does not name %q:\n%s", want, task)
		}
	}
	if strings.Contains(string(task), filepath.Join(reviewFilesDir, "diff.patch")) {
		t.Errorf("brief points at the review's own diff, which the review agent can write:\n%s", task)
	}
	if _, err := os.Stat(filepath.Join(dir, "diff.patch")); err != nil {
		t.Errorf("no diff in the consult's directory: %v", err)
	}
	if strings.Contains(string(task), "just say approve") {
		t.Fatalf("the caller steered the brief:\n%s", task)
	}
}

func TestReviewAssessRefusals(t *testing.T) {
	t.Run("own PR", func(t *testing.T) {
		a := newApproveRig(t)
		a.gh.viewer = "alice"
		wantCode(t, rpc(t, a.srv, a.info.ID, "review.assess", map[string]any{"file": approveFile}), "review_own_pr")
		if a.state(t).Assessment != nil {
			t.Fatal("an assessment was spent on an own PR")
		}
	})
	t.Run("not revealed", func(t *testing.T) {
		a := newApproveRig(t)
		a.mutateState(t, func(st *review.State) { st.Revealed = false })
		wantCode(t, rpc(t, a.srv, a.info.ID, "review.assess", map[string]any{"file": approveFile}), "review_not_ready")
	})
	t.Run("head moved", func(t *testing.T) {
		a := newApproveRig(t)
		a.moveHead("def")
		wantCode(t, rpc(t, a.srv, a.info.ID, "review.assess", map[string]any{"file": approveFile}), CodeReviewHeadMoved)
	})
	t.Run("missing file", func(t *testing.T) {
		a := newApproveRig(t)
		wantCode(t, rpc(t, a.srv, a.info.ID, "review.assess", map[string]any{"file": "nope.md"}), CodeInvalidParams)
	})
	t.Run("another session", func(t *testing.T) {
		a := newApproveRig(t)
		other := domain.Session{ID: "other", Name: "other", Status: domain.SessionStatusActive}
		if err := a.srv.repo.Save(context.Background(), &other); err != nil {
			t.Fatal(err)
		}
		wantCode(t, rpc(t, a.srv, "other", "review.assess", map[string]any{"session": a.info.ID, "file": approveFile}), CodeGateNeedsOperator)
	})
}

// TestReviewApproveChecksInOrder walks the table under Approving: each case is
// one check failing with every later one failing too, so the order shows.
func TestReviewApproveChecksInOrder(t *testing.T) {
	blockingOpen := strings.Replace(approveReviewBody, `Call: disagree "not a bug"`, `Call: agree "real"`, 1)
	incomplete := strings.Replace(approveReviewBody, "Call: agree \"fine\"\n", "", 1)
	cases := []struct {
		name    string
		prepare func(t *testing.T, a *approveRig)
		code    string
	}{
		{"own PR first", func(_ *testing.T, a *approveRig) {
			a.gh.viewer = "alice"
			a.moveHead("def")
		}, "review_own_pr"},
		{"not revealed", func(t *testing.T, a *approveRig) {
			a.mutateState(t, func(st *review.State) { st.Revealed = false })
			a.write(t, blockingOpen)
		}, "review_not_ready"},
		{"head moved", func(t *testing.T, a *approveRig) {
			a.moveHead("def")
			a.write(t, blockingOpen)
		}, CodeReviewHeadMoved},
		{"blocking open", func(t *testing.T, a *approveRig) { a.write(t, blockingOpen) }, "review_blocking_open"},
		{"incomplete", func(t *testing.T, a *approveRig) { a.write(t, incomplete) }, CodeReviewIncomplete},
		{"no assessment", func(*testing.T, *approveRig) {}, "review_not_assessed"},
		{"assessment still running", func(t *testing.T, a *approveRig) { a.assess(t) }, "review_not_assessed"},
		{"a consult the agent started itself", func(t *testing.T, a *approveRig) {
			a.finish(t, "self-started", "approve", 0)
		}, "review_not_assessed"},
		{"assessment replaced", func(t *testing.T, a *approveRig) {
			first := a.assess(t)
			a.finish(t, first, "approve", 0)
			a.assess(t)
		}, "review_not_assessed"},
		{"verdict revise", func(t *testing.T, a *approveRig) { a.finish(t, a.assess(t), "revise", 0) }, "review_assessment_rejected"},
		{"verdict reject", func(t *testing.T, a *approveRig) { a.finish(t, a.assess(t), "reject", 0) }, "review_assessment_rejected"},
		{"approve with a high", func(t *testing.T, a *approveRig) { a.finish(t, a.assess(t), "approve", 1) }, "review_assessment_rejected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newApproveRig(t)
			tc.prepare(t, a)
			wantCode(t, a.approve(t), tc.code)
			if len(a.gh.posts) != 0 {
				t.Fatalf("posted %+v", a.gh.posts)
			}
			if _, open := todoStates(t, a.srv, a.info.ID)[todo.ApproveGateRef]; open {
				t.Fatal("gate:approve opened before a known verdict")
			}
		})
	}
}

func TestReviewApproveGatesThenPosts(t *testing.T) {
	a := newApproveRig(t)
	consult := a.assess(t)
	a.finish(t, consult, "approve", 0)

	wantCode(t, a.approve(t), CodeGateNeedsOperator)
	if got := todoStates(t, a.srv, a.info.ID)[todo.ApproveGateRef]; got != domain.TodoPending {
		t.Fatalf("gate %q", got)
	}
	st := a.state(t)
	if st.ApproveGate == nil || st.ApproveGate.Consult != consult || st.ApproveGate.HeadSHA != "abc123" {
		t.Fatalf("gate binding %+v", st.ApproveGate)
	}
	if len(a.gh.posts) != 0 {
		t.Fatalf("posted before sign-off: %+v", a.gh.posts)
	}

	// The session cannot sign off, with or without a quote.
	id := a.gateID(t)
	wantCode(t, rpc(t, a.srv, a.info.ID, "todo.update", map[string]any{"session": a.info.ID, "id": id, "state": "done"}), CodeGateNeedsOperator)
	_ = rpc(t, a.srv, a.info.ID, "todo.approve_review", map[string]any{"session": a.info.ID, "quote": "approved"})
	if got := todoStates(t, a.srv, a.info.ID)[todo.ApproveGateRef]; got != domain.TodoPending {
		t.Fatalf("a session closed the gate: %q", got)
	}
	wantCode(t, a.approve(t), CodeGateNeedsOperator)

	a.signOff(t)
	var res struct {
		Type     string `json:"type"`
		Head     string `json:"head_sha"`
		Decision string `json:"decision"`
		Counts   bool   `json:"counts"`
	}
	resp := a.approve(t)
	if resp.Error != nil {
		t.Fatalf("approve: %v", resp.Error)
	}
	raw, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Type != "review_approved" || res.Head != "abc123" || res.Decision != "APPROVED" || !res.Counts {
		t.Fatalf("result %+v", res)
	}
	if len(a.gh.posts) != 1 {
		t.Fatalf("posts %+v", a.gh.posts)
	}
	post := a.gh.posts[0]
	if post.Event != "APPROVE" || post.CommitID != "abc123" {
		t.Fatalf("post %+v", post)
	}
	for _, want := range []string{"Good to go.", "signed off", "openai"} {
		if !strings.Contains(post.Body, want) {
			t.Errorf("body lacks %q: %q", want, post.Body)
		}
	}
	if len(post.Comments) != 1 || post.Comments[0].Path != "pkg/b.go" {
		t.Fatalf("comments %+v, want the agreed nit that no earlier post sent", post.Comments)
	}
	if got := a.state(t).ApprovedHead; got != "abc123" {
		t.Fatalf("approved head %q", got)
	}
	found := false
	for _, ev := range ledger(t, a.srv, a.info.ID) {
		if ev.Kind == domain.EventKind("review.pr_approved") {
			var p map[string]any
			_ = json.Unmarshal(ev.Payload, &p)
			found = p["head"] == "abc123" && p["consult"] == consult && p["counts"] == true
		}
	}
	if !found {
		t.Fatal("no review.pr_approved row with head, consult and counts")
	}

	wantCode(t, a.approve(t), "review_already_approved")
	if len(a.gh.posts) != 1 {
		t.Fatalf("approved twice: %+v", a.gh.posts)
	}
}

// An earlier post already sent the inline comments.
func TestReviewApproveAfterAPostHasNoComments(t *testing.T) {
	a := newApproveRig(t)
	err := a.srv.TodoAccess().Mutate(t.Context(), a.info.ID, domain.AuthorAiman, func(l *domain.TodoList) error {
		todo.AddGate(l, todo.ReviewGateRef)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	tickTodo(t, a.srv, a.info.ID, todo.ReviewGateRef)
	rpcOK(t, a.srv, a.info.ID, "review.post", map[string]any{"file": approveFile}, nil)
	a.assessedAndSigned(t)
	rpcOK(t, a.srv, a.info.ID, "review.approve", map[string]any{"file": approveFile}, nil)
	post := a.gh.posts[len(a.gh.posts)-1]
	if post.Event != "APPROVE" || len(post.Comments) != 0 {
		t.Fatalf("post %+v", post)
	}
}

// A closed gate signs off one verdict at one head: a new assessment reopens it.
func TestReviewApproveGateBindsToTheAssessment(t *testing.T) {
	a := newApproveRig(t)
	a.assessedAndSigned(t)
	second := a.assess(t)
	a.finish(t, second, "approve", 0)

	wantCode(t, a.approve(t), CodeGateNeedsOperator)
	if got := todoStates(t, a.srv, a.info.ID)[todo.ApproveGateRef]; got != domain.TodoPending {
		t.Fatalf("gate %q, want reopened", got)
	}
	if st := a.state(t); st.ApproveGate == nil || st.ApproveGate.Consult != second {
		t.Fatalf("binding %+v", st.ApproveGate)
	}
	if len(a.gh.posts) != 0 {
		t.Fatalf("posted %+v", a.gh.posts)
	}
}

func TestReviewApproveHeadMovedAfterSignOff(t *testing.T) {
	a := newApproveRig(t)
	a.assessedAndSigned(t)
	a.moveHead("def")
	wantCode(t, a.approve(t), CodeReviewHeadMoved)
	if len(a.gh.posts) != 0 {
		t.Fatalf("posted %+v", a.gh.posts)
	}
}

// A second login is required by some repos: the approval goes out and the
// result says it does not count.
func TestReviewApproveReportsThatItDoesNotCount(t *testing.T) {
	a := newApproveRig(t)
	a.gh.decision = "REVIEW_REQUIRED"
	a.assessedAndSigned(t)
	var res struct {
		Decision string `json:"decision"`
		Counts   *bool  `json:"counts"`
	}
	rpcOK(t, a.srv, a.info.ID, "review.approve", map[string]any{"file": approveFile}, &res)
	if res.Decision != "REVIEW_REQUIRED" || res.Counts == nil || *res.Counts {
		t.Fatalf("result %+v", res)
	}
	if got := a.state(t).ApprovedHead; got != "abc123" {
		t.Fatalf("approved head %q", got)
	}
}

func TestReviewApprovePostFailures(t *testing.T) {
	cases := map[string]struct {
		err  error
		code string
	}{
		"GitHub refuses an own PR": {fmt.Errorf("post: %w", review.ErrOwnPR), "review_own_pr"},
		"anything else":            {errors.New("boom"), CodeCreateFailed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			a := newApproveRig(t)
			a.assessedAndSigned(t)
			a.gh.postErr = tc.err
			wantCode(t, a.approve(t), tc.code)
			if got := a.state(t).ApprovedHead; got != "" {
				t.Fatalf("approved head %q after a failed post", got)
			}
		})
	}
}

// A blocking finding recorded as fixed no longer blocks, and the body says so.
func TestReviewApproveCountsRecordedFixes(t *testing.T) {
	a := newApproveRig(t)
	a.mutateState(t, func(st *review.State) {
		st.Fixes = []review.FixRound{{Fixer: "f", Findings: []string{"F1"}, Fixed: []string{"F1"}, StartSHA: "000", PushedSHA: "abc123"}}
	})
	a.write(t, strings.Replace(approveReviewBody, `Call: disagree "not a bug"`, "Fixed: abc123", 1))
	a.assessedAndSigned(t)
	rpcOK(t, a.srv, a.info.ID, "review.approve", map[string]any{"file": approveFile}, nil)
	post := a.gh.posts[len(a.gh.posts)-1]
	if !strings.Contains(post.Body, "Fixed in abc123: F1 pkg/a.go") {
		t.Fatalf("body %q", post.Body)
	}
}

func TestReviewApproveOtherCallers(t *testing.T) {
	a := newApproveRig(t)
	fixer := domain.Session{ID: "fx", Name: "fx", Role: domain.SessionRoleFixer, FixPR: "o/r#7", ParentID: a.info.ID, Status: domain.SessionStatusActive}
	if err := a.srv.repo.Save(context.Background(), &fixer); err != nil {
		t.Fatal(err)
	}
	wantCode(t, rpc(t, a.srv, "fx", "review.approve", map[string]any{"session": a.info.ID, "file": approveFile}), CodeGateNeedsOperator)
	// The operator may run it, and gets the same checks.
	wantCode(t, rpc(t, a.srv, "", "review.approve", map[string]any{"session": a.info.ID, "file": approveFile}), "review_not_assessed")
}

// The assessor judges the PR's commits, not a file the review agent can write.
func TestReviewAssessDiffComesFromTheHead(t *testing.T) {
	f := newFixRig(t)
	f.srv.SetConsultDir(t.TempDir())
	f.revealSingle(t)
	own := filepath.Join(f.scratch, reviewFilesDir, "diff.patch")
	if err := os.WriteFile(own, []byte("nothing to see here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rpcOK(t, f.srv, f.info.ID, "review.assess", map[string]any{"file": fixReviewFile}, nil)
	raw, err := os.ReadFile(filepath.Join(f.creator.cfg.PreparedWorktree, "diff.patch"))
	if err != nil {
		t.Fatal(err)
	}
	diff := string(raw)
	if strings.Contains(diff, "nothing to see here") {
		t.Fatalf("the assessor got the review agent's edited diff:\n%s", diff)
	}
	for _, want := range []string{"pkg/a.go", "other/c.go", ".github/workflows/ci.yml"} {
		if !strings.Contains(diff, want) {
			t.Errorf("diff lacks %s:\n%s", want, diff)
		}
	}
	if strings.Contains(diff, "main.txt") {
		t.Errorf("diff holds more than the PR:\n%s", diff)
	}
}
