package domain

import (
	"strings"
	"testing"
)

func TestSanitizeBranchName(t *testing.T) {
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
		{
			input:    "feature/foo/bar",
			expected: "feature/foo/bar",
		},
		{
			input:    "//weird//",
			expected: "weird",
		},
		{
			input:    "ends-with.lock",
			expected: "ends-with-lock",
		},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := SanitizeBranchName(tt.input)
			if got != tt.expected {
				t.Errorf("SanitizeBranchName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSanitizeBranchName_pastedTableNoise(t *testing.T) {
	// e.g. accidental paste of nvidia-smi style output: | + % etc.
	raw := `|   0  Tesla P4                       Off |   00000000:05:00.0 Off |                    0 |
| N/A   48C    P0             28W /   70W |    4772MiB /   7680MiB |      0%      Default |`
	got := SanitizeBranchName(raw)
	if strings.ContainsAny(got, "|+%") {
		t.Errorf("SanitizeBranchName should strip table chars, got %q", got)
	}
	if got == "" {
		t.Fatal("expected non-empty sanitized branch")
	}
	// Should be mostly alphanumeric segments joined by single slashes or hyphens
	if strings.Contains(got, " ") {
		t.Errorf("no spaces allowed, got %q", got)
	}
}
