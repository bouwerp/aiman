package harness

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/handover"
)

// Metric is one deterministic check over a completed case. Applies keeps a
// metric out of cases whose behaviour it cannot judge, so a score is never
// inflated by an irrelevant pass.
type Metric struct {
	Name    string
	Applies func(Result) bool
	Score   func(Result) MetricResult
}

// StandardMetrics is the suite's shipping metric set. verify_evidence_present
// joins after the verifier lands.
func StandardMetrics() []Metric {
	return []Metric{
		{
			Name: "prompt_submitted_once",
			Applies: func(r Result) bool {
				return strings.HasPrefix(r.Name, "prompt") || strings.HasPrefix(r.Name, "steer-")
			},
			Score: func(r Result) MetricResult {
				n := typedSteers(r.Execs)
				return score("prompt_submitted_once", n == 1, "typed "+strconv.Itoa(n)+" times")
			},
		},
		{
			Name: "steer_delivered_after_ready",
			Applies: func(r Result) bool {
				return strings.HasPrefix(r.Name, "prompt") || strings.HasPrefix(r.Name, "steer-")
			},
			Score: func(r Result) MetricResult {
				if typedSteers(r.Execs) == 0 {
					return score("steer_delivered_after_ready", false, "note was never typed")
				}
				if strings.HasPrefix(r.Name, "steer-") && !hasIdleReport(r.Events) {
					return score("steer_delivered_after_ready", false, "no idle report opened the gate")
				}
				return score("steer_delivered_after_ready", true, "")
			},
		},
		{
			Name: "no_duplicate_steer",
			Applies: func(r Result) bool {
				return strings.HasPrefix(r.Name, "prompt") || strings.HasPrefix(r.Name, "steer-")
			},
			Score: func(r Result) MetricResult {
				n := typedSteers(r.Execs)
				return score("no_duplicate_steer", n <= 1, "typed "+strconv.Itoa(n)+" times")
			},
		},
		{
			Name: "handover_sections_complete",
			Applies: func(r Result) bool {
				_, ok := r.Files["handover"]
				return ok
			},
			Score: func(r Result) MetricResult {
				missing := handover.MissingSections(r.Files["handover"])
				return score("handover_sections_complete", len(missing) == 0, strings.Join(missing, ", "))
			},
		},
		{
			Name: "todo_mirror_preserves_user_text",
			Applies: func(r Result) bool {
				return r.Name == "todo-mirror"
			},
			Score: func(r Result) MetricResult {
				return score("todo_mirror_preserves_user_text",
					strings.Contains(r.Files["todo"], "Keep me"), "user text disappeared")
			},
		},
		{
			Name: "worktree_untouched_on_restart",
			Applies: func(r Result) bool {
				return strings.Contains(r.Name, "restart")
			},
			Score: func(r Result) MetricResult {
				for _, cmd := range r.Execs {
					if strings.Contains(cmd, "git worktree remove") || strings.Contains(cmd, "rm -rf") {
						return score("worktree_untouched_on_restart", false, "restart removed the worktree")
					}
				}
				return score("worktree_untouched_on_restart", true, "")
			},
		},
		{
			Name: "event_sequence_contiguous",
			Applies: func(r Result) bool {
				return len(r.Events) > 0
			},
			Score: func(r Result) MetricResult {
				for i, ev := range r.Events {
					if ev.Seq != int64(i+1) || ev.Hash == "" {
						return score("event_sequence_contiguous", false, "sequence or hash is broken")
					}
				}
				return score("event_sequence_contiguous", true, "")
			},
		},
	}
}

// Evaluate appends every applicable standard metric and recomputes Passed.
func Evaluate(res Result) Result {
	res.Passed = res.Err == nil
	for _, m := range StandardMetrics() {
		if m.Applies != nil && !m.Applies(res) {
			continue
		}
		mr := m.Score(res)
		res.Metrics = append(res.Metrics, mr)
		if mr.Score < 1 {
			res.Passed = false
		}
	}
	return res
}

// RunSuite runs cases in order and logs one line per case plus a summary.
func RunSuite(t *testing.T, cases []Case) []Result {
	out := make([]Result, 0, len(cases))
	passed := 0
	for _, c := range cases {
		res := c.Run(t)
		out = append(out, res)
		if res.Passed {
			passed++
		}
		t.Logf("%-24s %s", res.Name, passWord(res.Passed))
	}
	t.Logf("%d/%d cases passed", passed, len(cases))
	return out
}

func passWord(ok bool) string {
	if ok {
		return "PASS"
	}
	return "FAIL"
}

func typedSteers(execs []string) int {
	n := 0
	for _, cmd := range execs {
		if strings.Contains(cmd, "tmux send-keys") && strings.Contains(cmd, " -l ") {
			n++
		}
	}
	return n
}

func hasIdleReport(events []domain.SessionEvent) bool {
	for _, ev := range events {
		if ev.Kind != domain.EventHookReported {
			continue
		}
		var payload struct {
			State string `json:"state"`
		}
		if json.Unmarshal(ev.Payload, &payload) == nil && payload.State == "idle" {
			return true
		}
	}
	return false
}
