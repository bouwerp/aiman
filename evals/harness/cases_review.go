package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/review"
)

const (
	reviewRef  = "o/r#7"
	reviewHead = "abc123"
)

// startReview enables review mode with PR o/r#7 at reviewHead and creates the
// review session, returning its id.
func startReview(h *Harness) (string, []MetricResult) {
	gh := h.EnableReview()
	ref, _ := review.ParseRef(reviewRef)
	gh.SetPR(ref, review.PR{
		Number: 7, Title: "Add the parser", Author: "alice", Body: "The parser closes PROJ-1.",
		BaseRef: "main", HeadRef: "feat/parser", HeadSHA: reviewHead,
	})
	resp, err := h.Call("session.create", map[string]any{"review": reviewRef})
	if err != nil {
		return "", []MetricResult{score("review_created", false, err.Error())}
	}
	var res struct {
		Session struct {
			ID       string `json:"id"`
			ReviewPR string `json:"review_pr"`
		} `json:"session"`
	}
	raw, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(raw, &res); err != nil || res.Session.ID == "" {
		return "", []MetricResult{score("review_created", false, "create returned no session id")}
	}
	return res.Session.ID, []MetricResult{score("review_created", res.Session.ReviewPR == reviewRef, "review_pr "+res.Session.ReviewPR)}
}

// reviewFilesMetrics checks the blind inputs serve wrote into the scratch
// directory the agent runs in: diff, files and meta present, meta naming the
// PR worktree, the description absent, and nothing written into the PR
// worktree.
func reviewFilesMetrics(h *Harness, id string) []MetricResult {
	sess, err := h.Repo.Get(context.Background(), id)
	if err != nil {
		return []MetricResult{score("review_row", false, err.Error())}
	}
	wt, work := sess.WorktreePath, sess.WorkingDirectory
	present := true
	for _, name := range []string{"diff.patch", "files.txt", "meta.json"} {
		if _, err := os.Stat(filepath.Join(work, ".aiman_review", name)); err != nil {
			present = false
		}
	}
	var meta struct {
		Number   int    `json:"number"`
		HeadSHA  string `json:"head_sha"`
		Worktree string `json:"worktree"`
	}
	raw, _ := os.ReadFile(filepath.Join(work, ".aiman_review", "meta.json"))
	_ = json.Unmarshal(raw, &meta)
	_, descErr := os.Stat(filepath.Join(work, ".aiman_pr_description.md"))
	_, inPR := os.Stat(filepath.Join(wt, ".aiman_review"))
	return []MetricResult{
		score("worktree_named_for_pr", filepath.Base(wt) == "r@pr-7", "worktree "+wt),
		score("agent_runs_in_scratch", work == filepath.Join(h.ReviewDir(), id, "work"), "working directory "+work),
		score("review_files_present", present, "missing a file under .aiman_review"),
		score("meta_has_head", meta.Number == 7 && meta.HeadSHA == reviewHead, "meta "+string(raw)),
		score("meta_names_worktree", meta.Worktree == wt, "meta worktree "+meta.Worktree),
		score("meta_has_no_description", !strings.Contains(string(raw), "PROJ-1"), "meta carries the description"),
		score("description_absent", os.IsNotExist(descErr), "description written before reveal"),
		score("pr_worktree_untouched", os.IsNotExist(inPR), "review files written into the PR worktree"),
	}
}

// reviewGitMetrics checks the git commands the worktree setup sent: the PR
// head fetched as pull/7/head (which also reaches a fork), a detached
// worktree, and the soft reset that stages the PR.
func reviewGitMetrics(h *Harness) []MetricResult {
	execs := strings.Join(h.Execs(), "\n")
	return []MetricResult{
		score("fetched_pull_head", strings.Contains(execs, "fetch -q origin pull/7/head"), "no pull/7/head fetch"),
		score("detached_worktree", strings.Contains(execs, "worktree add -q --detach") && strings.Contains(execs, "r@pr-7"), "no detached worktree add"),
		score("staged_at_merge_base", strings.Contains(execs, "merge-base HEAD 'origin/main'") && strings.Contains(execs, "reset -q --soft"), "no soft reset to the merge base"),
	}
}

// reviewRecordMetrics checks the state file, the seeded to-dos and the
// ledger row.
func reviewRecordMetrics(h *Harness, id string) []MetricResult {
	st, err := review.LoadState(h.ReviewDir(), id)
	stateOK := err == nil && st.HeadSHA == reviewHead && st.Ref == reviewRef && !st.Revealed
	list, err := h.srv.TodoAccess().Get(id)
	var texts []string
	if err == nil {
		for _, it := range list.Items {
			texts = append(texts, it.Text)
		}
	}
	want := []string{"Blind review", "Informed pass", "Verify findings", "Publish the brief", "Ask the three questions", "Post the review"}
	started := slices.ContainsFunc(h.Events(id), func(ev domain.SessionEvent) bool { return ev.Kind == domain.EventReviewStarted })
	return []MetricResult{
		score("state_has_head", stateOK, "state missing or wrong"),
		score("review_todos_seeded", slices.Equal(texts, want), "to-dos "+strings.Join(texts, ", ")),
		score("review_started_in_ledger", started, "no review.started row"),
	}
}

// CaseReviewBlind starts a review of o/r#7. The worktree is the PR head
// staged against its merge base, serve writes the diff, the changed files and
// the metadata but not the description, the review to-dos are seeded, and a
// second review of the same PR is refused while the first lives.
func CaseReviewBlind() Case {
	return Case{Name: "review-blind", Assert: func(h *Harness) []MetricResult {
		id, out := startReview(h)
		if id == "" {
			return out
		}
		out = append(out, reviewFilesMetrics(h, id)...)
		out = append(out, reviewGitMetrics(h)...)
		out = append(out, reviewRecordMetrics(h, id)...)
		resp, err := h.Call("session.create", map[string]any{"review": reviewRef})
		refused := err != nil && resp.Error != nil && resp.Error.Code == "review_exists"
		out = append(out, score("second_review_refused", refused, errText(err)))
		return append(out, revealAfterConsult(h, id)...)
	}}
}

// revealCode is the error code of a review.reveal by caller, or "" when it
// succeeded.
func revealCode(h *Harness, caller string, params map[string]any) string {
	resp, err := h.CallAs(caller, "review.reveal", params)
	if err == nil || resp.Error == nil {
		return ""
	}
	return resp.Error.Code
}

// tickBlind marks the review's Blind review to-do done as its agent.
func tickBlind(h *Harness, id string) error {
	return h.srv.TodoAccess().Mutate(context.Background(), id, domain.AuthorAgent, func(l *domain.TodoList) error {
		for i := range l.Items {
			if l.Items[i].Ref == "review:blind" {
				l.Items[i].State = domain.TodoDone
			}
		}
		return nil
	})
}

// revealedState scores the state file and the review.revealed row after a
// reveal.
func revealedState(h *Harness, id string, single bool) []MetricResult {
	st, err := review.LoadState(h.ReviewDir(), id)
	var payload map[string]any
	for _, ev := range h.Events(id) {
		if ev.Kind == domain.EventReviewRevealed {
			payload = map[string]any{}
			_ = json.Unmarshal(ev.Payload, &payload)
		}
	}
	return []MetricResult{
		score("state_revealed", err == nil && st.Revealed && st.Single == single, fmt.Sprintf("state %+v err %v", st, err)),
		score("revealed_in_ledger", payload != nil && payload["single"] == single, fmt.Sprintf("review.revealed %v", payload)),
	}
}

// revealAfterConsult runs the blind consult: reveal is refused before the
// blind to-do is done and while the consult runs, and allowed once its report
// settles. The description lands in the scratch directory, never in the PR
// worktree.
func revealAfterConsult(h *Harness, id string) []MetricResult {
	out := []MetricResult{score("reveal_refused_before_blind",
		revealCode(h, id, map[string]any{}) == "review_not_ready", "reveal before the blind to-do")}
	if err := tickBlind(h, id); err != nil {
		return append(out, score("blind_ticked", false, err.Error()))
	}
	if err := h.SeedTodos(id, domain.TodoItem{ID: "bc", Text: "Blind consult", State: domain.TodoPending}); err != nil {
		return append(out, score("consult_todo", false, err.Error()))
	}
	resp, err := h.CallAs(id, "session.create", map[string]any{"for": "bc", "consult": true, "brief": consultBriefText})
	if err != nil {
		return append(out, score("consult_created", false, err.Error()))
	}
	var res struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	raw, _ := json.Marshal(resp.Result)
	_ = json.Unmarshal(raw, &res)
	consult, err := h.Repo.Get(context.Background(), res.Session.ID)
	if err != nil {
		return append(out, score("consult_created", false, err.Error()))
	}
	out = append(out, score("reveal_waits_for_consult",
		revealCode(h, id, map[string]any{"single": true}) == "review_not_ready", "reveal while the consult runs"))
	if err := os.WriteFile(filepath.Join(consult.WorkingDirectory, domain.AimanReportFileName), []byte(consultReportText), 0o600); err != nil {
		return append(out, score("consult_report", false, err.Error()))
	}
	h.TrackOnce()
	out = append(out, score("reveal_after_consult", revealCode(h, id, map[string]any{}) == "", "reveal after consult.finished"))
	sess, err := h.Repo.Get(context.Background(), id)
	if err != nil {
		return append(out, score("review_row", false, err.Error()))
	}
	desc, descErr := os.ReadFile(filepath.Join(sess.WorkingDirectory, review.DescriptionFileName))
	_, inPR := os.Stat(filepath.Join(sess.WorktreePath, review.DescriptionFileName))
	out = append(out,
		score("description_written", descErr == nil && strings.Contains(string(desc), "# Add the parser") && strings.Contains(string(desc), "Linked issues: PROJ-1"),
			fmt.Sprintf("description %q err %v", desc, descErr)),
		score("description_not_in_pr_worktree", os.IsNotExist(inPR), "description written into the PR worktree"),
	)
	return append(out, revealedState(h, id, false)...)
}

// CaseReviewSingle reveals a review no consult took part in: refused without
// single, allowed with it, and recorded as a one-reviewer review in the state
// file and the ledger.
func CaseReviewSingle() Case {
	return Case{Name: "review-single", Assert: func(h *Harness) []MetricResult {
		id, out := startReview(h)
		if id == "" {
			return out
		}
		if err := tickBlind(h, id); err != nil {
			return append(out, score("blind_ticked", false, err.Error()))
		}
		out = append(out,
			score("reveal_needs_single", revealCode(h, id, map[string]any{}) == "review_not_ready", "reveal without a consult or single"),
			score("reveal_single", revealCode(h, id, map[string]any{"single": true}) == "", "reveal with single"),
		)
		return append(out, revealedState(h, id, true)...)
	}}
}
