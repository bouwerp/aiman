package domain

import (
	"crypto/rand"
	"strings"
	"testing"
	"time"
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

func TestAdHocLabelHasSecondsAndSuffix(t *testing.T) {
	now := time.Date(2026, 10, 22, 15, 4, 5, 0, time.UTC)
	got := AdHocLabel("adhoc-", now, strings.NewReader("\x00\x01\x02\x03"))
	if got != "adhoc-20261022-150405-0123" {
		t.Fatalf("got %q", got)
	}
	if TerminalNameFromBranch(got) != got {
		t.Fatalf("not tmux-safe: %q", got)
	}
}

func TestAdHocLabelDiffersWithinOneSecond(t *testing.T) {
	now := time.Now()
	a := AdHocLabel("adhoc-", now, rand.Reader)
	b := AdHocLabel("adhoc-", now, rand.Reader)
	if a == b {
		t.Fatalf("collision %q", a)
	}
}

func TestAdHocLabelFallsBackOnReaderError(t *testing.T) {
	now := time.Date(2026, 10, 22, 15, 4, 5, 37, time.UTC)
	got := AdHocLabel("adhoc-", now, strings.NewReader(""))
	if got != "adhoc-20261022-150405-0011" {
		t.Fatalf("got %q", got)
	}
}
