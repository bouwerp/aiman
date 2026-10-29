package harness

import (
	"context"
	"encoding/json"

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
