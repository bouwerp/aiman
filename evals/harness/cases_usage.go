package harness

import (
	"math"
	"os"
	"path/filepath"

	"github.com/bouwerp/aiman/internal/domain"
)

const usageContextFixture = `{"type":"assistant","timestamp":"2026-10-21T07:25:15.630Z","message":{"id":"msg_1","model":"claude-opus-5-5","usage":{"input_tokens":10,"cache_read_input_tokens":5,"cache_creation_input_tokens":100,"output_tokens":1}}}
{"type":"assistant","timestamp":"2026-10-21T07:26:15.630Z","message":{"id":"msg_2","model":"claude-opus-5-5","usage":{"input_tokens":20,"cache_read_input_tokens":7,"cache_creation_input_tokens":900,"output_tokens":1}}}
`

// CaseUsageContext checks the last assistant turn's context occupancy and the
// explicit window on a real usage.get report.
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
