package harness

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/review"
	"github.com/bouwerp/aiman/internal/todo"
)

const postableReview = `# Review: o/r#7

## Answers

### What, why, how
> adds a parser

### Blast radius
> only the cli

### Less code
> nothing to drop

## Findings

### F1 blocking internal/x/y.go:42
Verified: yes (go test fails)
Source: primary
Call: agree "real bug"

Nil deref on empty input.

### F2 nit internal/x/z.go:3
Verified: yes (read it)
Source: secondary
Call: disagree "style only"

Rename this.

## Summary

One bug.
`

// openReviewGate adds the open review gate the first brief publish adds, and
// writes the review file into the session's scratch directory.
func openReviewGate(h *Harness, id, body string) error {
	err := h.srv.TodoAccess().Mutate(context.Background(), id, domain.AuthorAiman, func(l *domain.TodoList) error {
		todo.AddGate(l, todo.ReviewGateRef)
		return nil
	})
	if err != nil {
		return err
	}
	sess, err := h.Repo.Get(context.Background(), id)
	if err != nil {
		return err
	}
	// The fake creator registers the terminal under sessions/<id>, and serve
	// resolves a live session's directory from the terminal, so the file goes
	// there as well as into the scratch directory the stored row names.
	for _, dir := range []string{sess.WorkingDirectory, filepath.Join(h.Dir, "sessions", id)} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "review.md"), []byte(body), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// markRevealed records that the operator has seen the PR description, which
// a post requires. The evals skip the consult path and set the flag directly.
func markRevealed(h *Harness, id string) error {
	st, err := review.LoadState(h.ReviewDir(), id)
	if err != nil {
		return err
	}
	st.Revealed = true
	return review.SaveState(h.ReviewDir(), id, st)
}

// postCode is the error code of a review.post by the session, or "" when it
// succeeded.
func postCode(h *Harness, id string) (string, string) {
	resp, err := h.CallAs(id, "review.post", map[string]any{"file": "review.md"})
	if err == nil || resp.Error == nil {
		return "", ""
	}
	return resp.Error.Code, resp.Error.Message
}

// approveReview closes the review gate as the session, with the operator's
// words.
func approveReview(h *Harness, id string) error {
	_, err := h.CallAs(id, "todo.approve_review", map[string]any{"session": id, "quote": "go ahead"})
	return err
}

// CaseReviewGate posts nothing while the review gate is open, then posts one
// review at the staged head once the operator approves: the agreed finding
// only, event COMMENT, and a review.posted row.
func CaseReviewGate() Case {
	return Case{Name: "review-gate", Assert: func(h *Harness) []MetricResult {
		id, gh, out := startReviewGH(h)
		if id == "" {
			return out
		}
		if err := openReviewGate(h, id, postableReview); err != nil {
			return append(out, score("gate_opened", false, err.Error()))
		}
		code, _ := postCode(h, id)
		out = append(out, score("post_refused_while_gate_open", code == "gate_needs_operator", "code "+code),
			score("nothing_posted_while_open", len(gh.Posts()) == 0, "posted while the gate was open"))
		if err := approveReview(h, id); err != nil {
			return append(out, score("gate_approved", false, err.Error()))
		}
		code, _ = postCode(h, id)
		out = append(out, score("post_refused_before_reveal", code == "review_not_ready", "code "+code))
		if err := markRevealed(h, id); err != nil {
			return append(out, score("revealed", false, err.Error()))
		}
		code, msg := postCode(h, id)
		posts := gh.Posts()
		out = append(out, score("post_after_approval", code == "", code+" "+msg),
			score("one_review_posted", len(posts) == 1, "posts"))
		code, _ = postCode(h, id)
		out = append(out, score("second_post_refused", code == "review_already_posted", "code "+code),
			score("still_one_review", len(gh.Posts()) == 1, "posts"))
		if len(posts) == 1 {
			p := posts[0]
			out = append(out, score("post_is_comment_at_head", p.Event == "COMMENT" && p.CommitID == reviewHead, p.Event+" "+p.CommitID),
				score("only_agreed_comments", len(p.Comments) == 1 && strings.HasPrefix(p.Comments[0].Body, "blocking:"), "comments"))
		}
		posted := slices.ContainsFunc(h.Events(id), func(ev domain.SessionEvent) bool { return ev.Kind == domain.EventReviewPosted })
		return append(out, score("posted_in_ledger", posted, "no review.posted row"))
	}}
}

// CaseReviewHeadMoved refuses a post once the PR head moved after the brief.
// Refresh needs a real clone, so the gate it reopens is covered by
// TestRefreshResets in internal/server.
func CaseReviewHeadMoved() Case {
	return Case{Name: "review-head-moved", Assert: func(h *Harness) []MetricResult {
		id, gh, out := startReviewGH(h)
		if id == "" {
			return out
		}
		if err := openReviewGate(h, id, postableReview); err != nil {
			return append(out, score("gate_opened", false, err.Error()))
		}
		if err := approveReview(h, id); err != nil {
			return append(out, score("gate_approved", false, err.Error()))
		}
		if err := markRevealed(h, id); err != nil {
			return append(out, score("revealed", false, err.Error()))
		}
		ref, _ := review.ParseRef(reviewRef)
		gh.SetPR(ref, review.PR{Number: 7, Title: "Add the parser", Author: "alice", Body: "b", BaseRef: "main", HeadRef: "feat/parser", HeadSHA: "def456"})
		code, _ := postCode(h, id)
		out = append(out, score("post_refused_head_moved", code == "review_head_moved", "code "+code),
			score("nothing_posted_after_move", len(gh.Posts()) == 0, "posted at a stale head"))
		return out
	}}
}

// CaseReviewIncomplete refuses to post a review file without the operator's
// answers or calls, and names the gaps.
func CaseReviewIncomplete() Case {
	return Case{Name: "review-incomplete", Assert: func(h *Harness) []MetricResult {
		id, gh, out := startReviewGH(h)
		if id == "" {
			return out
		}
		if err := openReviewGate(h, id, "# Review: o/r#7\n\n## Findings\n\n### F1 blocking a.go:1\nVerified: yes (x)\nSource: primary\n\nBody.\n\n## Summary\n\nS.\n"); err != nil {
			return append(out, score("gate_opened", false, err.Error()))
		}
		if err := approveReview(h, id); err != nil {
			return append(out, score("gate_approved", false, err.Error()))
		}
		code, msg := postCode(h, id)
		return append(out,
			score("post_refused_incomplete", code == "review_incomplete", "code "+code),
			score("gaps_named", strings.Contains(msg, "What, why, how") && strings.Contains(msg, "F1"), msg),
			score("nothing_posted_incomplete", len(gh.Posts()) == 0, "posted an incomplete review"))
	}}
}
