package usecase

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/local"
)

func TestCreateSessionRequiresAgent(t *testing.T) {
	manager := &FlowManager{}

	_, err := manager.CreateSession(context.Background(), domain.SessionConfig{})
	if err == nil || !strings.Contains(err.Error(), "agent") {
		t.Fatalf("CreateSession() error = %v, want an agent validation error", err)
	}
}

func TestCreateSessionReviewNeedsPreparedWorktree(t *testing.T) {
	manager := &FlowManager{}
	_, err := manager.CreateSession(context.Background(), domain.SessionConfig{
		Agent:    &domain.Agent{Name: "Claude Code", Command: "claude"},
		ReviewPR: "o/r#7",
		Repo:     domain.Repo{Name: "o/r"},
	})
	if err == nil || !strings.Contains(err.Error(), "agent API") {
		t.Fatalf("CreateSession() error = %v, want a review-must-use-serve error", err)
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

// A prepared review scratch is a git repo, but serve's executor runs from its
// own cwd. The session id must land in that repo's git-dir, not a relative
// .git next to the process.
func TestCreateSessionWritesAimanIDWhenCwdIsNotTheWorktree(t *testing.T) {
	t.Chdir(t.TempDir())
	scratch := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", scratch).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	const id = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	_, createErr := (&FlowManager{}).CreateSession(context.Background(), domain.SessionConfig{
		SessionID:        id,
		Agent:            &domain.Agent{Name: "claude", Command: "true"},
		PreparedWorktree: scratch,
		ReviewPR:         "o/r#7",
		Repo:             domain.Repo{Name: "o/r"},
		SSHManager:       &stopAfterIDWrite{Executor: local.NewExecutor("")},
	})
	got, err := os.ReadFile(filepath.Join(scratch, ".git", "aiman-id"))
	if err != nil {
		t.Fatalf("aiman-id missing (create err %v): %v", createErr, err)
	}
	if strings.TrimSpace(string(got)) != id {
		t.Fatalf("aiman-id = %q, want %s", got, id)
	}
}

// stopAfterIDWrite runs real shell commands until a PTY launch, so create
// cannot start an agent after the id write under test.
type stopAfterIDWrite struct {
	*local.Executor
}

func (e *stopAfterIDWrite) Execute(ctx context.Context, cmd string) (string, error) {
	if strings.Contains(cmd, "pty") {
		return "", context.Canceled
	}
	return e.Executor.Execute(ctx, cmd)
}
