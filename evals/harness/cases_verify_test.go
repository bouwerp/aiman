package harness

import "testing"

func TestVerifyPass(t *testing.T) {
	res := CaseVerifyPass().Run(t)
	if !res.Passed {
		t.Fatalf("verify-pass failed: %+v", res.Metrics)
	}
}

func TestVerifyMissingEvidence(t *testing.T) {
	res := CaseVerifyMissingEvidence().Run(t)
	if !res.Passed {
		t.Fatalf("verify-missing-evidence failed: %+v", res.Metrics)
	}
}

func TestVerifyBlockedPR(t *testing.T) {
	res := CaseVerifyBlockedPR().Run(t)
	if !res.Passed {
		t.Fatalf("verify-blocked-pr failed: %+v", res.Metrics)
	}
}

func TestVerifyReproduceFirst(t *testing.T) {
	res := CaseVerifyReproduceFirst().Run(t)
	if !res.Passed {
		t.Fatalf("verify-reproduce-first failed: %+v", res.Metrics)
	}
}
