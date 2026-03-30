package git

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// mockRemote implements domain.RemoteExecutor for testing
type mockRemote struct {
	root     string
	dirs     map[string]bool // paths that "exist"
	outputs  map[string]string
	errors   map[string]error
}

func (m *mockRemote) GetRoot() string { return m.root }

func (m *mockRemote) ValidateDir(_ context.Context, path string) error {
	if m.dirs[path] {
		return nil
	}
	return fmt.Errorf("directory not found: %s", path)
}

func (m *mockRemote) Execute(_ context.Context, cmd string) (string, error) {
	out := ""
	if o, ok := m.outputs[cmd]; ok {
		out = o
	}
	if err, ok := m.errors[cmd]; ok {
		return out, err
	}
	return out, nil
}

// Stub all other RemoteExecutor methods
func (m *mockRemote) Connect(context.Context) error                           { return nil }
func (m *mockRemote) WriteFile(context.Context, string, []byte) error         { return nil }
func (m *mockRemote) ScanTmuxSessions(context.Context) ([]string, error)      { return nil, nil }
func (m *mockRemote) ScanGitRepos(context.Context) ([]string, error)          { return nil, nil }
func (m *mockRemote) ScanWorktrees(context.Context, string) ([]string, error) { return nil, nil }
func (m *mockRemote) GetGitRoot(context.Context, string) (string, error)      { return "", nil }
func (m *mockRemote) GetTmuxSessionCWD(context.Context, string) (string, error) {
	return "", nil
}
func (m *mockRemote) GetTmuxSessionEnv(context.Context, string, string) (string, error) {
	return "", nil
}
func (m *mockRemote) CaptureTmuxPane(context.Context, string) (string, error)     { return "", nil }
func (m *mockRemote) AttachTmuxSession(string) *exec.Cmd                          { return nil }
func (m *mockRemote) StreamTmuxSession(context.Context, string) (io.ReadWriteCloser, error) {
	return nil, nil
}
func (m *mockRemote) StartTmuxSession(context.Context, string) error { return nil }
func (m *mockRemote) Close() error                                   { return nil }

func TestListRemoteBranches_Success(t *testing.T) {
	branchOutput := "  origin/main\n  origin/feature-x\n  origin/HEAD -> origin/main\n"
	mgr := NewManager(nil)
	remote := &mockRemote{
		root:  "/home/dev",
		dirs:  map[string]bool{"/home/dev/myrepo": true},
		outputs: map[string]string{
			"git -C /home/dev/myrepo branch -r": branchOutput,
		},
	}
	// Stub fetch (no error)
	remote.outputs["git -C /home/dev/myrepo fetch origin 2>/dev/null"] = ""

	branches, err := mgr.ListRemoteBranches(context.Background(), remote, domain.Repo{Name: "myrepo"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(branches) != 2 {
		t.Fatalf("expected 2 branches, got %d: %v", len(branches), branches)
	}

	wantBranches := map[string]bool{"main": true, "feature-x": true}
	for _, b := range branches {
		if !wantBranches[b] {
			t.Errorf("unexpected branch: %q", b)
		}
	}
}

func TestListRemoteBranches_RepoNotFound(t *testing.T) {
	mgr := NewManager(nil)
	remote := &mockRemote{
		root: "/home/dev",
		dirs: map[string]bool{}, // repo doesn't exist
	}

	_, err := mgr.ListRemoteBranches(context.Background(), remote, domain.Repo{Name: "missing-repo"})
	if err == nil {
		t.Fatal("expected error for missing repo, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got: %v", err)
	}
}

func TestSetupRemoteWorktreeFromBranch_WorktreeAlreadyExists(t *testing.T) {
	mgr := NewManager(nil)

	// Simulate git worktree list --porcelain returning an entry for the branch
	worktreeListOutput := "worktree /home/dev/feature-x\nHEAD abc123\nbranch refs/heads/feature-x\n\n"

	remote := &mockRemote{
		root: "/home/dev",
		dirs: map[string]bool{"/home/dev/myrepo": true},
		outputs: map[string]string{
			"git -C /home/dev/myrepo fetch origin": "",
			"git -C /home/dev/myrepo worktree list --porcelain": worktreeListOutput,
		},
	}

	_, err := mgr.SetupRemoteWorktreeFromBranch(context.Background(), remote, domain.Repo{Name: "myrepo"}, "feature-x")
	if err == nil {
		t.Fatal("expected WORKTREE_EXISTS error, got nil")
	}
	if err.Error() != "WORKTREE_EXISTS" {
		t.Errorf("expected 'WORKTREE_EXISTS', got: %v", err)
	}
}

func TestSetupRemoteWorktreeFromBranch_LocalBranchAlreadyExists_FallsBack(t *testing.T) {
	mgr := NewManager(nil)

	// Simulate git worktree add -b failing with "already exists"
	createWithBCmd := "git -C /home/dev/myrepo worktree add -b feature-x ../feature-x origin/feature-x"
	createDirectCmd := "git -C /home/dev/myrepo worktree add ../feature-x feature-x"

	remote := &mockRemote{
		root: "/home/dev",
		dirs: map[string]bool{"/home/dev/myrepo": true},
		outputs: map[string]string{
			"git -C /home/dev/myrepo fetch origin":                "",
			"git -C /home/dev/myrepo worktree list --porcelain":   "",
			`bash -c 'if [ -d "/home/dev/myrepo/../feature-x" ]; then echo EXISTS; fi'`: "",
			createWithBCmd:   "fatal: a branch named 'feature-x' already exists",
			createDirectCmd:  "",
			`realpath "/home/dev/myrepo/../feature-x"`: "/home/dev/feature-x",
		},
		errors: map[string]error{
			createWithBCmd: fmt.Errorf("exit status 128"),
		},
	}

	wt, err := mgr.SetupRemoteWorktreeFromBranch(context.Background(), remote, domain.Repo{Name: "myrepo"}, "feature-x")
	if err != nil {
		t.Fatalf("expected fallback to succeed, got error: %v", err)
	}
	if wt.Path != "/home/dev/feature-x" {
		t.Errorf("expected resolved path /home/dev/feature-x, got %q", wt.Path)
	}
}

func TestSetupRemoteWorktreeFromBranch_DirectoryAlreadyExists(t *testing.T) {
	mgr := NewManager(nil)

	remote := &mockRemote{
		root: "/home/dev",
		dirs: map[string]bool{"/home/dev/myrepo": true},
		outputs: map[string]string{
			"git -C /home/dev/myrepo fetch origin":                "",
			"git -C /home/dev/myrepo worktree list --porcelain":   "",
			`bash -c 'if [ -d "/home/dev/myrepo/../feature-x" ]; then echo EXISTS; fi'`: "EXISTS",
		},
	}

	_, err := mgr.SetupRemoteWorktreeFromBranch(context.Background(), remote, domain.Repo{Name: "myrepo"}, "feature-x")
	if err == nil {
		t.Fatal("expected WORKTREE_EXISTS error, got nil")
	}
	if err.Error() != "WORKTREE_EXISTS" {
		t.Errorf("expected 'WORKTREE_EXISTS', got: %v", err)
	}
}

func TestAimanTaskGitignoreBashScript_ContainsWTAndRules(t *testing.T) {
	s := aimanTaskGitignoreBashScript("/home/dev/wt")
	if !strings.Contains(s, ".aiman_task.md") {
		t.Fatalf("missing ignore line: %s", s)
	}
	if !strings.Contains(s, "/home/dev/wt") {
		t.Fatalf("missing worktree path: %s", s)
	}
	if !strings.Contains(s, "grep -qxF") {
		t.Fatal("missing idempotency guard")
	}
}

func TestEnsureAimanTaskGitignored_EmptyPath(t *testing.T) {
	mgr := NewManager(nil)
	err := mgr.EnsureAimanTaskGitignored(context.Background(), &mockRemote{}, "   ")
	if err != nil {
		t.Fatal(err)
	}
}
