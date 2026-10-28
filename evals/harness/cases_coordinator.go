package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/coordinator"
	"github.com/bouwerp/aiman/internal/domain"
)

const (
	parentBranch = "feat/proj-1"
	noEvidence   = "returned no evidence"
)

// coordinatorParent registers s1 as an idle coordinator on parentBranch with
// one open item t1, and enables the tracker.
func coordinatorParent(h *Harness) error {
	h.RegisterSession(domain.Session{
		ID: "s1", Branch: parentBranch, BaseBranch: "main", RepoName: "owner/r",
		HookState: domain.AgentStateIdle, HookStateAt: time.Now().UTC(),
		HookStateSource: agenthook.SourceIdlePrompt,
	})
	h.EnableCoordinator()
	return h.SeedTodos("s1", domain.TodoItem{ID: "t1", Text: "Add the parser", State: domain.TodoPending})
}

// spawnChild runs `aiman session create --for t1 --parent s1` and returns the
// child's row.
func spawnChild(h *Harness) (domain.Session, []MetricResult) {
	resp, err := h.Call("session.create", map[string]any{"for": "t1", "parent": "s1"})
	if err != nil {
		return domain.Session{}, []MetricResult{score("child_created", false, err.Error())}
	}
	var res struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	raw, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(raw, &res); err != nil || res.Session.ID == "" {
		return domain.Session{}, []MetricResult{score("child_created", false, "create returned no session id")}
	}
	child, err := h.Repo.Get(context.Background(), res.Session.ID)
	if err != nil {
		return domain.Session{}, []MetricResult{score("child_created", false, err.Error())}
	}
	return *child, nil
}

// childLinked checks the parent/child link session.create --for makes.
func childLinked(h *Harness, child domain.Session) MetricResult {
	it, err := h.TodoItem("s1", "t1")
	if err != nil {
		return score("child_linked", false, err.Error())
	}
	switch {
	case it.Ref != "child:"+child.ID:
		return score("child_linked", false, "t1 ref is "+it.Ref)
	case child.ParentID != "s1":
		return score("child_linked", false, "child parent is "+child.ParentID)
	case child.Branch != coordinator.ChildBranch(parentBranch, "t1") || child.BaseBranch != parentBranch:
		return score("child_linked", false, "child branch "+child.Branch+" off "+child.BaseBranch)
	}
	return score("child_linked", true, "")
}

func itemState(h *Harness) domain.TodoState {
	it, err := h.TodoItem("s1", "t1")
	if err != nil {
		return ""
	}
	return it.State
}

// noEvidenceAsks counts the report asks typed into the parent. The steerer
// joins notes queued together into one typed message, so asks are counted in
// the text, not per write.
func noEvidenceAsks(h *Harness) int {
	n := 0
	for _, w := range h.Writes() {
		if w.ID == "s1" {
			n += strings.Count(w.Data, noEvidence)
		}
	}
	return n
}

// CaseChildSpawn creates a child for t1 and lands its PR. A PR on another
// base and a PR still open leave t1 open; a merge into the parent's branch
// with a report citing a file and a test command marks it done, with no
// steer to the parent.
func CaseChildSpawn() Case {
	return Case{Name: "child-spawn", Setup: coordinatorParent, Assert: func(h *Harness) []MetricResult {
		child, fail := spawnChild(h)
		if fail != nil {
			return fail
		}
		report := "## Changed\n\n- internal/parse/parse.go\n\n## Tests\n\ngo test ./internal/parse/...\n"
		if err := os.WriteFile(filepath.Join(child.WorkingDirectory, domain.AimanReportFileName), []byte(report), 0o600); err != nil {
			return []MetricResult{score("child_report", false, err.Error())}
		}
		h.SetChildPR(child.ID, coordinator.PRState{Base: "main", Merged: true})
		h.TrackOnce()
		wrongBase := itemState(h)
		h.SetChildPR(child.ID, coordinator.PRState{Base: parentBranch, Open: true})
		h.TrackOnce()
		open := itemState(h)
		h.SetChildPR(child.ID, coordinator.PRState{Base: parentBranch, Merged: true})
		h.TrackOnce()
		done := itemState(h)
		time.Sleep(2 * time.Second)
		return []MetricResult{
			childLinked(h, child),
			score("child_waits_for_land", wrongBase == domain.TodoPending && open == domain.TodoPending,
				"t1 was "+string(wrongBase)+" after a merge into main and "+string(open)+" with the PR open"),
			score("child_item_settled", done == domain.TodoDone, "t1 is "+string(done)+" after the merge"),
			score("no_evidence_steer", noEvidenceAsks(h) == 0, "the parent was asked for a report it already has"),
		}
	}}
}

// CaseChildNoEvidence lands the child's PR with no report. t1 stays open, and
// across two passes the parent's pane receives the report ask once.
func CaseChildNoEvidence() Case {
	return Case{Name: "child-no-evidence", Setup: coordinatorParent, Assert: func(h *Harness) []MetricResult {
		child, fail := spawnChild(h)
		if fail != nil {
			return fail
		}
		h.SetChildPR(child.ID, coordinator.PRState{Base: parentBranch, Merged: true})
		h.TrackOnce()
		h.TrackOnce()
		// Delivery takes the coalescing window plus the submit settle, about 5s.
		waitUntil(10*time.Second, func() bool { return !h.srv.Steerer().Status("s1").LastDeliveredAt.IsZero() })
		asks := noEvidenceAsks(h)
		state := itemState(h)
		return []MetricResult{
			childLinked(h, child),
			score("child_evidence_required", state == domain.TodoPending, "t1 is "+string(state)+" without a report"),
			score("evidence_steer_once", asks == 1, "the pane received the report ask "+strconv.Itoa(asks)+" times"),
		}
	}}
}

// CaseChildVanished forgets the child. The next pass clears t1's Ref, keeps
// it open, and notifies once.
func CaseChildVanished() Case {
	return Case{Name: "child-vanished", Setup: coordinatorParent, Assert: func(h *Harness) []MetricResult {
		child, fail := spawnChild(h)
		if fail != nil {
			return fail
		}
		if _, err := h.Call("session.forget", map[string]any{"id": child.ID}); err != nil {
			return []MetricResult{score("child_forgotten", false, err.Error())}
		}
		h.TrackOnce()
		h.TrackOnce()
		it, err := h.TodoItem("s1", "t1")
		if err != nil {
			return []MetricResult{score("child_unlinked_on_loss", false, err.Error())}
		}
		notes := h.Notified()
		return []MetricResult{
			score("child_unlinked_on_loss", it.Ref == "" && it.State == domain.TodoPending,
				"t1 is "+string(it.State)+" with ref "+it.Ref),
			score("child_loss_notified", len(notes) == 1 && strings.Contains(notes[0], child.ID),
				"notifications: "+strings.Join(notes, "; ")),
		}
	}}
}
