package review

import (
	"strings"
	"testing"
)

// The agent runs outside the PR worktree, so the second sentence tells it
// where the PR is.
func TestPromptPointsAtTheWorktree(t *testing.T) {
	first := "You are reviewing a pull request in this worktree. "
	second := "The pull request is checked out at the path in .aiman_review/meta.json (worktree); read it there. "
	if !strings.HasPrefix(Prompt, first+second) {
		t.Fatalf("prompt %q", Prompt)
	}
}
