package git

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// mockRemote implements domain.RemoteExecutor for testing
type mockRemote struct {
	root    string
	dirs    map[string]bool // paths that "exist"
	outputs map[string]string
	errors  map[string]error
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
func (m *mockRemote) CaptureTmuxPane(context.Context, string) (string, error) { return "", nil }
func (m *mockRemote) AttachTmuxSession(string) *exec.Cmd                      { return nil }
func (m *mockRemote) StreamTmuxSession(context.Context, string) (io.ReadWriteCloser, error) {
	return nil, nil
}
func (m *mockRemote) StartTmuxSession(context.Context, string) error { return nil }
func (m *mockRemote) ProvisionRemote(_ context.Context, _ []domain.ProvisionStep, _ chan<- domain.ProvisionProgress) error {
	return nil
}
func (m *mockRemote) Close() error { return nil }

func TestListRemoteBranches_Success(t *testing.T) {
	branchOutput := "  origin/main\n  origin/feature-x\n  origin/HEAD -> origin/main\n"
	mgr := NewManager(nil)
	remote := &mockRemote{
		root: "/home/dev",
		dirs: map[string]bool{"/home/dev/myrepo": true},
		outputs: map[string]string{
			`git -C "/home/dev/myrepo" branch -r --sort=-committerdate`: branchOutput,
		},
	}

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
			`git -C "/home/dev/myrepo" worktree list --porcelain`: worktreeListOutput,
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
			"git -C /home/dev/myrepo fetch origin":                                      "",
			"git -C /home/dev/myrepo worktree list --porcelain":                         "",
			`bash -c 'if [ -d "/home/dev/myrepo/../feature-x" ]; then echo EXISTS; fi'`: "",
			createWithBCmd:  "fatal: a branch named 'feature-x' already exists",
			createDirectCmd: "",
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
			"git -C /home/dev/myrepo fetch origin":                                      "",
			"git -C /home/dev/myrepo worktree list --porcelain":                         "",
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

func TestSetupRemoteWorktreeFromBranch_BranchNameEqualsRepoName(t *testing.T) {
	mgr := NewManager(nil)

	remote := &mockRemote{
		root: "/home/dev",
		dirs: map[string]bool{"/home/dev/myrepo": true},
		outputs: map[string]string{
			"git -C /home/dev/myrepo fetch origin": "",
		},
	}

	_, err := mgr.SetupRemoteWorktreeFromBranch(context.Background(), remote, domain.Repo{Name: "myrepo"}, "myrepo")
	if err == nil {
		t.Fatal("expected error when branch name equals repo name, got nil")
	}
	if !strings.Contains(err.Error(), "main repository directory") {
		t.Errorf("expected error about main repository directory, got: %v", err)
	}
}

func TestSetupRemoteWorktree_BranchNameEqualsRepoName(t *testing.T) {
	mgr := NewManager(nil)

	remote := &mockRemote{
		root: "/home/dev",
		dirs: map[string]bool{"/home/dev/myrepo": true},
		outputs: map[string]string{
			"git -C /home/dev/myrepo fetch origin": "",
		},
	}

	_, err := mgr.SetupRemoteWorktree(context.Background(), remote, domain.Repo{Name: "myrepo"}, "myrepo")
	if err == nil {
		t.Fatal("expected error when branch name equals repo name, got nil")
	}
	if !strings.Contains(err.Error(), "main repository directory") {
		t.Errorf("expected error about main repository directory, got: %v", err)
	}
}

func TestAimanTaskGitignoreBashScript_ContainsWTAndRules(t *testing.T) {
	s := aimanTaskGitignoreBashScript("/home/dev/wt")
	if !strings.Contains(s, domain.AimanGeneratedFileGlob) {
		t.Fatalf("missing ignore line: %s", s)
	}
	if !strings.Contains(s, "/home/dev/wt") {
		t.Fatalf("missing worktree path: %s", s)
	}
	if !strings.Contains(s, "grep -qxF") {
		t.Fatal("missing idempotency guard")
	}
}

func TestEnsureAimanSessionFilesGitignored_EmptyPath(t *testing.T) {
	mgr := NewManager(nil)
	err := mgr.EnsureAimanSessionFilesGitignored(context.Background(), &mockRemote{}, "   ")
	if err != nil {
		t.Fatal(err)
	}
}

func TestGithubRepoActivityTime(t *testing.T) {
	old := "2025-01-01T00:00:00Z"
	newer := "2026-03-31T12:00:00Z"
	if got := githubRepoActivityTime(newer, old); !got.Equal(timeMustParse(t, newer)) {
		t.Fatalf("expected pushed when newer: got %v", got)
	}
	if got := githubRepoActivityTime(old, newer); !got.Equal(timeMustParse(t, newer)) {
		t.Fatalf("expected updated when newer than pushed: got %v", got)
	}
	if got := githubRepoActivityTime("", newer); !got.Equal(timeMustParse(t, newer)) {
		t.Fatalf("expected updated when pushed empty: got %v", got)
	}
}

func timeMustParse(t *testing.T, s string) time.Time {
	t.Helper()
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return tt
}

func TestSortReposByRecentActivity(t *testing.T) {
	t1 := timeMustParse(t, "2026-01-01T00:00:00Z")
	t2 := timeMustParse(t, "2026-06-01T00:00:00Z")
	t3 := timeMustParse(t, "2025-01-01T00:00:00Z")
	repos := []domain.Repo{
		{Name: "z-old", LastActivityAt: t3},
		{Name: "m-mid", LastActivityAt: t1},
		{Name: "a-new", LastActivityAt: t2},
	}
	sortReposByRecentActivity(repos)
	want := []string{"a-new", "m-mid", "z-old"}
	for i, name := range want {
		if repos[i].Name != name {
			t.Fatalf("index %d: want %q, got %q", i, name, repos[i].Name)
		}
	}
}

func TestSortReposByRecentActivity_TieBreakName(t *testing.T) {
	ts := timeMustParse(t, "2026-01-01T00:00:00Z")
	repos := []domain.Repo{
		{Name: "Bbb", LastActivityAt: ts},
		{Name: "aaa", LastActivityAt: ts},
	}
	sortReposByRecentActivity(repos)
	if repos[0].Name != "aaa" || repos[1].Name != "Bbb" {
		t.Fatalf("want alphabetical tie-break, got %q then %q", repos[0].Name, repos[1].Name)
	}
}

func TestParseGhRepos_SetsLastActivityAt(t *testing.T) {
	jsonOut := `[
		{"name":"b","nameWithOwner":"o/b","url":"https://github.com/o/b","sshUrl":"git@github.com:o/b.git","pushedAt":"2025-06-01T00:00:00Z","updatedAt":"2025-01-01T00:00:00Z"},
		{"name":"a","nameWithOwner":"o/a","url":"https://github.com/o/a","sshUrl":"git@github.com:o/a.git","pushedAt":"2026-01-01T00:00:00Z","updatedAt":"2026-01-02T00:00:00Z"}
	]`
	repos, err := (&Manager{}).parseGhRepos([]byte(jsonOut))
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 {
		t.Fatalf("len=%d", len(repos))
	}
	// parse order preserved; ListRepos sorts after merge
	if repos[0].Name != "o/b" || repos[0].URL != "git@github.com:o/b.git" || !repos[0].LastActivityAt.Equal(timeMustParse(t, "2025-06-01T00:00:00Z")) {
		t.Fatalf("repo b: %+v", repos[0])
	}
	if repos[1].Name != "o/a" || repos[1].URL != "git@github.com:o/a.git" || !repos[1].LastActivityAt.Equal(timeMustParse(t, "2026-01-02T00:00:00Z")) {
		t.Fatalf("repo a: want updatedAt as max, got %+v", repos[1])
	}
}

type commandRecorderRemote struct {
	mockRemote
	commandsRun []string
}

func (c *commandRecorderRemote) Execute(ctx context.Context, cmd string) (string, error) {
	c.commandsRun = append(c.commandsRun, cmd)
	return c.mockRemote.Execute(ctx, cmd)
}

func TestEnsureHealthyRepo_HealthyRepo(t *testing.T) {
	mgr := NewManager(nil)
	remote := &commandRecorderRemote{
		mockRemote: mockRemote{
			root: "/home/dev",
			dirs: map[string]bool{"/home/dev/myrepo": true},
			outputs: map[string]string{
				`git -C "/home/dev/myrepo" remote get-url origin 2>/dev/null`:        "git@github.com:owner/myrepo.git",
				`git -C "/home/dev/myrepo" rev-parse --git-dir`:                      ".git",
				`git -C "/home/dev/myrepo" fetch origin 2>&1`:                        "",
				`git -C "/home/dev/myrepo" rev-parse HEAD 2>/dev/null || echo EMPTY`: "abc1234",
			},
		},
	}

	repo := domain.Repo{Name: "myrepo", URL: "git@github.com:owner/myrepo.git"}
	err := mgr.EnsureHealthyRepo(context.Background(), remote, repo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify no recovery/rm-rf/clone command was run
	for _, cmd := range remote.commandsRun {
		if strings.Contains(cmd, "rm -rf") || strings.Contains(cmd, "clone") {
			t.Errorf("unexpected recovery/clone command run: %q", cmd)
		}
	}
}

func TestEnsureHealthyRepo_MissingOriginRemote(t *testing.T) {
	mgr := NewManager(nil)
	remote := &commandRecorderRemote{
		mockRemote: mockRemote{
			root: "/home/dev",
			dirs: map[string]bool{"/home/dev/myrepo": true},
			outputs: map[string]string{
				`git -C "/home/dev/myrepo" rev-parse --git-dir`:                                 ".git",
				`git -C "/home/dev/myrepo" remote add origin "git@github.com:owner/myrepo.git"`: "",
				`git -C "/home/dev/myrepo" fetch origin 2>&1`:                                   "",
				`git -C "/home/dev/myrepo" rev-parse HEAD 2>/dev/null || echo EMPTY`:            "abc1234",
			},
			errors: map[string]error{
				`git -C "/home/dev/myrepo" remote get-url origin 2>/dev/null`: fmt.Errorf("exit status 1"),
			},
		},
	}

	repo := domain.Repo{Name: "myrepo", URL: "git@github.com:owner/myrepo.git"}
	err := mgr.EnsureHealthyRepo(context.Background(), remote, repo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify remote add origin was executed
	foundAdd := false
	for _, cmd := range remote.commandsRun {
		if strings.Contains(cmd, "remote add origin") {
			foundAdd = true
			break
		}
	}
	if !foundAdd {
		t.Errorf("expected 'remote add origin' to be called, commands run: %v", remote.commandsRun)
	}
}

func TestEnsureHealthyRepo_MismatchedRemoteURL(t *testing.T) {
	mgr := NewManager(nil)
	remote := &commandRecorderRemote{
		mockRemote: mockRemote{
			root: "/home/dev",
			dirs: map[string]bool{"/home/dev/myrepo": true},
			outputs: map[string]string{
				`git -C "/home/dev/myrepo" remote get-url origin 2>/dev/null`:                     "git@github.com:old/myrepo.git",
				`git -C "/home/dev/myrepo" rev-parse --git-dir`:                                   ".git",
				`git -C "/home/dev/myrepo" remote set-url origin "git@github.com:new/myrepo.git"`: "",
				`git -C "/home/dev/myrepo" fetch origin 2>&1`:                                     "",
				`git -C "/home/dev/myrepo" rev-parse HEAD 2>/dev/null || echo EMPTY`:              "abc1234",
			},
		},
	}

	repo := domain.Repo{Name: "myrepo", URL: "git@github.com:new/myrepo.git"}
	err := mgr.EnsureHealthyRepo(context.Background(), remote, repo)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify remote set-url origin was executed
	foundSet := false
	for _, cmd := range remote.commandsRun {
		if strings.Contains(cmd, "remote set-url origin") {
			foundSet = true
			break
		}
	}
	if !foundSet {
		t.Errorf("expected 'remote set-url origin' to be called, commands run: %v", remote.commandsRun)
	}
}

func TestEnsureHealthyRepo_FetchFailure(t *testing.T) {
	mgr := NewManager(nil)
	remote := &commandRecorderRemote{
		mockRemote: mockRemote{
			root: "/home/dev",
			dirs: map[string]bool{"/home/dev/myrepo": true},
			outputs: map[string]string{
				`git -C "/home/dev/myrepo" remote get-url origin 2>/dev/null`: "git@github.com:owner/myrepo.git",
				`git -C "/home/dev/myrepo" rev-parse --git-dir`:               ".git",
				`git -C "/home/dev/myrepo" fetch origin 2>&1`:                 "fatal: Could not read from remote repository.",
			},
			errors: map[string]error{
				`git -C "/home/dev/myrepo" fetch origin 2>&1`: fmt.Errorf("exit status 128"),
			},
		},
	}

	repo := domain.Repo{Name: "myrepo", URL: "git@github.com:owner/myrepo.git"}
	err := mgr.EnsureHealthyRepo(context.Background(), remote, repo)
	if err == nil {
		t.Fatal("expected error on fetch failure, got nil")
	}
	if !strings.Contains(err.Error(), "failed to fetch from origin remote") {
		t.Errorf("expected fetch failure error message, got: %v", err)
	}

	// Verify no recovery/rm-rf/clone command was run (repository is NOT deleted/recloned)
	for _, cmd := range remote.commandsRun {
		if strings.Contains(cmd, "rm -rf") || strings.Contains(cmd, "clone") {
			t.Errorf("unexpected recovery/clone command run on fetch failure: %q", cmd)
		}
	}
}

func TestEnsureHealthyRepo_MissingRepoWithLinkedWorktrees_BlocksClone(t *testing.T) {
	mgr := NewManager(nil)
	repoPath := "/home/dev/myrepo"
	scanCmd := "bash -ce " + strconv.Quote(linkedWorktreeScanScript(repoPath))
	remote := &commandRecorderRemote{
		mockRemote: mockRemote{
			root: "/home/dev",
			dirs: map[string]bool{},
			outputs: map[string]string{
				scanCmd: "/home/dev/feature-x\n",
			},
		},
	}

	repo := domain.Repo{Name: "myrepo", URL: "git@github.com:owner/myrepo.git"}
	err := mgr.EnsureHealthyRepo(context.Background(), remote, repo)
	if err == nil {
		t.Fatal("expected linked-worktree protection error, got nil")
	}
	if !strings.Contains(err.Error(), "linked worktrees still exist") {
		t.Fatalf("expected linked-worktree protection error, got: %v", err)
	}
	for _, cmd := range remote.commandsRun {
		if strings.Contains(cmd, " clone ") || strings.Contains(cmd, "rm -rf") {
			t.Fatalf("unexpected destructive command when linked worktrees exist: %q", cmd)
		}
	}
}

func TestEnsureHealthyRepo_InvalidGitDirWithLinkedWorktrees_BlocksRecovery(t *testing.T) {
	mgr := NewManager(nil)
	repoPath := "/home/dev/myrepo"
	scanCmd := "bash -ce " + strconv.Quote(linkedWorktreeScanScript(repoPath))
	remote := &commandRecorderRemote{
		mockRemote: mockRemote{
			root: "/home/dev",
			dirs: map[string]bool{repoPath: true},
			outputs: map[string]string{
				`git -C "/home/dev/myrepo" remote get-url origin 2>/dev/null`: "git@github.com:owner/myrepo.git",
				scanCmd: "/home/dev/feature-x\n",
			},
			errors: map[string]error{
				`git -C "/home/dev/myrepo" rev-parse --git-dir`: fmt.Errorf("exit status 128"),
			},
		},
	}

	repo := domain.Repo{Name: "myrepo", URL: "git@github.com:owner/myrepo.git"}
	err := mgr.EnsureHealthyRepo(context.Background(), remote, repo)
	if err == nil {
		t.Fatal("expected linked-worktree protection error, got nil")
	}
	if !strings.Contains(err.Error(), "linked worktrees still exist") {
		t.Fatalf("expected linked-worktree protection error, got: %v", err)
	}
	for _, cmd := range remote.commandsRun {
		if strings.Contains(cmd, " clone ") || strings.Contains(cmd, "rm -rf") {
			t.Fatalf("unexpected destructive command when linked worktrees exist: %q", cmd)
		}
	}
}

func TestSetupRemoteWorktree_DisablesSparseCheckoutPerWorktree(t *testing.T) {
	mgr := NewManager(nil)
	remote := &commandRecorderRemote{
		mockRemote: mockRemote{
			root: "/home/dev",
			dirs: map[string]bool{"/home/dev/myrepo": true},
			outputs: map[string]string{
				`git -C "/home/dev/myrepo" remote get-url origin 2>/dev/null`:                              "git@github.com:owner/myrepo.git",
				`git -C "/home/dev/myrepo" rev-parse --git-dir`:                                            ".git",
				`git -C "/home/dev/myrepo" fetch origin 2>&1`:                                              "",
				`git -C "/home/dev/myrepo" rev-parse HEAD 2>/dev/null || echo EMPTY`:                       "abc1234",
				`bash -c 'if [ -d "/home/dev/myrepo/../feature-x" ]; then echo EXISTS; fi'`:                "",
				`git -C "/home/dev/myrepo" rev-parse --verify origin/main`:                                 "abc1234",
				`git -C "/home/dev/myrepo" worktree add -B feature-x ../feature-x origin/main`:             "",
				`realpath "/home/dev/myrepo/../feature-x"`:                                                 "/home/dev/feature-x",
				`git -C "/home/dev/feature-x" config --bool core.sparseCheckout 2>/dev/null || echo false`: "true",
				`git -C "/home/dev/myrepo" config extensions.worktreeConfig true`:                          "",
				`git -C "/home/dev/feature-x" sparse-checkout disable`:                                     "",
			},
		},
	}

	wt, err := mgr.SetupRemoteWorktree(context.Background(), remote, domain.Repo{Name: "myrepo", URL: "git@github.com:owner/myrepo.git"}, "feature-x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wt.Path != "/home/dev/feature-x" {
		t.Fatalf("expected resolved path, got %q", wt.Path)
	}

	joined := strings.Join(remote.commandsRun, "\n")
	if !strings.Contains(joined, `git -C "/home/dev/myrepo" config extensions.worktreeConfig true`) {
		t.Fatalf("expected worktreeConfig enable command, got:\n%s", joined)
	}
	if !strings.Contains(joined, `git -C "/home/dev/feature-x" sparse-checkout disable`) {
		t.Fatalf("expected sparse-checkout disable command, got:\n%s", joined)
	}
}

func TestFindExistingWorktree_DisablesSparseCheckoutForSparseWorktree(t *testing.T) {
	mgr := NewManager(nil)
	remote := &commandRecorderRemote{
		mockRemote: mockRemote{
			root: "/home/dev",
			dirs: map[string]bool{"/home/dev/myrepo": true},
			outputs: map[string]string{
				`git -C "/home/dev/myrepo" remote get-url origin 2>/dev/null`:                              "git@github.com:owner/myrepo.git",
				`git -C "/home/dev/myrepo" rev-parse --git-dir`:                                            ".git",
				`git -C "/home/dev/myrepo" fetch origin 2>&1`:                                              "",
				`git -C "/home/dev/myrepo" rev-parse HEAD 2>/dev/null || echo EMPTY`:                       "abc1234",
				`git -C "/home/dev/myrepo" worktree list --porcelain`:                                      "worktree /home/dev/feature-x\nHEAD abc123\nbranch refs/heads/feature-x\n\n",
				`git -C "/home/dev/feature-x" rev-parse --git-dir 2>/dev/null || echo BROKEN`:              ".git/worktrees/feature-x",
				`realpath "/home/dev/feature-x"`:                                                           "/home/dev/feature-x",
				`git -C "/home/dev/feature-x" config --bool core.sparseCheckout 2>/dev/null || echo false`: "true",
				`git -C "/home/dev/myrepo" config extensions.worktreeConfig true`:                          "",
				`git -C "/home/dev/feature-x" sparse-checkout disable`:                                     "",
			},
		},
	}

	wt, err := mgr.FindExistingWorktree(context.Background(), remote, domain.Repo{Name: "myrepo", URL: "git@github.com:owner/myrepo.git"}, "feature-x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wt.Path != "/home/dev/feature-x" {
		t.Fatalf("expected resolved path, got %q", wt.Path)
	}

	joined := strings.Join(remote.commandsRun, "\n")
	if !strings.Contains(joined, `git -C "/home/dev/feature-x" sparse-checkout disable`) {
		t.Fatalf("expected sparse-checkout disable command, got:\n%s", joined)
	}
}
