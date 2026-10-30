package harness

import (
	"encoding/json"
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

// reviewFilesMetrics checks the blind inputs serve wrote: diff, files and meta
// present, the description absent.
func reviewFilesMetrics(h *Harness, id string) []MetricResult {
	wt := h.RepoDir(id)
	present := true
	for _, name := range []string{"diff.patch", "files.txt", "meta.json"} {
		if _, err := os.Stat(filepath.Join(wt, ".aiman_review", name)); err != nil {
			present = false
		}
	}
	var meta struct {
		Number  int    `json:"number"`
		HeadSHA string `json:"head_sha"`
	}
	raw, _ := os.ReadFile(filepath.Join(wt, ".aiman_review", "meta.json"))
	_ = json.Unmarshal(raw, &meta)
	_, descErr := os.Stat(filepath.Join(wt, ".aiman_pr_description.md"))
	return []MetricResult{
		score("worktree_named_for_pr", filepath.Base(wt) == "r@pr-7", "worktree "+wt),
		score("review_files_present", present, "missing a file under .aiman_review"),
		score("meta_has_head", meta.Number == 7 && meta.HeadSHA == reviewHead, "meta "+string(raw)),
		score("meta_has_no_description", !strings.Contains(string(raw), "PROJ-1"), "meta carries the description"),
		score("description_absent", os.IsNotExist(descErr), "description written before reveal"),
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
		return append(out, score("second_review_refused", refused, errText(err)))
	}}
}
