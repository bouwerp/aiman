package domain

import (
	"testing"
)

func TestGitSlugger_Slugify(t *testing.T) {
	tests := []struct {
		name     string
		key      string
		summary  string
		expected string
	}{
		{
			name:     "Simple title",
			key:      "AIMAN-123",
			summary:  "Implement SSH Manager",
			expected: "AIMAN-123/implement-ssh-manager",
		},
		{
			name:     "Title with special characters",
			key:      "PROJ-456",
			summary:  "Fix: [BUG] SSH Connection Error!!!",
			expected: "PROJ-456/fix-bug-ssh-connection-error",
		},
		{
			name:     "Title with extra spaces",
			key:      "DOC-789",
			summary:  "   Update README  documentation   ",
			expected: "DOC-789/update-readme-documentation",
		},
		{
			name:     "Title with mixed case",
			key:      "UI-101",
			summary:  "Refactor BubbleTea components",
			expected: "UI-101/refactor-bubbletea-components",
		},
		{
			name:     "Title with comma",
			key:      "COM-123",
			summary:  "Fix bugs, add features",
			expected: "COM-123/fix-bugs-add-features",
		},
	}

	slugger := NewGitSlugger()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := slugger.Slugify(tt.key, tt.summary)
			if got != tt.expected {
				t.Errorf("GitSlugger.Slugify() = %v, want %v", got, tt.expected)
			}
		})
	}
}
