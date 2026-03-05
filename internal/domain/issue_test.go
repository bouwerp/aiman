package domain

import (
	"testing"
)

func TestSanitizeGitBranchName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{
			input:    "simple title",
			expected: "simple-title",
		},
		{
			input:    "Title With Spaces",
			expected: "Title-With-Spaces",
		},
		{
			input:    "Title-With-Em-Dash-—-And-En-Dash-–",
			expected: "Title-With-Em-Dash-And-En-Dash",
		},
		{
			input:    "Title with [Special] {Characters}!",
			expected: "Title-with-Special-Characters",
		},
		{
			input:    "Title with ... dots",
			expected: "Title-with-.-dots",
		},
		{
			input:    "Title with \"quotes\" and 'smart quotes' ‘’“”",
			expected: "Title-with-quotes-and-smart-quotes",
		},
		{
			input:    "---leading-and-trailing---",
			expected: "leading-and-trailing",
		},
		{
			input:    "multiple---dashes",
			expected: "multiple-dashes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeGitBranchName(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeGitBranchName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

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
