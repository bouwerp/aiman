package git

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	worktreeDir, err := scopedWorktreeDir("myrepo", "feature-x")
	if err != nil {
		t.Fatal(err)
	}

	// Simulate git worktree add -b failing with "already exists"
	createWithBCmd := fmt.Sprintf("git -C /home/dev/myrepo worktree add -b feature-x ../%s origin/feature-x", worktreeDir)
	createDirectCmd := fmt.Sprintf("git -C /home/dev/myrepo worktree add ../%s feature-x", worktreeDir)

	remote := &mockRemote{
		root: "/home/dev",
		dirs: map[string]bool{"/home/dev/myrepo": true},
		outputs: map[string]string{
			"git -C /home/dev/myrepo fetch origin":                                                         "",
			"git -C /home/dev/myrepo worktree list --porcelain":                                            "",
			fmt.Sprintf(`bash -c 'if [ -d "/home/dev/myrepo/../%s" ]; then echo EXISTS; fi'`, worktreeDir): "",
			`bash -c 'if [ -d "/home/dev/myrepo/../feature-x" ]; then echo EXISTS; fi'`:                    "",
			createWithBCmd:  "fatal: a branch named 'feature-x' already exists",
			createDirectCmd: "",
			fmt.Sprintf(`realpath "/home/dev/myrepo/../%s"`, worktreeDir): fmt.Sprintf("/home/dev/%s", worktreeDir),
		},
		errors: map[string]error{
			createWithBCmd: fmt.Errorf("exit status 128"),
		},
	}

	wt, err := mgr.SetupRemoteWorktreeFromBranch(context.Background(), remote, domain.Repo{Name: "myrepo"}, "feature-x")
	if err != nil {
		t.Fatalf("expected fallback to succeed, got error: %v", err)
	}
	if wt.Path != fmt.Sprintf("/home/dev/%s", worktreeDir) {
		t.Errorf("expected resolved path /home/dev/%s, got %q", worktreeDir, wt.Path)
	}
}

func TestSetupRemoteWorktreeFromBranch_DirectoryAlreadyExists(t *testing.T) {
	mgr := NewManager(nil)
	worktreeDir, err := scopedWorktreeDir("myrepo", "feature-x")
	if err != nil {
		t.Fatal(err)
	}

	remote := &mockRemote{
		root: "/home/dev",
		dirs: map[string]bool{"/home/dev/myrepo": true},
		outputs: map[string]string{
			"git -C /home/dev/myrepo fetch origin":                                                         "",
			"git -C /home/dev/myrepo worktree list --porcelain":                                            "",
			fmt.Sprintf(`bash -c 'if [ -d "/home/dev/myrepo/../%s" ]; then echo EXISTS; fi'`, worktreeDir): "EXISTS",
		},
	}

	_, err = mgr.SetupRemoteWorktreeFromBranch(context.Background(), remote, domain.Repo{Name: "myrepo"}, "feature-x")
	if err == nil {
		t.Fatal("expected WORKTREE_EXISTS error, got nil")
	}
	if err.Error() != "WORKTREE_EXISTS" {
		t.Errorf("expected 'WORKTREE_EXISTS', got: %v", err)
	}
}

func TestScopedWorktreeDir_NamespacesByRepo(t *testing.T) {
	first, err := scopedWorktreeDir("org/repo-a", "feature-x")
	if err != nil {
		t.Fatal(err)
	}
	second, err := scopedWorktreeDir("org/repo-b", "feature-x")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("expected repo-scoped worktree dirs, got identical %q", first)
	}
	if first != "repo-a@feature-x" || second != "repo-b@feature-x" {
		t.Fatalf("unexpected scoped worktree dirs: %q %q", first, second)
	}
}

func TestScopedWorktreeDir_UsesCollisionSafeSeparator(t *testing.T) {
	first, err := scopedWorktreeDir("org/foo--bar", "baz")
	if err != nil {
		t.Fatal(err)
	}
	second, err := scopedWorktreeDir("org/foo", "bar--baz")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("expected separator to avoid collisions, both were %q", first)
	}
}

func TestSetupRemoteWorktree_RejectsEmptyBranchDir(t *testing.T) {
	if _, err := scopedWorktreeDir("myrepo", "   "); err == nil {
		t.Fatal("expected empty branch name error")
	}
}

func TestFindExistingWorktree_FallsBackToLegacyPathForSameRepo(t *testing.T) {
	mgr := NewManager(nil)
	worktreeDir, err := scopedWorktreeDir("myrepo", "feature-x")
	if err != nil {
		t.Fatal(err)
	}
	remote := &commandRecorderRemote{
		mockRemote: mockRemote{
			root: "/home/dev",
			dirs: map[string]bool{"/home/dev/myrepo": true},
			outputs: map[string]string{
				`git -C "/home/dev/myrepo" remote get-url origin 2>/dev/null`:                                  "git@github.com:owner/myrepo.git",
				`git -C "/home/dev/myrepo" rev-parse --git-dir`:                                                ".git",
				`git -C "/home/dev/myrepo" fetch origin 2>&1`:                                                  "",
				`git -C "/home/dev/myrepo" rev-parse HEAD 2>/dev/null || echo EMPTY`:                           "abc1234",
				`git -C "/home/dev/myrepo" worktree list --porcelain`:                                          "",
				fmt.Sprintf(`bash -c 'if [ -d "/home/dev/myrepo/../%s" ]; then echo EXISTS; fi'`, worktreeDir): "",
				`bash -c 'if [ -d "/home/dev/myrepo/../feature-x" ]; then echo EXISTS; fi'`:                    "EXISTS",
				`git -C "/home/dev/myrepo/../feature-x" rev-parse --git-dir 2>/dev/null || echo BROKEN`:        ".git/worktrees/feature-x",
				`git -C "/home/dev/myrepo/../feature-x" rev-parse --git-common-dir 2>/dev/null || echo BROKEN`: "/home/dev/myrepo/.git",
				`realpath "/home/dev/myrepo/../feature-x"`:                                                     "/home/dev/feature-x",
				`git -C "/home/dev/feature-x" config --bool core.sparseCheckout 2>/dev/null || echo false`:     "false",
			},
		},
	}

	wt, err := mgr.FindExistingWorktree(context.Background(), remote, domain.Repo{Name: "myrepo", URL: "git@github.com:owner/myrepo.git"}, "feature-x")
	if err != nil {
		t.Fatalf("expected legacy fallback to succeed, got error: %v", err)
	}
	if wt.Path != "/home/dev/feature-x" {
		t.Fatalf("expected legacy resolved path, got %q", wt.Path)
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
	scanCmd := "bash -ce " + shellSingleQuote(linkedWorktreeScanScript(repoPath))
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
	scanCmd := "bash -ce " + shellSingleQuote(linkedWorktreeScanScript(repoPath))
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
	worktreeDir, err := scopedWorktreeDir("myrepo", "feature-x")
	if err != nil {
		t.Fatal(err)
	}
	remote := &commandRecorderRemote{
		mockRemote: mockRemote{
			root: "/home/dev",
			dirs: map[string]bool{"/home/dev/myrepo": true},
			outputs: map[string]string{
				`git -C "/home/dev/myrepo" remote get-url origin 2>/dev/null`:                                                 "git@github.com:owner/myrepo.git",
				`git -C "/home/dev/myrepo" rev-parse --git-dir`:                                                               ".git",
				`git -C "/home/dev/myrepo" fetch origin 2>&1`:                                                                 "",
				`git -C "/home/dev/myrepo" rev-parse HEAD 2>/dev/null || echo EMPTY`:                                          "abc1234",
				fmt.Sprintf(`bash -c 'if [ -d "/home/dev/myrepo/../%s" ]; then echo EXISTS; fi'`, worktreeDir):                "",
				`bash -c 'if [ -d "/home/dev/myrepo/../feature-x" ]; then echo EXISTS; fi'`:                                   "",
				`git -C "/home/dev/myrepo" rev-parse --verify origin/main`:                                                    "abc1234",
				fmt.Sprintf(`git -C "/home/dev/myrepo" worktree add -B feature-x ../%s origin/main`, worktreeDir):             "",
				fmt.Sprintf(`realpath "/home/dev/myrepo/../%s"`, worktreeDir):                                                 fmt.Sprintf("/home/dev/%s", worktreeDir),
				fmt.Sprintf(`git -C "/home/dev/%s" config --bool core.sparseCheckout 2>/dev/null || echo false`, worktreeDir): "true",
				`git -C "/home/dev/myrepo" config extensions.worktreeConfig true`:                                             "",
				fmt.Sprintf(`git -C "/home/dev/%s" sparse-checkout disable`, worktreeDir):                                     "",
			},
		},
	}

	wt, err := mgr.SetupRemoteWorktree(context.Background(), remote, domain.Repo{Name: "myrepo", URL: "git@github.com:owner/myrepo.git"}, "feature-x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wt.Path != fmt.Sprintf("/home/dev/%s", worktreeDir) {
		t.Fatalf("expected resolved path, got %q", wt.Path)
	}

	joined := strings.Join(remote.commandsRun, "\n")
	if !strings.Contains(joined, `git -C "/home/dev/myrepo" config extensions.worktreeConfig true`) {
		t.Fatalf("expected worktreeConfig enable command, got:\n%s", joined)
	}
	if !strings.Contains(joined, fmt.Sprintf(`git -C "/home/dev/%s" sparse-checkout disable`, worktreeDir)) {
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

func TestDisableSparseCheckoutLocal_DisablesSparseCheckout(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repoPath := t.TempDir()
	runGit(t, repoPath, "init")
	runGit(t, repoPath, "config", "user.name", "Aiman Test")
	runGit(t, repoPath, "config", "user.email", "aiman@example.com")

	dashboardPath := filepath.Join(repoPath, "frontend", "app", "src", "pages", "Dashboard")
	hooksPath := filepath.Join(repoPath, "frontend", "app", "src", "hooks")
	if err := os.MkdirAll(dashboardPath, 0o755); err != nil {
		t.Fatalf("mkdir dashboard path: %v", err)
	}
	if err := os.MkdirAll(hooksPath, 0o755); err != nil {
		t.Fatalf("mkdir hooks path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dashboardPath, "index.tsx"), []byte("export const Dashboard = () => null\n"), 0o644); err != nil {
		t.Fatalf("write dashboard file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hooksPath, "useFoo.ts"), []byte("export const useFoo = () => null\n"), 0o644); err != nil {
		t.Fatalf("write hooks file: %v", err)
	}

	runGit(t, repoPath, "add", ".")
	runGit(t, repoPath, "commit", "-m", "seed")
	runGit(t, repoPath, "sparse-checkout", "init", "--cone")
	runGit(t, repoPath, "sparse-checkout", "set", "frontend/app/src/hooks")

	if _, err := os.Stat(filepath.Join(dashboardPath, "index.tsx")); !os.IsNotExist(err) {
		t.Fatalf("expected dashboard file to be absent in sparse checkout, err=%v", err)
	}

	if err := DisableSparseCheckoutLocal(context.Background(), repoPath); err != nil {
		t.Fatalf("DisableSparseCheckoutLocal returned error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dashboardPath, "index.tsx")); err != nil {
		t.Fatalf("expected dashboard file restored after disabling sparse checkout: %v", err)
	}
}

func TestDisableSparseCheckoutLocal_NoOpForFullWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	repoPath := t.TempDir()
	runGit(t, repoPath, "init")
	runGit(t, repoPath, "config", "user.name", "Aiman Test")
	runGit(t, repoPath, "config", "user.email", "aiman@example.com")
	if err := os.WriteFile(filepath.Join(repoPath, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, repoPath, "add", "README.md")
	runGit(t, repoPath, "commit", "-m", "seed")

	if err := DisableSparseCheckoutLocal(context.Background(), repoPath); err != nil {
		t.Fatalf("DisableSparseCheckoutLocal returned error: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(repoPath, "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if string(content) != "hello\n" {
		t.Fatalf("unexpected README content: %q", string(content))
	}
}

func TestDetectBaseBranch_UsesOriginHEAD(t *testing.T) {
	remote := &mockRemote{
		root: "/home/dev",
		outputs: map[string]string{
			`git -C "/home/dev/myrepo" rev-parse --abbrev-ref refs/remotes/origin/HEAD 2>/dev/null`: "origin/develop",
		},
	}
	got := detectBaseBranch(context.Background(), remote, "/home/dev/myrepo")
	if got != "origin/develop" {
		t.Errorf("expected origin/develop, got %q", got)
	}
}

func TestDetectBaseBranch_FallsBackToWellKnownNames(t *testing.T) {
	remote := &mockRemote{
		root: "/home/dev",
		outputs: map[string]string{
			// origin/HEAD not set, origin/main not present, origin/master present
			`git -C "/home/dev/myrepo" rev-parse --abbrev-ref refs/remotes/origin/HEAD 2>/dev/null`: "refs/remotes/origin/HEAD",
			`git -C "/home/dev/myrepo" rev-parse --verify origin/master 2>/dev/null`:                "abc1234",
		},
		errors: map[string]error{
			`git -C "/home/dev/myrepo" rev-parse --verify origin/main 2>/dev/null`: fmt.Errorf("exit 128"),
		},
	}
	got := detectBaseBranch(context.Background(), remote, "/home/dev/myrepo")
	if got != "origin/master" {
		t.Errorf("expected origin/master, got %q", got)
	}
}

func TestDetectBaseBranch_FallsBackToHEAD(t *testing.T) {
	remote := &mockRemote{
		root: "/home/dev",
		errors: map[string]error{
			`git -C "/home/dev/myrepo" rev-parse --abbrev-ref refs/remotes/origin/HEAD 2>/dev/null`: fmt.Errorf("exit 128"),
			`git -C "/home/dev/myrepo" rev-parse --verify origin/main 2>/dev/null`:                  fmt.Errorf("exit 128"),
			`git -C "/home/dev/myrepo" rev-parse --verify origin/master 2>/dev/null`:                fmt.Errorf("exit 128"),
			`git -C "/home/dev/myrepo" rev-parse --verify main 2>/dev/null`:                         fmt.Errorf("exit 128"),
			`git -C "/home/dev/myrepo" rev-parse --verify master 2>/dev/null`:                       fmt.Errorf("exit 128"),
		},
		outputs: map[string]string{
			`git -C "/home/dev/myrepo" rev-parse HEAD 2>/dev/null`: "abc1234",
		},
	}
	got := detectBaseBranch(context.Background(), remote, "/home/dev/myrepo")
	if got != "HEAD" {
		t.Errorf("expected HEAD fallback, got %q", got)
	}
}

func TestDetectBaseBranch_EmptyRepoReturnsEmpty(t *testing.T) {
	remote := &mockRemote{root: "/home/dev", errors: map[string]error{
		`git -C "/home/dev/myrepo" rev-parse --abbrev-ref refs/remotes/origin/HEAD 2>/dev/null`: fmt.Errorf("exit 128"),
		`git -C "/home/dev/myrepo" rev-parse --verify origin/main 2>/dev/null`:                  fmt.Errorf("exit 128"),
		`git -C "/home/dev/myrepo" rev-parse --verify origin/master 2>/dev/null`:                fmt.Errorf("exit 128"),
		`git -C "/home/dev/myrepo" rev-parse --verify main 2>/dev/null`:                         fmt.Errorf("exit 128"),
		`git -C "/home/dev/myrepo" rev-parse --verify master 2>/dev/null`:                       fmt.Errorf("exit 128"),
		`git -C "/home/dev/myrepo" rev-parse HEAD 2>/dev/null`:                                  fmt.Errorf("exit 128"),
	}}
	got := detectBaseBranch(context.Background(), remote, "/home/dev/myrepo")
	if got != "" {
		t.Errorf("expected empty string for empty repo, got %q", got)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return strings.TrimSpace(string(out))
}
