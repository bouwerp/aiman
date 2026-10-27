package harness

import "testing"

func TestUsageContext(t *testing.T) {
	res := CaseUsageContext().Run(t)
	if !res.Passed {
		t.Fatalf("usage-context failed: %+v", res.Metrics)
	}
}

func TestUsageBudget(t *testing.T) {
	t.Skip("waiting on nl-4 T14")
}
