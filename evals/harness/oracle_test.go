package harness

import "testing"

func TestOracleGoodPasses(t *testing.T) {
	results := RunSuite(t, GoodOracle())
	for _, res := range results {
		if !res.Passed {
			t.Errorf("%s failed: %+v", res.Name, res.Metrics)
		}
	}
}

func TestOracleBrokenFails(t *testing.T) {
	results := RunSuite(t, BrokenOracle())
	if len(results) != 2 {
		t.Fatalf("results = %d", len(results))
	}
	for _, res := range results {
		if res.Passed {
			t.Errorf("%s passed the broken oracle", res.Name)
		}
	}
	if findMetric(results[0], "prompt_submitted_once").Score != 0 {
		t.Fatalf("duplicate prompt metric = %+v", results[0].Metrics)
	}
	if findMetric(results[1], "handover_sections_complete").Score != 0 {
		t.Fatalf("missing evidence metric = %+v", results[1].Metrics)
	}
}
