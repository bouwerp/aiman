package domain

import (
	"testing"
)

func TestIssue_Slug(t *testing.T) {
	issue := Issue{
		Key:     "PROJ-123",
		Summary: "Daily Points Accrual — Position Based Multiplier",
	}
	expected := "PROJ-123-Daily-Points-Accrual-Position-Based-Multiplier"
	got := issue.Slug()
	if got != expected {
		t.Errorf("Issue.Slug() = %q, want %q", got, expected)
	}
}
