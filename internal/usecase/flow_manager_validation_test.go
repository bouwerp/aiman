package usecase

import (
	"context"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestCreateSessionRequiresAgent(t *testing.T) {
	manager := &FlowManager{}

	_, err := manager.CreateSession(context.Background(), domain.SessionConfig{})
	if err == nil || !strings.Contains(err.Error(), "agent") {
		t.Fatalf("CreateSession() error = %v, want an agent validation error", err)
	}
}

// A prepared worktree is used as is: nil git manager and executor prove no
// git call is made.
func TestResolveWorktreePrepared(t *testing.T) {
	cfg := domain.SessionConfig{PreparedWorktree: "/r/repo@review-ab12", Repo: domain.Repo{Name: "o/repo"}}
	wt, err := (&FlowManager{}).resolveWorktree(context.Background(), nil, cfg, "review-ab12")
	if err != nil {
		t.Fatal(err)
	}
	if wt != (domain.Worktree{Path: "/r/repo@review-ab12"}) {
		t.Fatalf("worktree %+v", wt)
	}
}
