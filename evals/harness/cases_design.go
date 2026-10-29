package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/coordinator"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/todo"
)

const (
	gateItemID = "g1"
	opSession  = "s2"
)

// seedGate gives sessionID an open design gate with an explicit id.
func seedGate(h *Harness, sessionID string) error {
	return h.srv.TodoAccess().Mutate(context.Background(), sessionID, domain.AuthorAiman, func(l *domain.TodoList) error {
		todo.AddGate(l)
		for i := range l.Items {
			if l.Items[i].Ref == todo.DesignGateRef {
				l.Items[i].ID = gateItemID
			}
		}
		return nil
	})
}

// gateRefused scores a call that must come back with code.
func gateRefused(h *Harness, name, caller, method string, params map[string]any, code string) MetricResult {
	resp, err := h.CallAs(caller, method, params)
	if err == nil || resp.Error == nil {
		return score(name, false, method+" succeeded")
	}
	return score(name, resp.Error.Code == code, "code "+resp.Error.Code)
}

// designApprovals counts the design.approved rows on the session's ledger and
// returns the last payload's quote.
func designApprovals(h *Harness, sessionID string) (int, string) {
	n, quote := 0, ""
	for _, ev := range h.Events(sessionID) {
		if ev.Kind != domain.EventDesignApproved {
			continue
		}
		n++
		var p struct {
			Quote string `json:"quote"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		quote = p.Quote
	}
	return n, quote
}

// CaseDesignGate checks that the design gate on s1 cannot be closed or edited
// by a session, that delegation waits for it, and that approve-design with the
// operator's quote closes it and records a ledger row. A second session's gate
// closes when the operator (no caller) marks it done.
func CaseDesignGate() Case {
	return Case{Name: "design-gate", Setup: func(h *Harness) error {
		if err := coordinatorParent(h); err != nil {
			return err
		}
		h.RegisterSession(domain.Session{ID: opSession})
		if err := seedGate(h, "s1"); err != nil {
			return err
		}
		return seedGate(h, opSession)
	}, Assert: func(h *Harness) []MetricResult {
		const refused = "gate_needs_operator"
		done := map[string]any{"session": "s1", "id": gateItemID, "state": "done"}
		out := []MetricResult{
			gateRefused(h, "owner_done_refused", "s1", "todo.update", done, refused),
			gateRefused(h, "other_done_refused", opSession, "todo.update", done, refused),
			gateRefused(h, "other_approve_refused", opSession, "todo.approve_design", map[string]any{"session": "s1", "quote": "yes"}, refused),
			gateRefused(h, "for_refused_while_open", "", "session.create", map[string]any{"for": "t1", "parent": "s1"}, "design_not_approved"),
			gateRefused(h, "empty_quote_refused", "s1", "todo.approve_design", map[string]any{"session": "s1", "quote": " "}, "invalid_params"),
		}
		_, err := h.CallAs("s1", "todo.approve_design", map[string]any{"session": "s1", "quote": "go ahead"})
		out = append(out, score("approve_with_quote", err == nil, errText(err)))
		n, quote := designApprovals(h, "s1")
		out = append(out, score("approval_in_ledger", n == 1 && quote == "go ahead", "rows/quote mismatch"))
		_, err = h.CallAs("", "todo.update", map[string]any{"session": opSession, "id": gateItemID, "state": "done"})
		out = append(out, score("operator_done_succeeds", err == nil, errText(err)))
		child, fail := spawnChild(h)
		if fail != nil {
			return append(out, fail...)
		}
		return append(out, score("for_allowed_after_approval", child.ParentID == "s1", "child parent "+child.ParentID))
	}}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

const consultBriefText = "## Question\n\nWhat is wrong with this design?\n\n## Scope\n\nRead the parent worktree; edit nothing.\n\n" +
	"## Report must contain\n\nObjections, each with severity and a suggested change, and a verdict.\n"

const consultReportText = "## Objections\n\n- [high] The store has no lock. Add one.\n- [low] Rename Foo.\n\n## Verdict\n\nrevise\n"

// consultFinished returns the payload of the last consult.finished row on
// the session's ledger and how many there are.
func consultFinished(h *Harness, sessionID string) (int, map[string]any) {
	n, payload := 0, map[string]any{}
	for _, ev := range h.Events(sessionID) {
		if ev.Kind != domain.EventConsultFinished {
			continue
		}
		n++
		payload = map[string]any{}
		_ = json.Unmarshal(ev.Payload, &payload)
	}
	return n, payload
}

// CaseConsultClosesOnReport starts a consult for t1 while s1's design gate is
// open, writes its report and runs one tracker pass. t1 closes with no PR,
// s1's ledger holds consult.finished with the counts and verdict, and the
// consult's row and scratch directory are gone while its report is kept
// beside them.
func CaseConsultClosesOnReport() Case {
	return Case{Name: "consult-closes-on-report", Setup: func(h *Harness) error {
		if err := coordinatorParent(h); err != nil {
			return err
		}
		return seedGate(h, "s1")
	}, Assert: func(h *Harness) []MetricResult {
		resp, err := h.Call("session.create", map[string]any{"for": "t1", "parent": "s1", "consult": true, "brief": consultBriefText})
		if err != nil {
			return []MetricResult{score("consult_created", false, err.Error())}
		}
		var res struct {
			Session struct {
				ID string `json:"id"`
			} `json:"session"`
		}
		raw, _ := json.Marshal(resp.Result)
		if err := json.Unmarshal(raw, &res); err != nil || res.Session.ID == "" {
			return []MetricResult{score("consult_created", false, "create returned no session id")}
		}
		consult, err := h.Repo.Get(context.Background(), res.Session.ID)
		if err != nil {
			return []MetricResult{score("consult_created", false, err.Error())}
		}
		out := []MetricResult{
			score("consult_created", consult.Role == domain.SessionRoleConsult && consult.ParentID == "s1" && consult.Role.Hidden(),
				"role "+string(consult.Role)+" parent "+consult.ParentID),
		}
		if err := os.WriteFile(filepath.Join(consult.WorkingDirectory, domain.AimanReportFileName), []byte(consultReportText), 0o600); err != nil {
			return append(out, score("consult_report", false, err.Error()))
		}
		h.TrackOnce()
		state := itemState(h)
		out = append(out, score("consult_item_done_without_pr", state == domain.TodoDone, "t1 is "+string(state)))
		n, payload := consultFinished(h, "s1")
		out = append(out, score("consult_finished_in_ledger",
			n == 1 && payload["verdict"] == "revise" && payload["high"] == float64(1) && payload["low"] == float64(1),
			fmt.Sprintf("%d rows, last %v", n, payload)))
		_, rowErr := h.Repo.Get(context.Background(), consult.ID)
		_, dirErr := os.Stat(consult.WorkingDirectory)
		out = append(out, score("consult_retired", rowErr != nil && errors.Is(dirErr, fs.ErrNotExist),
			fmt.Sprintf("row err %v, dir err %v", rowErr, dirErr)))
		kept, keptErr := os.ReadFile(filepath.Join(h.Dir, "consults", consult.ID+".report.md"))
		out = append(out, score("consult_report_kept", keptErr == nil && string(kept) == consultReportText,
			fmt.Sprintf("kept %d bytes, err %v", len(kept), keptErr)))
		return out
	}}
}

const structuredBrief = "## Objective\n\nParse nested lists.\n\n## Contract\n\ndocs/specs/PROJ-1.md\n\n## Owns\n\ninternal/parse\n\n" +
	"## Out of scope\n\nThe renderer.\n\n## Acceptance tests\n\n`go test ./internal/parse/...`\n\n## Report must contain\n\nCommands and exit codes.\n\n" +
	"## Escalate when\n\nThe tree shape changes.\n"

const surprisesText = "The Store interface gained Close."

// surpriseSteers counts the surprise notes typed into s1.
func surpriseSteers(h *Harness) int {
	n := 0
	for _, w := range h.Writes() {
		if w.ID == "s1" {
			n += strings.Count(w.Data, "reported surprises that may affect other pieces")
		}
	}
	return n
}

// CaseBriefStructure checks that a brief missing ## Acceptance tests is
// refused, that a landed child whose report lacks ## Surprises leaves t1
// open, and that a report whose Surprises is not "none" closes t1 and steers
// the coordinator once.
func CaseBriefStructure() Case {
	return Case{Name: "brief-structure", Setup: coordinatorParent, Assert: func(h *Harness) []MetricResult {
		bad := strings.Replace(structuredBrief, "## Acceptance tests", "## Checks", 1)
		_, err := h.Call("session.create", map[string]any{"for": "t1", "parent": "s1", "brief": bad})
		out := []MetricResult{
			score("brief_missing_refused", err != nil && strings.Contains(err.Error(), "invalid_params"), errText(err)),
		}
		child, fail := spawnChildWith(h, map[string]any{"brief": structuredBrief})
		if fail != nil {
			return append(out, fail...)
		}
		write := func(md string) error {
			return os.WriteFile(filepath.Join(child.WorkingDirectory, domain.AimanReportFileName), []byte(md), 0o600)
		}
		const evidence = "## Evidence\n\n- internal/parse/parse.go\n- go test ./internal/parse/... exit 0\n"
		if err := write(evidence); err != nil {
			return append(out, score("child_report", false, err.Error()))
		}
		h.SetChildPR(child.ID, coordinator.PRState{Base: parentBranch, Merged: true})
		h.TrackOnce()
		noSurprises := itemState(h)
		if err := write(evidence + "\n## Surprises\n\n" + surprisesText + "\n"); err != nil {
			return append(out, score("child_report", false, err.Error()))
		}
		h.TrackOnce()
		h.TrackOnce()
		waitUntil(10*time.Second, func() bool { return !h.srv.Steerer().Status("s1").LastDeliveredAt.IsZero() })
		return append(out,
			score("no_surprises_section_open", noSurprises == domain.TodoPending, "t1 is "+string(noSurprises)+" without ## Surprises"),
			score("surprises_close_item", itemState(h) == domain.TodoDone, "t1 is "+string(itemState(h))),
			score("surprises_steer_once", surpriseSteers(h) == 1, "the pane received the surprises note "+strconv.Itoa(surpriseSteers(h))+" times"),
		)
	}}
}

// CaseChildPromptNoWait checks that a --for child's task file tells it to skip
// the design-first flow and not wait for confirmation.
func CaseChildPromptNoWait() Case {
	return Case{Name: "child-prompt-no-wait", Setup: coordinatorParent, Assert: func(h *Harness) []MetricResult {
		child, fail := spawnChildWith(h, map[string]any{"brief": structuredBrief})
		if fail != nil {
			return fail
		}
		raw, err := os.ReadFile(filepath.Join(child.WorkingDirectory, domain.AimanTaskFileName))
		if err != nil {
			return []MetricResult{score("child_task_file", false, err.Error())}
		}
		task := string(raw)
		return []MetricResult{
			score("child_skips_design", strings.Contains(task, "skip the Design first section") && strings.Contains(task, "do not wait for confirmation"), "task file lacks the child guidelines"),
			score("child_no_suffix", !strings.Contains(task, "say your call before editing"), "task file carries the design-first suffix"),
		}
	}}
}
