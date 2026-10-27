package harness

import (
	"os"
	"path/filepath"

	"github.com/bouwerp/aiman/internal/domain"
)

// GoodOracle is the suite a correct harness should pass.
func GoodOracle() []Case { return LifecycleCases() }

// BrokenOracle deliberately violates two invariants. It exists to prove the
// metrics can fail: a broken agent must not score like a good one.
func BrokenOracle() []Case {
	return []Case{
		{
			Name:  "prompt-duplicate",
			Setup: idleSession,
			Assert: func(h *Harness) []MetricResult {
				for _, text := range []string{"one", "two"} {
					if _, err := h.Call("session.prompt", map[string]any{"id": "s1", "text": text, "force": true}); err != nil {
						return []MetricResult{score("prompt_sent", false, err.Error())}
					}
				}
				return nil
			},
		},
		{
			Name:  "handover-missing-evidence",
			Setup: idleSession,
			Assert: func(h *Harness) []MetricResult {
				path := filepath.Join(h.Dir, "sessions", "s1", domain.AimanHandoverFileName)
				broken := "# Handover: s1\n\n## Goal\n\nx\n\n## Constraints & Preferences\n\nx\n\n## Progress\n\nx\n"
				if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
					return []MetricResult{score("broken_handover", false, err.Error())}
				}
				return nil
			},
		},
	}
}
