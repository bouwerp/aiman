package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/git"
	"github.com/bouwerp/aiman/internal/infra/local"
	"github.com/bouwerp/aiman/internal/review"
	"github.com/bouwerp/aiman/internal/todo"
)

const fixEvalFile = ".aiman_review.md"

const fixEvalReview = `# Review: o/r#7

## Findings

### F1 blocking pkg/a.go:1
Verified: yes (read it)
Source: primary

Nil deref.

## Summary

One bug.
`

const fixEvalReport = "## Findings\n\nF1 fixed go test ./pkg passes\n\n## Surprises\n\nnone\n"

// fixGit runs git in dir and returns its trimmed output.
func fixGit(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput() //nolint:gosec // fixed git arguments in a temp directory
	return strings.TrimSpace(string(out)), err
}

func fixGitAll(dir string, steps ...[]string) error {
	for _, args := range steps {
		if out, err := fixGit(dir, args...); err != nil {
			return fmt.Errorf("git %v: %w: %s", args, err, out)
		}
	}
	return nil
}

// startReviewRealGit starts a review of o/r#7 over real git: an upstream with
// the PR's branch and a clone serve works from. It returns the review id, the
// fake GitHub and the PR head.
func startReviewRealGit(h *Harness) (string, *FakeGitHub, string, error) {
	h.EnableCoordinator()
	up := filepath.Join(h.Dir, "up", "o", "r")
	if err := os.MkdirAll(up, 0o750); err != nil {
		return "", nil, "", err
	}
	steps := [][]string{
		{"init", "-q", "-b", "main"}, {"config", "user.name", "Eval"}, {"config", "user.email", "eval@example.com"},
	}
	if err := fixGitAll(up, steps...); err != nil {
		return "", nil, "", err
	}
	write := func(rel, body string) error {
		p := filepath.Join(up, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			return err
		}
		return os.WriteFile(p, []byte(body), 0o600)
	}
	if err := write("main.txt", "base\n"); err != nil {
		return "", nil, "", err
	}
	if err := fixGitAll(up, []string{"add", "-A"}, []string{"commit", "-q", "-m", "base"}, []string{"checkout", "-q", "-b", "feat/parser"}); err != nil {
		return "", nil, "", err
	}
	if err := write("pkg/a.go", "package pkg\n"); err != nil {
		return "", nil, "", err
	}
	if err := fixGitAll(up, []string{"add", "-A"}, []string{"commit", "-q", "-m", "parser"}); err != nil {
		return "", nil, "", err
	}
	head, err := fixGit(up, "rev-parse", "HEAD")
	if err != nil {
		return "", nil, "", err
	}
	if err := fixGitAll(up, []string{"update-ref", "refs/pull/7/head", head}, []string{"checkout", "-q", "main"}); err != nil {
		return "", nil, "", err
	}
	clone := filepath.Join(h.Dir, "r")
	if out, err := exec.Command("git", "clone", "-q", up, clone).CombinedOutput(); err != nil { //nolint:gosec // temp directories
		return "", nil, "", fmt.Errorf("clone: %w: %s", err, out)
	}
	if err := fixGitAll(clone, []string{"remote", "set-url", "origin", "https://github.com/o/r.git"},
		[]string{"config", "url." + up + ".insteadOf", "https://github.com/o/r.git"}); err != nil {
		return "", nil, "", err
	}
	h.remote.RemoteExecutor = local.NewExecutor(h.Dir)
	gh := &FakeGitHub{}
	h.srv.SetReviewGitHub(gh)
	h.srv.SetReviewWorktrees(git.NewManager(nil))
	h.srv.SetReviewDir(h.ReviewDir())
	ref, _ := review.ParseRef(reviewRef)
	gh.SetPR(ref, review.PR{
		Number: 7, Title: "Add the parser", Author: "alice", Body: "The parser.",
		BaseRef: "main", HeadRef: "feat/parser", HeadSHA: head,
	})
	resp, err := h.Call("session.create", map[string]any{"review": reviewRef})
	if err != nil {
		return "", nil, "", err
	}
	var res struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	raw, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(raw, &res); err != nil || res.Session.ID == "" {
		return "", nil, "", fmt.Errorf("create returned no session id")
	}
	return res.Session.ID, gh, head, nil
}

// reviewScratch is the directory the review agent runs in.
func reviewScratch(h *Harness, id string) string {
	return filepath.Join(h.ReviewDir(), id, "work")
}

// CaseReviewFixPush runs one fix round: the review session starts a fixer,
// the fixer commits and pushes through serve, and the next refresh is a delta
// that keeps the description and writes fix.patch.
func CaseReviewFixPush() Case {
	return Case{Name: "review-fix-push", Assert: func(h *Harness) []MetricResult {
		id, gh, head, err := startReviewRealGit(h)
		if err != nil {
			return []MetricResult{score("review_created", false, err.Error())}
		}
		out := []MetricResult{score("review_created", true, "")}
		scratch := reviewScratch(h, id)
		// The fake creator registers the terminal under sessions/<id>, where
		// serve resolves a live session's directory, so the file goes there
		// as well as into the scratch directory.
		for _, dir := range []string{scratch, filepath.Join(h.Dir, "sessions", id)} {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return append(out, score("review_file", false, err.Error()))
			}
			if err := os.WriteFile(filepath.Join(dir, fixEvalFile), []byte(fixEvalReview), 0o600); err != nil {
				return append(out, score("review_file", false, err.Error()))
			}
		}
		if err := tickBlind(h, id); err != nil {
			return append(out, score("blind_ticked", false, err.Error()))
		}
		out = append(out, score("reveal_single", revealCode(h, id, map[string]any{"single": true}) == "", "reveal"))

		resp, err := h.CallAs(id, "review.fix", map[string]any{"file": fixEvalFile})
		if err != nil {
			return append(out, score("fix_started", false, err.Error()))
		}
		var fx struct {
			Fixer    struct{ ID string } `json:"fixer"`
			StartSHA string              `json:"start_sha"`
			Round    int                 `json:"round"`
		}
		raw, _ := json.Marshal(resp.Result)
		_ = json.Unmarshal(raw, &fx)
		row, err := h.Repo.Get(context.Background(), fx.Fixer.ID)
		if err != nil {
			return append(out, score("fixer_row", false, err.Error()))
		}
		out = append(out,
			score("fix_started", fx.StartSHA == head && fx.Round == 1, fmt.Sprintf("start %s round %d", fx.StartSHA, fx.Round)),
			score("fixer_is_not_a_review", row.Role == domain.SessionRoleFixer && row.FixPR == reviewRef && row.ReviewPR == "", "role "+string(row.Role)))
		_, err = h.CallAs(id, "review.fix", map[string]any{"file": fixEvalFile})
		out = append(out, score("second_fixer_refused", err != nil && strings.Contains(err.Error(), "review_fix_running"), fmt.Sprint(err)))

		tree := row.WorktreePath
		if err := os.WriteFile(filepath.Join(tree, "pkg", "a.go"), []byte("package pkg // fixed\n"), 0o600); err != nil {
			return append(out, score("fix_edit", false, err.Error()))
		}
		if err := fixGitAll(tree, []string{"config", "user.name", "Fixer"}, []string{"config", "user.email", "fixer@example.com"},
			[]string{"add", "-A"}, []string{"commit", "-q", "-m", "fix"}); err != nil {
			return append(out, score("fix_commit", false, err.Error()))
		}
		for _, dir := range []string{row.WorkingDirectory, filepath.Join(h.Dir, "sessions", fx.Fixer.ID)} {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return append(out, score("fix_report", false, err.Error()))
			}
			if err := os.WriteFile(filepath.Join(dir, domain.AimanReportFileName), []byte(fixEvalReport), 0o600); err != nil {
				return append(out, score("fix_report", false, err.Error()))
			}
		}
		resp, err = h.CallAs(fx.Fixer.ID, "review.push", map[string]any{})
		if err != nil {
			return append(out, score("push_accepted", false, err.Error()))
		}
		var pushed struct {
			SHA string `json:"sha"`
		}
		raw, _ = json.Marshal(resp.Result)
		_ = json.Unmarshal(raw, &pushed)
		upstream, _ := fixGit(filepath.Join(h.Dir, "up", "o", "r"), "rev-parse", "feat/parser")
		out = append(out, score("push_reached_branch", pushed.SHA != "" && upstream == pushed.SHA, "branch "+upstream+" pushed "+pushed.SHA))
		_, gone := h.Repo.Get(context.Background(), fx.Fixer.ID)
		out = append(out, score("fixer_retired", gone != nil, "fixer row survives the push"))
		ledger := false
		for _, ev := range h.Events(id) {
			if ev.Kind == domain.EventReviewFixPushed {
				ledger = true
			}
		}
		out = append(out, score("fix_pushed_in_ledger", ledger, "no review.fix_pushed row"))

		// GitHub now reports the pushed head.
		ref, _ := review.ParseRef(reviewRef)
		pr, _ := gh.PR(context.Background(), ref)
		pr.HeadSHA = pushed.SHA
		gh.SetPR(ref, pr)
		_, _ = fixGit(filepath.Join(h.Dir, "up", "o", "r"), "update-ref", "refs/pull/7/head", pushed.SHA)

		resp, err = h.CallAs(id, "review.refresh", map[string]any{})
		if err != nil {
			return append(out, score("refresh_ok", false, err.Error()))
		}
		var rf struct {
			Refreshed bool `json:"refreshed"`
			Delta     bool `json:"delta"`
		}
		raw, _ = json.Marshal(resp.Result)
		_ = json.Unmarshal(raw, &rf)
		st, _ := review.LoadState(h.ReviewDir(), id)
		patch, perr := os.ReadFile(filepath.Join(scratch, ".aiman_review", "fix.patch"))
		_, derr := os.Stat(filepath.Join(scratch, review.DescriptionFileName))
		return append(out,
			score("refresh_is_delta", rf.Refreshed && rf.Delta, fmt.Sprintf("%+v", rf)),
			score("delta_keeps_reveal_and_rounds", st.Revealed && len(st.Fixes) == 1 && st.HeadSHA == pushed.SHA, fmt.Sprintf("%+v", st)),
			score("fix_patch_written", perr == nil && strings.Contains(string(patch), "// fixed"), fmt.Sprint(perr)),
			score("description_kept", derr == nil, fmt.Sprint(derr)))
	}}
}

const approveEvalReview = `# Review: o/r#7

## Findings

### F1 nit pkg/a.go:1
Verified: yes (read it)
Source: primary
Call: agree "fine"

Rename this.

## Summary

Good to go.
`

func approveCode(h *Harness, id string) (string, string, map[string]any) {
	resp, err := h.CallAs(id, "review.approve", map[string]any{"file": "review.md"})
	if err != nil {
		if resp.Error != nil {
			return resp.Error.Code, resp.Error.Message, nil
		}
		return "error", err.Error(), nil
	}
	m := map[string]any{}
	raw, _ := json.Marshal(resp.Result)
	_ = json.Unmarshal(raw, &m)
	return "", "", m
}

// CaseReviewApprove runs assess, an approve refused at the gate, the
// operator's sign-off and the approve that goes out.
func CaseReviewApprove() Case {
	return Case{Name: "review-approve", Assert: func(h *Harness) []MetricResult {
		id, gh, out := startReviewGH(h)
		if id == "" {
			return out
		}
		gh.SetViewer("bot")
		gh.SetDecision("APPROVED")
		if err := openReviewGate(h, id, approveEvalReview); err != nil {
			return append(out, score("review_file", false, err.Error()))
		}
		if err := markRevealed(h, id); err != nil {
			return append(out, score("revealed", false, err.Error()))
		}
		code, _, _ := approveCode(h, id)
		out = append(out, score("approve_needs_assessment", code == "review_not_assessed", "code "+code))

		resp, err := h.CallAs(id, "review.assess", map[string]any{"file": "review.md"})
		if err != nil {
			return append(out, score("assess_started", false, err.Error()))
		}
		var as struct {
			Consult struct{ ID string } `json:"consult"`
			HeadSHA string              `json:"head_sha"`
		}
		raw, _ := json.Marshal(resp.Result)
		_ = json.Unmarshal(raw, &as)
		out = append(out, score("assess_started", as.Consult.ID != "" && as.HeadSHA == reviewHead, "head "+as.HeadSHA))
		code, _, _ = approveCode(h, id)
		out = append(out, score("approve_waits_for_verdict", code == "review_not_assessed", "code "+code))

		h.srv.AppendLedger(context.Background(), id, domain.EventConsultFinished, map[string]any{
			"child": as.Consult.ID, "todo": "t9", "high": 0, "medium": 0, "low": 1, "verdict": "approve",
		})
		code, _, _ = approveCode(h, id)
		out = append(out, score("approve_refused_at_gate", code == "gate_needs_operator", "code "+code),
			score("nothing_approved_yet", len(gh.Posts()) == 0, "posted before sign-off"))

		list, err := h.srv.TodoAccess().Get(id)
		gate := ""
		for _, it := range list.Items {
			if it.Ref == todo.ApproveGateRef {
				gate = it.ID
			}
		}
		if err != nil || gate == "" {
			return append(out, score("gate_opened", false, fmt.Sprint(err)))
		}
		out = append(out, score("gate_opened", true, ""))
		// An agent cannot close it: the caller is the session.
		_, aerr := h.CallAs(id, "todo.update", map[string]any{"session": id, "id": gate, "state": "done"})
		out = append(out, score("session_cannot_sign_off", aerr != nil && strings.Contains(aerr.Error(), "gate_needs_operator"), fmt.Sprint(aerr)))
		if _, err := h.CallAs("", "todo.update", map[string]any{"session": id, "id": gate, "state": "done"}); err != nil {
			return append(out, score("operator_signed_off", false, err.Error()))
		}

		code, msg, res := approveCode(h, id)
		out = append(out, score("approve_after_sign_off", code == "", code+" "+msg))
		posts := gh.Posts()
		out = append(out, score("one_approve_posted", len(posts) == 1 && posts[0].Event == "APPROVE" && posts[0].CommitID == reviewHead, fmt.Sprintf("%d posts", len(posts))))
		if len(posts) == 1 {
			out = append(out, score("body_says_signed_off", strings.Contains(posts[0].Body, "signed off"), posts[0].Body))
		}
		out = append(out, score("approval_counts", res["decision"] == "APPROVED" && res["counts"] == true, fmt.Sprint(res)))
		code, _, _ = approveCode(h, id)
		out = append(out, score("second_approve_refused", code == "review_already_approved", "code "+code))
		ledger := false
		for _, ev := range h.Events(id) {
			if ev.Kind == domain.EventReviewPRApproved {
				ledger = true
			}
		}
		return append(out, score("approved_in_ledger", ledger, "no review.pr_approved row"))
	}}
}
