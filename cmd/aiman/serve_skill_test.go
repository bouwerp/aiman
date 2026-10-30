package main

import (
	"slices"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// The skill is never copied into a review's PR checkout, only into its
// scratch working directory.
func TestSkillRootsSkipReviewWorktree(t *testing.T) {
	got := skillRoots([]domain.Session{
		{WorktreePath: "/w/r@x", WorkingDirectory: "/w/r@x/sub"},
		{WorktreePath: "/w/r@pr-7", WorkingDirectory: "/h/.aiman/pr-reviews/s/work", ReviewPR: "o/r#7"},
	})
	want := []string{"/w/r@x", "/w/r@x/sub", "/h/.aiman/pr-reviews/s/work"}
	if !slices.Equal(got, want) {
		t.Fatalf("roots %v, want %v", got, want)
	}
}
