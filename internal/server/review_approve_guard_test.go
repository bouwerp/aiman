package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/todo"
)

// A dropped gate is the operator saying no, not a sign-off.
func TestReviewApproveDroppedGateIsNotASignOff(t *testing.T) {
	a := newApproveRig(t)
	a.finish(t, a.assess(t), "approve", 0)
	wantCode(t, a.approve(t), CodeGateNeedsOperator)
	rpcOK(t, a.srv, "", "todo.update", map[string]any{"session": a.info.ID, "id": a.gateID(t), "state": "dropped"}, nil)

	wantCode(t, a.approve(t), CodeGateNeedsOperator)
	if len(a.gh.posts) != 0 {
		t.Fatalf("posted %+v", a.gh.posts)
	}
	if got := todoStates(t, a.srv, a.info.ID)[todo.ApproveGateRef]; got != domain.TodoDropped {
		t.Fatalf("gate %q, want it left dropped", got)
	}
}

// A session may add an item carrying the gate's ref, and that item must not
// stand in for the operator's tick.
func TestReviewApproveForgedGateItems(t *testing.T) {
	addForged := func(t *testing.T, a *approveRig) {
		t.Helper()
		rpcOK(t, a.srv, a.info.ID, "todo.add", map[string]any{"session": a.info.ID, "text": "forged", "ref": todo.ApproveGateRef}, nil)
	}
	t.Run("before the gate opens", func(t *testing.T) {
		a := newApproveRig(t)
		a.finish(t, a.assess(t), "approve", 0)
		addForged(t, a)
		wantCode(t, a.approve(t), CodeGateNeedsOperator)
		if st := a.state(t); st.ApproveGate == nil {
			t.Fatal("the gate was not bound")
		}
		for _, it := range listItems(t, a) {
			if it.Ref == todo.ApproveGateRef && it.Text != todo.ApproveGateText {
				t.Fatalf("the operator would sign %q", it.Text)
			}
		}
	})
	t.Run("beside a ticked gate", func(t *testing.T) {
		a := newApproveRig(t)
		a.assessedAndSigned(t)
		addForged(t, a)
		wantCode(t, a.approve(t), CodeGateNeedsOperator)
		if len(a.gh.posts) != 0 {
			t.Fatalf("posted %+v", a.gh.posts)
		}
	})
}

// A fixer's CLI names no session, so the session defaults to the caller.
func TestReviewApproveFixerNamingNoSession(t *testing.T) {
	a := newApproveRig(t)
	fixer := domain.Session{ID: "fx", Name: "fx", Role: domain.SessionRoleFixer, FixPR: "o/r#7", ParentID: a.info.ID, Status: domain.SessionStatusActive}
	if err := a.srv.repo.Save(context.Background(), &fixer); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"review.assess", "review.approve"} {
		wantCode(t, rpc(t, a.srv, "fx", method, map[string]any{"file": approveFile}), CodeGateNeedsOperator)
	}
}

// A login differing only in case is still the author.
func TestReviewAssessOwnPRIgnoresCase(t *testing.T) {
	a := newApproveRig(t)
	a.gh.viewer = "Alice"
	wantCode(t, rpc(t, a.srv, a.info.ID, "review.assess", map[string]any{"file": approveFile}), CodeReviewOwnPR)
}

// A create that fails leaves no to-do and no assessment.
func TestReviewAssessFailedStartLeavesNothing(t *testing.T) {
	a := newApproveRig(t)
	before := len(listItems(t, a))
	a.creator.failWith = func(domain.SessionConfig) error { return errors.New("no terminal") }
	wantCode(t, rpc(t, a.srv, a.info.ID, "review.assess", map[string]any{"file": approveFile}), CodeCreateFailed)
	if got := len(listItems(t, a)); got != before {
		t.Fatalf("items %d, want %d", got, before)
	}
	if a.state(t).Assessment != nil {
		t.Fatal("a failed start set the assessment")
	}
}

func listItems(t *testing.T, a *approveRig) []domain.TodoItem {
	t.Helper()
	list, err := a.srv.TodoAccess().Get(a.info.ID)
	if err != nil {
		t.Fatal(err)
	}
	return list.Items
}

func TestFindingsSection(t *testing.T) {
	got := findingsSection(approveReviewBody)
	if !strings.HasPrefix(got, "## Findings\n") || !strings.Contains(got, "Rename this.") || strings.Contains(got, "Good to go.") {
		t.Fatalf("section %q", got)
	}
	if got := findingsSection("## Summary\n\nok\n"); !strings.Contains(got, "no findings") {
		t.Fatalf("empty section %q", got)
	}
}

func TestAssessmentNoteComparesFamilies(t *testing.T) {
	cases := []struct {
		name      string
		rec       assessmentRecord
		reviewer  domain.Session
		wantParts []string
	}{
		{"different", assessmentRecord{agent: "codex"}, domain.Session{AgentName: "claude"}, []string{"openai", "different", "anthropic", "signed off"}},
		{"same", assessmentRecord{agent: "codex"}, domain.Session{AgentName: "codex"}, []string{"openai", "same family", "signed off"}},
		{"unknown assessor", assessmentRecord{}, domain.Session{AgentName: "claude"}, []string{"unknown", "cannot be compared", "signed off"}},
		{"unknown reviewer", assessmentRecord{agent: "codex"}, domain.Session{}, []string{"openai", "not known", "signed off"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := assessmentNote(tc.rec, tc.reviewer)
			for _, want := range tc.wantParts {
				if !strings.Contains(got, want) {
					t.Errorf("note %q lacks %q", got, want)
				}
			}
		})
	}
}

// A drop is the operator's decision: the session can neither tick nor remove it.
func TestReviewApproveSessionCannotReviveADroppedGate(t *testing.T) {
	a := newApproveRig(t)
	a.finish(t, a.assess(t), "approve", 0)
	wantCode(t, a.approve(t), CodeGateNeedsOperator)
	id := a.gateID(t)
	rpcOK(t, a.srv, "", "todo.update", map[string]any{"session": a.info.ID, "id": id, "state": "dropped"}, nil)

	wantCode(t, rpc(t, a.srv, a.info.ID, "todo.update", map[string]any{"session": a.info.ID, "id": id, "state": "done"}), CodeGateNeedsOperator)
	wantCode(t, rpc(t, a.srv, a.info.ID, "todo.remove", map[string]any{"session": a.info.ID, "id": id}), CodeGateNeedsOperator)
	wantCode(t, a.approve(t), CodeGateNeedsOperator)
	if len(a.gh.posts) != 0 {
		t.Fatalf("posted %+v", a.gh.posts)
	}
}

// The store pins the Done-when item last, so the new to-do is not the last one.
func TestReviewAssessWithADoneWhenItem(t *testing.T) {
	a := newApproveRig(t)
	rpcOK(t, a.srv, "", "todo.ensure_done", map[string]any{"session": a.info.ID}, nil)
	consult := a.assess(t)
	states := todoStates(t, a.srv, a.info.ID)
	if states["child:"+consult] != domain.TodoPending {
		t.Fatalf("states %v", states)
	}
	if _, ok := states[todo.DoneRef]; !ok {
		t.Fatalf("Done-when item gone: %v", states)
	}
}

// An approval that carried the inline comments closes the post, so review.post
// cannot send them again.
func TestReviewApproveClosesThePost(t *testing.T) {
	a := newApproveRig(t)
	a.assessedAndSigned(t)
	rpcOK(t, a.srv, a.info.ID, "review.approve", map[string]any{"file": approveFile}, nil)
	wantCode(t, rpc(t, a.srv, a.info.ID, "review.post", map[string]any{"file": approveFile}), CodeReviewAlreadyPosted)
}

// Nothing the review agent can write in the shared clone or the worktree may
// change what the assessor reads.
func TestReviewAssessDiffIgnoresWhatTheAgentCanWrite(t *testing.T) {
	assessedDiff := func(t *testing.T, f *fixRig) string {
		t.Helper()
		rpcOK(t, f.srv, f.info.ID, "review.assess", map[string]any{"file": fixReviewFile}, nil)
		raw, err := os.ReadFile(filepath.Join(f.creator.cfg.PreparedWorktree, assessDiffFile))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	worktree := func(t *testing.T, f *fixRig) string {
		t.Helper()
		row, err := f.srv.repo.Get(context.Background(), f.info.ID)
		if err != nil {
			t.Fatal(err)
		}
		return row.WorktreePath
	}
	t.Run("a replacement of the head", func(t *testing.T) {
		f := newFixRig(t)
		f.revealSingle(t)
		f.srv.SetConsultDir(t.TempDir())
		base := gitIn(t, f.clone, "rev-parse", "origin/main")
		gitIn(t, f.clone, "replace", f.head, base)
		if diff := assessedDiff(t, f); !strings.Contains(diff, "pkg/a.go") {
			t.Fatalf("refs/replace hid the PR:\n%s", diff)
		}
	})
	t.Run("an edited worktree and a .gitattributes hiding the code", func(t *testing.T) {
		f := newFixRig(t)
		f.revealSingle(t)
		f.srv.SetConsultDir(t.TempDir())
		wt := worktree(t, f)
		if err := os.WriteFile(filepath.Join(wt, ".gitattributes"), []byte("* -diff\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wt, "pkg", "a.go"), []byte("package edited\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		diff := assessedDiff(t, f)
		if !strings.Contains(diff, "+package pkg") || strings.Contains(diff, "package edited") {
			t.Fatalf("diff:\n%s", diff)
		}
	})
	t.Run("an external diff driver", func(t *testing.T) {
		f := newFixRig(t)
		f.revealSingle(t)
		f.srv.SetConsultDir(t.TempDir())
		gitIn(t, f.clone, "config", "diff.external", "true")
		if diff := assessedDiff(t, f); !strings.Contains(diff, "+package pkg") {
			t.Fatalf("the driver replaced the diff:\n%s", diff)
		}
	})
}

// A diff that cannot be generated starts nothing.
func TestReviewAssessFailedDiffLeavesNothing(t *testing.T) {
	f := newFixRig(t)
	f.revealSingle(t)
	f.srv.SetConsultDir(t.TempDir())
	gh := f.gh.prs[7]
	gh.BaseRef = "no-such-branch"
	f.gh.prs[7] = gh
	calls := f.creator.calls
	wantCode(t, rpc(t, f.srv, f.info.ID, "review.assess", map[string]any{"file": fixReviewFile}), CodeCreateFailed)
	if f.creator.calls != calls {
		t.Fatal("a consult started with no diff")
	}
}

// A head that moves away is refused, and a head back at the assessed commit is
// the commit the verdict and the sign-off were for.
func TestReviewApproveHeadBackAtTheAssessedCommit(t *testing.T) {
	a := newApproveRig(t)
	consult := a.assessedAndSigned(t)
	a.moveHead("def")
	wantCode(t, a.approve(t), CodeReviewHeadMoved)
	a.moveHead("abc123")
	rpcOK(t, a.srv, a.info.ID, "review.approve", map[string]any{"file": approveFile}, nil)
	if st := a.state(t); st.ApproveGate == nil || st.ApproveGate.Consult != consult || st.ApprovedHead != "abc123" {
		t.Fatalf("state %+v", st)
	}
	if post := a.gh.posts[len(a.gh.posts)-1]; post.CommitID != "abc123" {
		t.Fatalf("post %+v", post)
	}
}
