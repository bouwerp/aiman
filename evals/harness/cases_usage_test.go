package harness

import "testing"

func TestUsageContext(t *testing.T) {
	res := CaseUsageContext().Run(t)
	if !res.Passed {
		t.Fatalf("usage-context failed: %+v", res.Metrics)
	}
}

func TestUsageBudget(t *testing.T) {
	res := CaseUsageBudget().Run(t)
	if !res.Passed {
		t.Fatalf("usage-budget failed: %+v", res.Metrics)
	}
}
