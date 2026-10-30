package domain

import (
	"errors"
	"testing"
)

func TestPRWorktreePaths(t *testing.T) {
	for path, want := range map[string]bool{
		"/w/r@pr-7":            true,
		"/w/r@pr-7/":           true,
		"/w/r@pr-7/sub/dir":    true,
		"/w/r@pr-x":            false,
		"/w/r@feat":            false,
		"/w/r":                 false,
		"/w/pr-7":              false,
		"":                     false,
		"/h/.aiman/pr-reviews": false,
	} {
		if got := InPRWorktree(path); got != want {
			t.Errorf("InPRWorktree(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestCheckLaunchDir(t *testing.T) {
	if err := CheckLaunchDir("/w/r@pr-7/sub"); !errors.Is(err, ErrPRWorktreeLaunch) {
		t.Fatalf("err %v, want ErrPRWorktreeLaunch", err)
	}
	if err := CheckLaunchDir("/w/r@feat"); err != nil {
		t.Fatalf("ordinary dir refused: %v", err)
	}
}
