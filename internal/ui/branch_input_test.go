package ui

import (
	"strings"
	"testing"
)

func TestBranchInputKeepsSessionNameAndPreviewsIdentifiers(t *testing.T) {
	m := NewBranchInputModel("Deploy API: phase 2!")

	if got, want := m.Name(), "Deploy API: phase 2!"; got != want {
		t.Fatalf("Name() = %q, want %q", got, want)
	}
	if got, want := m.BranchName(), "Deploy-API-phase-2"; got != want {
		t.Fatalf("BranchName() = %q, want %q", got, want)
	}
	for _, want := range []string{
		"Branch: Deploy-API-phase-2",
		"Worktree: <repository>@Deploy-API-phase-2",
		"Tmux session: Deploy-API-phase-2",
	} {
		if !strings.Contains(m.viewString(), want) {
			t.Fatalf("preview missing %q:\n%s", want, m.viewString())
		}
	}
}
