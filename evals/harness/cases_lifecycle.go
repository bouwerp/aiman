package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/handover"
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
	res := Result{Name: c2.Name, Metrics: metrics, Execs: h.Execs()}
	res.Passed = true
	for _, m := range metrics {
		if m.Score < 1 {
			res.Passed = false
		}
	}
	return res
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
		ok := waitUntil(5*time.Second, func() bool { return countSendKeys(h.Execs()) >= 2 })
		if !ok {
			return []MetricResult{score("prompt_submitted_once", false, "note was not delivered")}
		}
		time.Sleep(1200 * time.Millisecond)
		return []MetricResult{
			score("prompt_submitted_once", countSendKeys(h.Execs()) == 2, "note was typed more than once"),
			score("no_duplicate_steer", countSendKeys(h.Execs()) == 2, "duplicate delivery"),
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
		held := countSendKeys(h.Execs()) == 0
		if !held {
			return []MetricResult{score("steer_delivered_after_ready", false, "delivered while held")}
		}
		if err := h.Report([]byte(`{"id":"s1","event":"Stop","state":"idle","source":"idle_prompt"}`)); err != nil {
			return []MetricResult{score("idle_report", false, err.Error())}
		}
		delivered := waitUntil(5*time.Second, func() bool { return countSendKeys(h.Execs()) >= 2 })
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

// LifecycleCases is the deterministic suite Task 4 runs.
func LifecycleCases() []Case {
	return []Case{
		CasePromptOnce(),
		CaseSteerHeldDialog(),
		CaseSteerHeldWorking(),
		CaseTodoMirror(),
		CaseHandoverRestart(),
		CaseHandoverPrecompact(),
		CaseEventSequence(),
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

func countSendKeys(execs []string) int {
	n := 0
	for _, cmd := range execs {
		if strings.Contains(cmd, "tmux send-keys") {
			n++
		}
	}
	return n
}
