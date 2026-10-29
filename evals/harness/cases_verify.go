package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/verify"
)

func setupVerifySession(h *Harness, cfg verify.Config) error {
	dir, err := h.InitGitSession("s1")
	if err != nil {
		return err
	}
	h.RegisterSession(domain.Session{
		ID: "s1", Name: "s1", WorktreePath: dir, WorkingDirectory: dir,
		BaseBranch: "main", RepoName: "owner/repo", AgentName: "claude",
	})
	return h.EnableVerify(cfg)
}

func startAndCompleteReview(h *Harness) (verify.Review, error) {
	rv, err := h.StartVerify("s1")
	if err != nil {
		return verify.Review{}, err
	}
	if _, ok := h.WaitReview(rv.ID, verify.StatusRunning); !ok {
		return verify.Review{}, errReviewNotRunning
	}
	if err := h.WriteReview(rv.ID, "# Review\n\n"+verify.CompleteMarker+"\n"); err != nil {
		return verify.Review{}, err
	}
	done, ok := h.WaitReview(rv.ID, verify.StatusComplete)
	if !ok {
		return verify.Review{}, errReviewNotComplete
	}
	return done, nil
}

var (
	errReviewNotRunning  = &reviewWaitError{msg: "review did not reach running"}
	errReviewNotComplete = &reviewWaitError{msg: "review did not complete"}
)

type reviewWaitError struct{ msg string }

func (e *reviewWaitError) Error() string { return e.msg }

// CaseVerifyPass checks that a clean review with a passing test command is
// recorded as evidence and reaches Passed.
func CaseVerifyPass() Case {
	return Case{Name: "verify-pass", Setup: func(h *Harness) error {
		return setupVerifySession(h, verify.Config{BeforePR: true, TestCommand: "true"})
	}, Assert: func(h *Harness) []MetricResult {
		rv, err := startAndCompleteReview(h)
		if err != nil {
			return []MetricResult{score("verify_pass", false, err.Error())}
		}
		return []MetricResult{
			score("verify_pass", rv.Passed(), "review did not pass"),
			score("verify_evidence_present", rv.Evidence.Command == "true" && rv.Evidence.ExitCode == 0,
				"evidence does not name the successful command"),
		}
	}}
}

// CaseVerifyMissingEvidence checks that an unverifiable review stays open.
func CaseVerifyMissingEvidence() Case {
	return Case{Name: "verify-missing-evidence", Setup: func(h *Harness) error {
		return setupVerifySession(h, verify.Config{BeforePR: true})
	}, Assert: func(h *Harness) []MetricResult {
		rv, err := h.StartVerify("s1")
		if err != nil {
			return []MetricResult{score("verify_missing_evidence", false, err.Error())}
		}
		if _, ok := h.WaitReview(rv.ID, verify.StatusRunning); !ok {
			return []MetricResult{score("verify_missing_evidence", false, "review did not reach running")}
		}
		if err := h.WriteReview(rv.ID, "# Review\n\n"+verify.CompleteMarker+"\n"); err != nil {
			return []MetricResult{score("verify_missing_evidence", false, err.Error())}
		}
		done, ok := h.WaitReview(rv.ID, verify.StatusIncomplete)
		if !ok {
			return []MetricResult{score("verify_missing_evidence", false, "review did not become incomplete")}
		}
		return []MetricResult{
			score("verify_missing_evidence", !done.Passed() && strings.Contains(done.Error, "no test command"),
				"missing test command did not keep the review incomplete"),
		}
	}}
}

// CaseVerifyBlockedPR checks that a gate query without a review is denied and
// leaves a verify.recorded kind=gate event.
func CaseVerifyBlockedPR() Case {
	return Case{Name: "verify-blocked-pr", Setup: func(h *Harness) error {
		if err := setupVerifySession(h, verify.Config{BeforePR: true, TestCommand: "true"}); err != nil {
			return err
		}
		return nil
	}, Assert: func(h *Harness) []MetricResult {
		dir := h.RepoDir("s1")
		hash, err := verify.TreeHash(context.Background(), dir)
		if err != nil {
			return []MetricResult{score("verify_blocked_pr", false, err.Error())}
		}
		_, err = h.Call("verify.status", map[string]any{"tree_hash": hash, "gate": true})
		if err == nil {
			return []MetricResult{score("verify_blocked_pr", false, "gate allowed a tree with no review")}
		}
		recorded := false
		for _, ev := range h.Events("s1") {
			if ev.Kind == domain.EventVerifyRecorded && strings.Contains(string(ev.Payload), `"kind":"gate"`) {
				recorded = true
			}
		}
		return []MetricResult{
			score("verify_blocked_pr", true, ""),
			score("verify_gate_recorded", recorded, "gate denial was not appended to the ledger"),
		}
	}}
}

// CaseVerifyReproduceFirst checks that a base that already passes ends the
// review without spawning a reviewer.
func CaseVerifyReproduceFirst() Case {
	return Case{Name: "verify-reproduce-first", Setup: func(h *Harness) error {
		on := true
		return setupVerifySession(h, verify.Config{BeforePR: true, TestCommand: "true", Reproduce: &on})
	}, Assert: func(h *Harness) []MetricResult {
		rv, err := h.StartVerify("s1")
		if err != nil {
			return []MetricResult{score("verify_reproduce_first", false, err.Error())}
		}
		done, ok := h.WaitReview(rv.ID, verify.StatusIncomplete)
		if !ok {
			return []MetricResult{score("verify_reproduce_first", false, "review did not become incomplete")}
		}
		found := false
		for _, f := range done.Findings {
			if f.Severity == verify.SeverityRequired && f.Title == "not reproducible" {
				found = true
			}
		}
		return []MetricResult{
			score("verify_reproduce_first", found, "not reproducible finding missing"),
			score("verify_no_reviewer_spawned", done.ReviewerID == "", "a reviewer was spawned"),
		}
	}}
}

// CaseProtectedAcceptanceTests checks that a worker whose diff edits a test
// file its brief protects gets an incomplete review with a REQUIRED finding.
func CaseProtectedAcceptanceTests() Case {
	return Case{Name: "protected-acceptance-tests", Setup: func(h *Harness) error {
		if err := setupVerifySession(h, verify.Config{BeforePR: true, TestCommand: "true"}); err != nil {
			return err
		}
		task := "# Task\n\n<!-- aiman:brief -->\n## Objective\n\nAdd the b file to package a today.\n\n## Contract\n\nPackage a exports nothing new.\n\n" +
			"## Owns\n\na.go\n\n## Out of scope\n\nEverything else.\n\n## Acceptance tests\n\nProtected file b.go and `true`.\n\n" +
			"## Report must contain\n\nCommands and exit codes.\n\n## Escalate when\n\nThe shape changes.\n<!-- /aiman:brief -->\n"
		return os.WriteFile(filepath.Join(h.RepoDir("s1"), domain.AimanTaskFileName), []byte(task), 0o600)
	}, Assert: func(h *Harness) []MetricResult {
		rv, err := h.StartVerify("s1")
		if err != nil {
			return []MetricResult{score("protected_acceptance_denied", false, err.Error())}
		}
		if _, ok := h.WaitReview(rv.ID, verify.StatusRunning); !ok {
			return []MetricResult{score("protected_acceptance_denied", false, "review did not reach running")}
		}
		if err := h.WriteReview(rv.ID, "# Review\n\n"+verify.CompleteMarker+"\n"); err != nil {
			return []MetricResult{score("protected_acceptance_denied", false, err.Error())}
		}
		done, ok := h.WaitReview(rv.ID, verify.StatusIncomplete)
		if !ok {
			return []MetricResult{score("protected_acceptance_denied", false, "review did not become incomplete")}
		}
		found := false
		for _, f := range done.Findings {
			if f.Severity == verify.SeverityRequired && f.Title == "Acceptance test edited: b.go" {
				found = true
			}
		}
		return []MetricResult{
			score("protected_acceptance_denied", found && !done.Passed(), "edit of a protected acceptance test was not denied"),
		}
	}}
}
