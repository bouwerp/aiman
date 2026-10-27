package harness

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestMetricPromptDuplicateFails(t *testing.T) {
	res := Evaluate(Result{Name: "prompt-once", Execs: []string{
		`tmux send-keys -t "s1" -l -- "one"`,
		`tmux send-keys -t "s1" -l -- "two"`,
	}})
	if res.Passed {
		t.Fatalf("duplicate prompt passed: %+v", res.Metrics)
	}
	if findMetric(res, "prompt_submitted_once").Score != 0 {
		t.Fatalf("metrics = %+v", res.Metrics)
	}
}

func TestMetricHandoverRequiresEvidence(t *testing.T) {
	res := Evaluate(Result{Name: "handover-restart", Files: map[string]string{
		"handover": "## Goal\n\nx\n\n## Constraints & Preferences\n\nx\n",
	}})
	if res.Passed {
		t.Fatalf("incomplete handover passed: %+v", res.Metrics)
	}
	if findMetric(res, "handover_sections_complete").Score != 0 {
		t.Fatalf("metrics = %+v", res.Metrics)
	}
}

func TestMetricEventSequenceContiguous(t *testing.T) {
	ok := Evaluate(Result{Name: "x", Events: []domain.SessionEvent{
		{Seq: 1, Hash: "a"}, {Seq: 2, Hash: "b"},
	}})
	if !ok.Passed {
		t.Fatalf("contiguous sequence failed: %+v", ok.Metrics)
	}
	bad := Evaluate(Result{Name: "x", Events: []domain.SessionEvent{
		{Seq: 1, Hash: "a"}, {Seq: 3, Hash: "b"},
	}})
	if bad.Passed || findMetric(bad, "event_sequence_contiguous").Score != 0 {
		t.Fatalf("broken sequence passed: %+v", bad.Metrics)
	}
}

func TestMetricSkipsIrrelevantCase(t *testing.T) {
	res := Evaluate(Result{Name: "todo-mirror", Files: map[string]string{"todo": `{"items":[]}`}})
	for _, m := range res.Metrics {
		if m.Name == "prompt_submitted_once" {
			t.Fatalf("prompt metric applied to a todo case: %+v", res.Metrics)
		}
	}
}

func findMetric(res Result, name string) MetricResult {
	for _, m := range res.Metrics {
		if m.Name == name {
			return m
		}
	}
	return MetricResult{}
}
