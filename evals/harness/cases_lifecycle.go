package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/handover"
	"github.com/bouwerp/aiman/internal/handoverchild"
	"github.com/bouwerp/aiman/internal/ptyruntime/ptyfake"
	"github.com/bouwerp/aiman/internal/server"
)

// MetricResult is one deterministic check. Score is 0 or 1.
type MetricResult struct {
	Name   string
	Score  float64
	Reason string
}

// Result is one case's outcome.
type Result struct {
	Name    string
	Passed  bool
	Metrics []MetricResult
	Events  []domain.SessionEvent
	Execs   []string
	Writes  []ptyfake.Write
	Files   map[string]string
	Err     error
}

// Case is one scripted session scenario.
type Case struct {
	Name   string
	Setup  func(*Harness) error
	Assert func(*Harness) []MetricResult
}

// Run starts a fresh harness for the case, runs it, and closes the harness.
func (c Case) Run(t *testing.T) Result {
	c2 := c
	h := New(t)
	defer h.Close()
	if c2.Setup != nil {
		if err := c2.Setup(h); err != nil {
			return Result{Name: c2.Name, Err: err}
		}
	}
	metrics := c2.Assert(h)
	res := Result{Name: c2.Name, Metrics: metrics, Events: h.Events("s1"), Execs: h.Execs(), Writes: h.Writes(), Files: h.Artifacts()}
	res.Passed = true
	for _, m := range metrics {
		if m.Score < 1 {
			res.Passed = false
		}
	}
	return Evaluate(res)
}

func waitUntil(timeout time.Duration, fn func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fn()
}

func score(name string, ok bool, reason string) MetricResult {
	if ok {
		return MetricResult{Name: name, Score: 1}
	}
	return MetricResult{Name: name, Reason: reason}
}

func idleSession(h *Harness) error {
	h.RegisterSession(domain.Session{
		ID: "s1", HookState: domain.AgentStateIdle, HookStateAt: time.Now().UTC(),
		HookStateSource: agenthook.SourceIdlePrompt,
	})
	return nil
}

// CasePromptOnce steers an idle session and expects the note to be typed and
// submitted exactly once.
func CasePromptOnce() Case {
	return Case{Name: "prompt-once", Setup: idleSession, Assert: func(h *Harness) []MetricResult {
		if _, err := h.Call("steer.send", map[string]any{"session": "s1", "text": "go"}); err != nil {
			return []MetricResult{score("steer_sent", false, err.Error())}
		}
		ok := waitUntil(5*time.Second, func() bool { return countSendKeys(h.Writes()) >= 2 })
		if !ok {
			return []MetricResult{score("prompt_submitted_once", false, "note was not delivered")}
		}
		time.Sleep(1200 * time.Millisecond)
		return []MetricResult{
			score("prompt_submitted_once", countSendKeys(h.Writes()) == 2, "note was typed more than once"),
			score("no_duplicate_steer", countSendKeys(h.Writes()) == 2, "duplicate delivery"),
		}
	}}
}

func heldSteerCase(name, heldState string) Case {
	return Case{Name: name, Setup: func(h *Harness) error {
		h.RegisterSession(domain.Session{
			ID: "s1", HookState: domain.AgentState(heldState), HookStateAt: time.Now().UTC(),
		})
		return nil
	}, Assert: func(h *Harness) []MetricResult {
		if _, err := h.Call("steer.send", map[string]any{"session": "s1", "text": "go"}); err != nil {
			return []MetricResult{score("steer_sent", false, err.Error())}
		}
		time.Sleep(1200 * time.Millisecond)
		held := countSendKeys(h.Writes()) == 0
		if !held {
			return []MetricResult{score("steer_delivered_after_ready", false, "delivered while held")}
		}
		if err := h.Report([]byte(`{"id":"s1","event":"Stop","state":"idle","source":"idle_prompt"}`)); err != nil {
			return []MetricResult{score("idle_report", false, err.Error())}
		}
		delivered := waitUntil(5*time.Second, func() bool { return countSendKeys(h.Writes()) >= 2 })
		return []MetricResult{
			score("steer_delivered_after_ready", delivered, "note was not delivered after idle"),
		}
	}}
}

// CaseSteerHeldDialog holds a note behind a waiting_input dialog.
func CaseSteerHeldDialog() Case { return heldSteerCase("steer-held-dialog", "waiting_input") }

// CaseSteerHeldWorking holds a note while the agent works.
func CaseSteerHeldWorking() Case { return heldSteerCase("steer-held-working", "working") }

// CaseTodoMirror checks the agent's status-only mirror keeps the user text.
func CaseTodoMirror() Case {
	return Case{Name: "todo-mirror", Setup: idleSession, Assert: func(h *Harness) []MetricResult {
		if _, err := h.Call("todo.add", map[string]any{"session": "s1", "text": "Keep me", "as": "user"}); err != nil {
			return []MetricResult{score("todo_add", false, err.Error())}
		}
		if _, err := h.Call("todo.mirror", map[string]any{"session": "s1", "items": []map[string]any{
			{"text": "Keep me", "status": "completed"},
			{"text": "Agent item", "status": "pending"},
		}}); err != nil {
			return []MetricResult{score("todo_mirror", false, err.Error())}
		}
		texts := todoTexts(h)
		return []MetricResult{
			score("todo_mirror_preserves_user_text", contains(texts, "Keep me"), "user item disappeared"),
			score("todo_mirror_adds_agent_item", contains(texts, "Agent item"), "agent item missing"),
		}
	}}
}

// CaseTodoUserEditSteers checks a user change steers once and an agent change
// does not.
func CaseTodoUserEditSteers() Case {
	return Case{Name: "todo-user-edit", Setup: idleSession, Assert: func(h *Harness) []MetricResult {
		if _, err := h.Call("todo.add", map[string]any{"session": "s1", "text": "Fix bug", "as": "user"}); err != nil {
			return []MetricResult{score("todo_add", false, err.Error())}
		}
		delivered := waitUntil(5*time.Second, func() bool { return typedSteers(h.Writes()) >= 1 })
		before := typedSteers(h.Writes())
		if _, err := h.Call("todo.mirror", map[string]any{"session": "s1", "items": []map[string]any{
			{"text": "Fix bug", "status": "completed"},
		}}); err != nil {
			return []MetricResult{score("todo_mirror", false, err.Error())}
		}
		time.Sleep(1200 * time.Millisecond)
		after := typedSteers(h.Writes())
		changed := 0
		for _, ev := range h.Events("s1") {
			if ev.Kind == domain.EventTodoChanged {
				changed++
			}
		}
		return []MetricResult{
			score("user_edit_steers_once", delivered && before == 1, "user edit did not steer exactly once"),
			score("agent_change_does_not_steer", after == before, "agent mirror steered"),
			score("todo_changed_recorded", changed >= 1, "todo.changed event missing"),
		}
	}}
}

// CaseHandoverRestart checks the fixed skeleton and the restart pointer.
func CaseHandoverRestart() Case {
	return Case{Name: "handover-restart", Setup: idleSession, Assert: func(h *Harness) []MetricResult {
		resp, err := h.Call("handover.write", map[string]any{"session": "s1", "reason": "restart"})
		if err != nil {
			return []MetricResult{score("handover_write", false, err.Error())}
		}
		m, _ := resp.Result.(map[string]any)
		path, _ := m["path"].(string)
		raw, rerr := os.ReadFile(path)
		missing := []string{"read failed"}
		if rerr == nil {
			missing = handover.MissingSections(string(raw))
		}
		point, perr := h.Call("handover.point", map[string]any{"session": "s1"})
		queued := false
		if perr == nil {
			pm, _ := point.Result.(map[string]any)
			queued, _ = pm["queued"].(bool)
		}
		return []MetricResult{
			score("handover_sections_complete", rerr == nil && len(missing) == 0, strings.Join(missing, ", ")),
			score("handover_pointer_queued", queued, "pointer was not queued"),
		}
	}}
}

// CaseHandoverPrecompact checks PreCompact writes the file and a compaction
// pointer can be queued.
func CaseHandoverPrecompact() Case {
	return Case{Name: "handover-precompact", Setup: idleSession, Assert: func(h *Harness) []MetricResult {
		if err := h.Report([]byte(`{"id":"s1","event":"PreCompact","state":"working"}`)); err != nil {
			return []MetricResult{score("precompact_report", false, err.Error())}
		}
		if _, err := os.Stat(filepath.Join(h.Dir, "sessions", "s1", domain.AimanHandoverFileName)); err != nil {
			return []MetricResult{score("handover_precompact_written", false, err.Error())}
		}
		resp, err := h.Call("handover.point", map[string]any{"session": "s1", "reason": "pre_compact"})
		queued := false
		if err == nil {
			m, _ := resp.Result.(map[string]any)
			queued, _ = m["queued"].(bool)
		}
		return []MetricResult{
			score("handover_precompact_written", true, ""),
			score("compaction_pointer_queued", queued, "compaction pointer was not queued"),
		}
	}}
}

// childHandover is a valid handover a case writes as the child's output.
const childHandover = `# Handover: s1

## Goal

Ship the retry fix.

## Constraints & Preferences

Keep the public API.

## Progress

Retry loop written; tests pending.

## Key Decisions

Back off exponentially, capped at 30s.

## Next Steps

Run go test ./... and open the PR.

## Critical Context

The flaky test is TestRetryBackoff.

## Evidence

None yet.

## Verification

go test ./... must pass.
`

// childMemories holds two valid candidates and one the filter drops.
const childMemories = `{"candidates":[
  {"type":"decision","title":"Exponential backoff","abstract":"Retries back off.","body":"Capped at 30s."},
  {"type":"pitfall","title":"Flaky TestRetryBackoff","abstract":"Timing-sensitive.","body":"Use a fake clock."},
  {"type":"decision","title":"  "}
]}`

// workingSession keeps steering notes queued, so a case can read them.
func workingSession(h *Harness) {
	h.RegisterSession(domain.Session{
		ID: "s1", AgentName: "claude", HookState: domain.AgentStateWorking, HookStateAt: time.Now().UTC(),
	})
}

// startPreCompactChild writes a pre_compact handover, which starts a child,
// then asks for the compaction pointer. It returns the run id and the
// mechanical file; an empty run id means the metrics hold the failure.
func startPreCompactChild(h *Harness) (run string, mechanical []byte, metrics []MetricResult) {
	resp, err := h.Call("handover.write", map[string]any{"session": "s1", "reason": "pre_compact"})
	if err != nil {
		return "", nil, []MetricResult{score("handover_write", false, err.Error())}
	}
	m, _ := resp.Result.(map[string]any)
	run, _ = m["child"].(string)
	path, _ := m["path"].(string)
	mechanical, rerr := os.ReadFile(path)
	if run == "" || rerr != nil {
		return "", nil, []MetricResult{score("handover_child_started", false, fmt.Sprintf("child %q, read %v", run, rerr))}
	}
	point, err := h.Call("handover.point", map[string]any{"session": "s1", "reason": "pre_compact"})
	accepted := false
	if err == nil {
		pm, _ := point.Result.(map[string]any)
		accepted, _ = pm["queued"].(bool)
	}
	time.Sleep(100 * time.Millisecond)
	held := accepted && h.HandoverActive("s1") && len(h.SteerPending("s1")) == 0
	return run, mechanical, []MetricResult{
		score("handover_child_started", true, ""),
		score("pointer_held_while_running", held, "pointer was not held while the child ran"),
	}
}

func pointerQueued(h *Harness) bool {
	return waitUntil(5*time.Second, func() bool {
		for _, n := range h.SteerPending("s1") {
			if n.Text == server.HandoverCompactionNote {
				return true
			}
		}
		return false
	})
}

func memoryJobCandidates(h *Harness) (jobs, candidates int, err error) {
	list, err := h.MemoryJobs("s1")
	if err != nil {
		return 0, 0, err
	}
	for _, j := range list {
		candidates += len(j.Candidates)
	}
	return len(list), candidates, nil
}

func childFinished(h *Harness, status handoverchild.Status) bool {
	for _, ev := range h.Events("s1") {
		if ev.Kind != domain.EventHandoverChildFinished {
			continue
		}
		var payload struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(ev.Payload, &payload) == nil && payload.Status == string(status) {
			return true
		}
	}
	return false
}

// CaseHandoverChild runs a pre_compact handover child to completion: the
// pointer is held while it runs, its handover replaces the mechanical file,
// its candidates reach the memory job, and the pointer is then queued.
func CaseHandoverChild() Case {
	return Case{Name: "handover-child", Setup: func(h *Harness) error {
		workingSession(h)
		return h.EnableHandovers(30 * time.Second)
	}, Assert: func(h *Harness) []MetricResult {
		run, _, metrics := startPreCompactChild(h)
		if run == "" {
			return metrics
		}
		if err := h.WriteHandoverOutputs(run, childHandover, childMemories); err != nil {
			return append(metrics, score("child_outputs_written", false, err.Error()))
		}
		r, done := h.WaitHandoverRun(run)
		got, rerr := os.ReadFile(filepath.Join(h.Dir, "sessions", "s1", domain.AimanHandoverFileName))
		applied := rerr == nil && string(got) == domain.AimanScaffoldHeader+childHandover
		jobs, candidates, jerr := memoryJobCandidates(h)
		return append(metrics,
			score("child_run_complete", done && r.Status == handoverchild.StatusComplete, fmt.Sprintf("run %q cleaned %v", r.Status, r.CleanedUp)),
			score("child_handover_applied", applied, "worker handover is not the child's"),
			score("memory_job_has_candidates", jerr == nil && jobs == 1 && candidates == 2,
				fmt.Sprintf("%d jobs, %d candidates, %v", jobs, candidates, jerr)),
			score("compaction_pointer_queued", pointerQueued(h), "pointer was not released"),
			score("child_finished_recorded", childFinished(h, handoverchild.StatusComplete), "handover.child_finished missing"),
		)
	}}
}

// CaseHandoverChildFallback lets a child time out: the mechanical file stays,
// the pointer is released, and the memory job carries no candidates.
func CaseHandoverChildFallback() Case {
	return Case{Name: "handover-child-fallback", Setup: func(h *Harness) error {
		workingSession(h)
		return h.EnableHandovers(time.Second)
	}, Assert: func(h *Harness) []MetricResult {
		run, mechanical, metrics := startPreCompactChild(h)
		if run == "" {
			return metrics
		}
		r, done := h.WaitHandoverRun(run)
		got, rerr := os.ReadFile(filepath.Join(h.Dir, "sessions", "s1", domain.AimanHandoverFileName))
		jobs, candidates, jerr := memoryJobCandidates(h)
		return append(metrics,
			score("child_run_timed_out", done && r.Status == handoverchild.StatusTimedOut, fmt.Sprintf("run %q cleaned %v", r.Status, r.CleanedUp)),
			score("mechanical_handover_kept", rerr == nil && bytes.Equal(got, mechanical), "worker handover changed"),
			score("memory_job_without_candidates", jerr == nil && jobs == 1 && candidates == 0,
				fmt.Sprintf("%d jobs, %d candidates, %v", jobs, candidates, jerr)),
			score("compaction_pointer_queued", pointerQueued(h), "pointer was not released"),
			score("child_finished_recorded", childFinished(h, handoverchild.StatusTimedOut), "handover.child_finished missing"),
		)
	}}
}

// CaseTerminateKeepsPane stops the terminal but leaves the row and the last
// capture in place; archive or forget removes the row.
func CaseTerminateKeepsPane() Case {
	return Case{Name: "terminate-keeps-pane", Setup: func(h *Harness) error {
		h.SetPane("last screen")
		h.RegisterSession(domain.Session{ID: "s1"})
		return nil
	}, Assert: func(h *Harness) []MetricResult {
		sess, err := h.Repo.Get(context.Background(), "s1")
		if err != nil {
			return []MetricResult{score("session_row", false, err.Error())}
		}
		if err := h.TerminateSession(*sess); err != nil {
			return []MetricResult{score("terminate", false, err.Error())}
		}
		_, rowErr := h.Repo.Get(context.Background(), "s1")
		kills := h.Kills()
		issued := len(kills) > 0 && kills[0] == "s1"
		return []MetricResult{
			score("terminate_issued", issued, fmt.Sprintf("pty kills %v, want s1", kills)),
			score("session_row_kept", rowErr == nil, "session row disappeared"),
			score("pane_snapshot_kept", h.Pane() == "last screen", "pane snapshot changed"),
		}
	}}
}

// CaseEventSequence checks the ledger is contiguous and hashed.
func CaseEventSequence() Case {
	return Case{Name: "event-sequence", Setup: func(h *Harness) error {
		if _, err := h.Call("session.register", map[string]any{"sessions": []map[string]any{{"id": "s1", "name": "s1"}}}); err != nil {
			return err
		}
		for _, state := range []string{"idle", "working"} {
			if err := h.Report([]byte(`{"id":"s1","event":"Stop","state":"` + state + `"}`)); err != nil {
				return err
			}
		}
		return nil
	}, Assert: func(h *Harness) []MetricResult {
		events := h.Events("s1")
		contiguous := len(events) > 0
		for i, ev := range events {
			if ev.Seq != int64(i+1) {
				contiguous = false
			}
			if ev.Hash == "" {
				contiguous = false
			}
		}
		return []MetricResult{score("event_sequence_contiguous", contiguous, "sequence or hash is broken")}
	}}
}

// CaseCreateTicket checks creation writes the task file and the first event,
// without creating a to-do list.
func CaseCreateTicket() Case {
	return Case{Name: "create-ticket", Assert: func(h *Harness) []MetricResult {
		resp, err := h.Call("session.create", map[string]any{"name": "s1", "agent": "claude", "quick": true})
		if err != nil {
			return []MetricResult{score("session_create", false, err.Error())}
		}
		m, _ := resp.Result.(map[string]any)
		info, _ := m["session"].(map[string]any)
		id, _ := info["id"].(string)
		wd, _ := info["working_directory"].(string)
		_, taskErr := os.Stat(filepath.Join(wd, domain.AimanTaskFileName))
		created := false
		for _, ev := range h.Events(id) {
			if ev.Kind == domain.EventSessionCreated {
				created = true
			}
		}
		_, todoErr := os.Stat(filepath.Join(h.Dir, "todos", id+".json"))
		return []MetricResult{
			score("session_created_event", created, "missing session.created"),
			score("task_file_written", taskErr == nil, "missing .aiman_task.md"),
			score("no_todo_list_yet", os.IsNotExist(todoErr), "to-do list exists"),
		}
	}}
}

// LifecycleCases is the deterministic suite Task 4 runs.
func LifecycleCases() []Case {
	return []Case{
		CaseCreateTicket(),
		CasePromptOnce(),
		CaseSteerHeldDialog(),
		CaseSteerHeldWorking(),
		CaseTodoMirror(),
		CaseTodoUserEditSteers(),
		CaseHandoverRestart(),
		CaseHandoverPrecompact(),
		CaseHandoverChild(),
		CaseHandoverChildFallback(),
		CaseTerminateKeepsPane(),
		CaseEventSequence(),
		CaseChildSpawn(),
		CaseChildNoEvidence(),
		CaseChildVanished(),
		CaseDesignGate(),
		CaseConsultClosesOnReport(),
		CaseBriefStructure(),
		CaseChildPromptNoWait(),
	}
}

func todoTexts(h *Harness) []string {
	resp, err := h.Call("todo.get", map[string]any{"session": "s1"})
	if err != nil {
		return nil
	}
	m, _ := resp.Result.(map[string]any)
	list, _ := m["list"].(map[string]any)
	items, _ := list["items"].([]any)
	out := make([]string, 0, len(items))
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if text, _ := item["text"].(string); text != "" {
			out = append(out, text)
		}
	}
	return out
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

// countSendKeys counts every write into a terminal: a delivered note is two,
// the text and the Return that submits it.
func countSendKeys(writes []ptyfake.Write) int {
	return len(writes)
}
