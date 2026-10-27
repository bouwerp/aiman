package harness

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"

	"github.com/bouwerp/aiman/internal/domain"
)

const usageContextFixture = `{"type":"assistant","timestamp":"2026-10-21T07:25:15.630Z","message":{"id":"msg_1","model":"claude-opus-5-5","usage":{"input_tokens":10,"cache_read_input_tokens":5,"cache_creation_input_tokens":100,"output_tokens":1}}}
{"type":"assistant","timestamp":"2026-10-21T07:26:15.630Z","message":{"id":"msg_2","model":"claude-opus-5-5","usage":{"input_tokens":20,"cache_read_input_tokens":7,"cache_creation_input_tokens":900,"output_tokens":1}}}
`

const usageBudgetFixture = `{"type":"assistant","timestamp":"2026-10-21T07:25:15.630Z","message":{"id":"budget_1","model":"claude-opus-5-5","usage":{"input_tokens":80,"output_tokens":0}}}
`

const usageBudgetExtra = `{"type":"assistant","timestamp":"2026-10-21T07:26:15.630Z","message":{"id":"budget_2","model":"claude-opus-5-5","usage":{"input_tokens":30,"output_tokens":0}}}
`

// CaseUsageBudget checks threshold events fire once each and usage.get reports
// the same used tokens and state.
func CaseUsageBudget() Case {
	transcript := func(h *Harness) string {
		return filepath.Join(h.Dir, ".claude", "projects", "p", "budget.jsonl")
	}
	return Case{Name: "usage-budget", Setup: func(h *Harness) error {
		path := transcript(h)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(usageBudgetFixture), 0o600); err != nil {
			return err
		}
		h.RegisterSession(domain.Session{
			ID: "s1", Name: "s1", AgentName: "claude", AgentSessionPath: path, BudgetTokens: 100,
		})
		return h.EnableUsage(0)
	}, Assert: func(h *Harness) []MetricResult {
		warnBudget, err := usageBudgetOf(h)
		if err != nil {
			return []MetricResult{score("usage_budget_get", false, err.Error())}
		}
		warnEvents := budgetEventStates(h)
		f, err := os.OpenFile(transcript(h), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return []MetricResult{score("usage_budget_append", false, err.Error())}
		}
		_, writeErr := f.WriteString(usageBudgetExtra)
		closeErr := f.Close()
		if writeErr != nil {
			return []MetricResult{score("usage_budget_append", false, writeErr.Error())}
		}
		if closeErr != nil {
			return []MetricResult{score("usage_budget_append", false, closeErr.Error())}
		}
		if err := h.ScanUsage(); err != nil {
			return []MetricResult{score("usage_budget_scan", false, err.Error())}
		}
		overBudget, err := usageBudgetOf(h)
		if err != nil {
			return []MetricResult{score("usage_budget_get", false, err.Error())}
		}
		allEvents := budgetEventStates(h)
		warnPct, _ := warnBudget["pct"].(float64)
		overPct, _ := overBudget["pct"].(float64)
		return []MetricResult{
			score("usage_budget_warn", warnBudget["state"] == "warn" &&
				warnBudget["tokens"] == float64(100) && warnBudget["used"] == float64(80) &&
				math.Abs(warnPct-0.8) <= 1e-9, "warn budget does not match 80/100"),
			score("usage_budget_warn_once", len(warnEvents) == 1 && warnEvents[0] == "warn",
				"warn event was not emitted exactly once"),
			score("usage_budget_over", overBudget["state"] == "over" &&
				overBudget["used"] == float64(110) && math.Abs(overPct-1.1) <= 1e-9,
				"over budget does not match 110/100"),
			score("usage_budget_over_once", len(allEvents) == 2 && allEvents[0] == "warn" && allEvents[1] == "over",
				"usage.budget events are not warn then over"),
		}
	}}
}

func usageBudgetOf(h *Harness) (map[string]any, error) {
	resp, err := h.Call("usage.get", map[string]any{"session": "s1"})
	if err != nil {
		return nil, err
	}
	m, _ := resp.Result.(map[string]any)
	budget, _ := m["budget"].(map[string]any)
	return budget, nil
}

func budgetEventStates(h *Harness) []string {
	var out []string
	for _, ev := range h.Events("s1") {
		if ev.Kind != domain.EventUsageBudget {
			continue
		}
		var payload struct {
			State string `json:"state"`
		}
		if json.Unmarshal(ev.Payload, &payload) == nil {
			out = append(out, payload.State)
		}
	}
	return out
}
func CaseUsageContext() Case {
	return Case{Name: "usage-context", Setup: func(h *Harness) error {
		transcript := filepath.Join(h.Dir, ".claude", "projects", "p", "s1.jsonl")
		if err := os.MkdirAll(filepath.Dir(transcript), 0o750); err != nil {
			return err
		}
		if err := os.WriteFile(transcript, []byte(usageContextFixture), 0o600); err != nil {
			return err
		}
		h.RegisterSession(domain.Session{
			ID: "s1", Name: "s1", AgentName: "claude", AgentSessionPath: transcript,
		})
		return h.EnableUsage(1000)
	}, Assert: func(h *Harness) []MetricResult {
		resp, err := h.Call("usage.get", map[string]any{"session": "s1"})
		if err != nil {
			return []MetricResult{score("usage_get", false, err.Error())}
		}
		m, _ := resp.Result.(map[string]any)
		report, _ := m["usage"].(map[string]any)
		known, _ := report["known"].(bool)
		tokens, _ := report["context_tokens"].(float64)
		window, _ := report["context_window"].(float64)
		pct, _ := report["context_pct"].(float64)
		source, _ := report["context_source"].(string)
		total, _ := report["total"].(map[string]any)
		counts, _ := total["tokens"].(map[string]any)
		return []MetricResult{
			score("usage_known", known, "usage report is not known"),
			score("usage_context_tokens", tokens == 27, "context_tokens is not the last turn's input + cache_read"),
			score("usage_context_window", window == 1000, "context_window is not the explicit window"),
			score("usage_context_pct", math.Abs(pct-0.027) <= 1e-9, "context_pct is not 27/1000"),
			score("usage_context_source", source == "explicit", "context_source is not explicit"),
			score("usage_totals", counts["input"] == float64(30) && counts["cache_read"] == float64(12),
				"the two assistant turns were not both summed"),
		}
	}}
}
