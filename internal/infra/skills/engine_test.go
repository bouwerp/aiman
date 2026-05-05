package skills

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// mockRemote captures WriteFile calls and returns success for Execute calls.
type mockRemote struct {
	writtenFiles map[string][]byte
	root         string
}

func newMockRemote() *mockRemote {
	return &mockRemote{writtenFiles: make(map[string][]byte), root: "/home/user/code"}
}

func (m *mockRemote) Connect(ctx context.Context) error                       { return nil }
func (m *mockRemote) GetRoot() string                                         { return m.root }
func (m *mockRemote) Execute(ctx context.Context, cmd string) (string, error) { return "", nil }
func (m *mockRemote) WriteFile(ctx context.Context, path string, content []byte) error {
	m.writtenFiles[path] = content
	return nil
}
func (m *mockRemote) ValidateDir(ctx context.Context, path string) error     { return nil }
func (m *mockRemote) ScanTmuxSessions(ctx context.Context) ([]string, error) { return nil, nil }
func (m *mockRemote) ScanGitRepos(ctx context.Context) ([]string, error)     { return nil, nil }
func (m *mockRemote) ScanWorktrees(ctx context.Context, repoPath string) ([]string, error) {
	return nil, nil
}
func (m *mockRemote) GetGitRoot(ctx context.Context, path string) (string, error) { return "", nil }
func (m *mockRemote) GetTmuxSessionCWD(ctx context.Context, name string) (string, error) {
	return "", nil
}
func (m *mockRemote) GetTmuxSessionEnv(ctx context.Context, name, envVar string) (string, error) {
	return "", nil
}
func (m *mockRemote) CaptureTmuxPane(ctx context.Context, name string) (string, error) {
	return "", nil
}
func (m *mockRemote) AttachTmuxSession(name string) *exec.Cmd { return nil }
func (m *mockRemote) StreamTmuxSession(ctx context.Context, name string) (io.ReadWriteCloser, error) {
	return nil, nil
}
func (m *mockRemote) StartTmuxSession(ctx context.Context, name string) error { return nil }
func (m *mockRemote) ProvisionRemote(ctx context.Context, steps []domain.ProvisionStep, progress chan<- domain.ProvisionProgress) error {
	return nil
}
func (m *mockRemote) Close() error { return nil }

func TestPrepareSession_ClaudeWithIssue_UsesSendKeys(t *testing.T) {
	cfg := &config.Config{}
	engine := NewEngine(cfg)
	remote := newMockRemote()
	ctx := context.Background()

	issue := &domain.Issue{
		Key:         "PROJ-42",
		Summary:     "Implement user authentication",
		Description: "We need OAuth2 support with Google and GitHub providers.",
		Status:      domain.IssueStatusTodo,
		Assignee:    "pieter",
	}

	agent := domain.Agent{Name: "Claude Code", Command: "claude"}

	result, err := engine.PrepareSession(ctx, remote, "/home/user/code/myrepo", agent, nil, true, issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have written .aiman_task.md
	taskContent, ok := remote.writtenFiles["/home/user/code/myrepo/.aiman_task.md"]
	if !ok {
		t.Fatal("expected .aiman_task.md to be written")
	}

	content := string(taskContent)
	if !strings.Contains(content, "PROJ-42") {
		t.Errorf("task file should contain issue key, got: %s", content)
	}
	if !strings.Contains(content, "Implement user authentication") {
		t.Errorf("task file should contain summary, got: %s", content)
	}
	if !strings.Contains(content, "OAuth2 support") {
		t.Errorf("task file should contain description, got: %s", content)
	}

	// Command should have --dangerously-skip-permissions but NOT the prompt
	// (prompt is sent via tmux send-keys to avoid nested quoting breakage)
	if !strings.Contains(result.Command, "--dangerously-skip-permissions") {
		t.Errorf("expected --dangerously-skip-permissions in command, got: %s", result.Command)
	}
	if strings.Contains(result.Command, ".aiman_task.md") {
		t.Errorf("Claude should NOT embed prompt in command (nested quoting breaks tmux), got: %s", result.Command)
	}

	// Should use InitialPrompt for tmux send-keys
	if result.InitialPrompt == "" {
		t.Error("expected non-empty InitialPrompt for Claude (uses send-keys)")
	}
	if !strings.Contains(result.InitialPrompt, ".aiman_task.md") {
		t.Errorf("InitialPrompt should reference .aiman_task.md, got: %s", result.InitialPrompt)
	}
}

func TestPrepareSession_ClaudeWithoutIssue(t *testing.T) {
	cfg := &config.Config{}
	engine := NewEngine(cfg)
	remote := newMockRemote()
	ctx := context.Background()

	agent := domain.Agent{Name: "Claude Code", Command: "claude"}

	result, err := engine.PrepareSession(ctx, remote, "/home/user/code/myrepo", agent, nil, true, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should NOT have written .aiman_task.md
	if _, ok := remote.writtenFiles["/home/user/code/myrepo/.aiman_task.md"]; ok {
		t.Error("should not write task file when no issue provided")
	}

	// Should still have --dangerously-skip-permissions
	if !strings.Contains(result.Command, "--dangerously-skip-permissions") {
		t.Errorf("expected --dangerously-skip-permissions, got: %s", result.Command)
	}

	// No prompt of any kind without an issue
	if result.InitialPrompt != "" {
		t.Errorf("expected empty InitialPrompt without issue, got: %s", result.InitialPrompt)
	}
}

func TestPrepareSession_GeminiWithIssue_UsesSendKeys(t *testing.T) {
	cfg := &config.Config{}
	engine := NewEngine(cfg)
	remote := newMockRemote()
	ctx := context.Background()

	issue := &domain.Issue{
		Key:         "PROJ-99",
		Summary:     "Fix login bug",
		Description: "Users cannot log in with special characters in password.",
		Status:      domain.IssueStatusInProgress,
	}

	agent := domain.Agent{Name: "Gemini CLI", Command: "gemini"}

	result, err := engine.PrepareSession(ctx, remote, "/home/user/code/myrepo", agent, nil, true, issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have written .aiman_task.md
	if _, ok := remote.writtenFiles["/home/user/code/myrepo/.aiman_task.md"]; !ok {
		t.Fatal("expected .aiman_task.md to be written")
	}

	// Command should include --yolo but NOT the prompt (would cause headless exit)
	if !strings.Contains(result.Command, "--yolo") {
		t.Errorf("expected --yolo in command, got: %s", result.Command)
	}
	if strings.Contains(result.Command, ".aiman_task.md") {
		t.Errorf("Gemini should NOT embed prompt in command (risk of headless exit), got: %s", result.Command)
	}

	// Should use InitialPrompt for tmux send-keys instead
	if result.InitialPrompt == "" {
		t.Error("expected non-empty InitialPrompt for Gemini (uses send-keys)")
	}
	if !strings.Contains(result.InitialPrompt, ".aiman_task.md") {
		t.Errorf("InitialPrompt should reference .aiman_task.md, got: %s", result.InitialPrompt)
	}
}

func TestPrepareSession_CursorWithIssue_UsesSendKeys(t *testing.T) {
	cfg := &config.Config{}
	engine := NewEngine(cfg)
	remote := newMockRemote()
	ctx := context.Background()

	issue := &domain.Issue{
		Key:     "PROJ-10",
		Summary: "Add dark mode",
	}

	agent := domain.Agent{Name: "Cursor", Command: "cursor-agent"}

	result, err := engine.PrepareSession(ctx, remote, "/home/user/code/myrepo", agent, nil, true, issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have written .aiman_task.md
	if _, ok := remote.writtenFiles["/home/user/code/myrepo/.aiman_task.md"]; !ok {
		t.Fatal("expected .aiman_task.md to be written")
	}

	// Command should include --force . but NOT the prompt inline
	if !strings.Contains(result.Command, "--force .") {
		t.Errorf("expected --force . in command, got: %s", result.Command)
	}
	if strings.Contains(result.Command, ".aiman_task.md") {
		t.Errorf("Cursor should NOT embed prompt in command (risk of headless exit), got: %s", result.Command)
	}

	// Should use InitialPrompt for tmux send-keys
	if result.InitialPrompt == "" {
		t.Error("expected non-empty InitialPrompt for Cursor (uses send-keys)")
	}
}

func TestPrepareSession_OpenCodeWithIssue_UsesSendKeys(t *testing.T) {
	cfg := &config.Config{}
	engine := NewEngine(cfg)
	remote := newMockRemote()
	ctx := context.Background()

	issue := &domain.Issue{
		Key:     "OPS-5",
		Summary: "Add health check endpoint",
	}

	agent := domain.Agent{Name: "OpenCode", Command: "opencode-cli"}

	result, err := engine.PrepareSession(ctx, remote, "/home/user/code/myrepo", agent, nil, false, issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Command should NOT have the prompt inline
	if strings.Contains(result.Command, ".aiman_task.md") {
		t.Errorf("OpenCode should NOT embed prompt in command, got: %s", result.Command)
	}

	// Should use InitialPrompt for tmux send-keys
	if result.InitialPrompt == "" {
		t.Error("expected non-empty InitialPrompt for OpenCode (uses send-keys)")
	}
}

func TestPrepareSession_CopilotAddsAllowAll(t *testing.T) {
	cfg := &config.Config{}
	engine := NewEngine(cfg)
	remote := newMockRemote()
	ctx := context.Background()

	agent := domain.Agent{Name: "GitHub Copilot CLI", Command: "copilot"}

	result, err := engine.PrepareSession(ctx, remote, "/home/user/code/myrepo", agent, nil, false, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Command, "--allow-all") {
		t.Errorf("expected --allow-all in command, got: %s", result.Command)
	}
	if strings.Contains(result.Command, "--autopilot") {
		t.Errorf("expected no --autopilot when promptFree=false, got: %s", result.Command)
	}
}

func TestPrepareSession_CopilotPromptFreeAddsAutopilot(t *testing.T) {
	cfg := &config.Config{}
	engine := NewEngine(cfg)
	remote := newMockRemote()
	ctx := context.Background()

	agent := domain.Agent{Name: "GitHub Copilot CLI", Command: "copilot"}

	result, err := engine.PrepareSession(ctx, remote, "/home/user/code/myrepo", agent, nil, true, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(result.Command, "--allow-all") {
		t.Errorf("expected --allow-all in command, got: %s", result.Command)
	}
	if !strings.Contains(result.Command, "--autopilot") {
		t.Errorf("expected --autopilot when promptFree=true, got: %s", result.Command)
	}
}

func TestPrepareSession_CopilotWithIssue_IncludesInitialTaskPrompt(t *testing.T) {
	cfg := &config.Config{}
	engine := NewEngine(cfg)
	remote := newMockRemote()
	ctx := context.Background()

	agent := domain.Agent{Name: "GitHub Copilot CLI", Command: "copilot"}
	issue := &domain.Issue{Key: "PROJ-7", Summary: "Wire startup prompt"}

	result, err := engine.PrepareSession(ctx, remote, "/home/user/code/myrepo", agent, nil, false, issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.InitialPrompt == "" {
		t.Fatal("expected non-empty InitialPrompt for copilot with issue")
	}
	if !strings.Contains(result.InitialPrompt, ".aiman_task.md") {
		t.Errorf("expected InitialPrompt to reference .aiman_task.md, got: %s", result.InitialPrompt)
	}
}

func TestPrepareSession_GHCopilotWithIssue_WritesTaskAndPrompt(t *testing.T) {
	cfg := &config.Config{}
	engine := NewEngine(cfg)
	remote := newMockRemote()
	ctx := context.Background()

	agent := domain.Agent{Name: "GitHub Copilot CLI", Command: "gh copilot"}
	issue := &domain.Issue{Key: "PROJ-8", Summary: "Seed prompt via gh copilot path"}

	result, err := engine.PrepareSession(ctx, remote, "/home/user/code/myrepo", agent, nil, false, issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := remote.writtenFiles["/home/user/code/myrepo/.aiman_task.md"]; !ok {
		t.Fatal("expected .aiman_task.md to be written")
	}
	if result.InitialPrompt == "" {
		t.Fatal("expected non-empty InitialPrompt for gh copilot with issue")
	}
	if !strings.Contains(result.InitialPrompt, ".aiman_task.md") {
		t.Errorf("expected InitialPrompt to reference .aiman_task.md, got: %s", result.InitialPrompt)
	}
}

func TestWriteTaskFile_Content(t *testing.T) {
	remote := newMockRemote()
	ctx := context.Background()

	issue := &domain.Issue{
		Key:         "DATA-7",
		Summary:     "Optimize query performance",
		Description: "The dashboard queries are slow.\n\nWe need to add indexes and optimize the JOIN clauses.",
		Status:      domain.IssueStatusTodo,
		Assignee:    "alice",
	}

	err := writeTaskFile(ctx, remote, "/work/repo", issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(remote.writtenFiles["/work/repo/.aiman_task.md"])

	expected := []string{
		"DO NOT COMMIT",
		"Do not commit this file",
		"## DATA-7: Optimize query performance",
		"**Status:** TODO",
		"**Assignee:** alice",
		"### Description",
		"dashboard queries are slow",
		"add indexes",
		// Working guidelines sections
		"# Working Guidelines",
		"## Workflow",
		"## Engineering Principles",
		"## Guardrails",
		"## Communication",
		"Write tests first",
		"TDD",
		"SOLID",
		"DDD",
		"Simplicity over cleverness",
		"Stay on scope",
	}

	for _, s := range expected {
		if !strings.Contains(content, s) {
			t.Errorf("task file should contain %q, got:\n%s", s, content)
		}
	}
}

func TestExpandUserPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got := expandUserPath("~/foo/bar"); got != filepath.Join(home, "foo", "bar") {
		t.Errorf("expandUserPath(~/foo/bar) = %q, want %q", got, filepath.Join(home, "foo", "bar"))
	}
	if got := expandUserPath("/abs/path"); got != "/abs/path" {
		t.Errorf("got %q", got)
	}
}

func TestListSkills_SKILLMdUsesParentDirName(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "jira"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "jira", "SKILL.md"), []byte("# jira"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Skills: config.SkillsConfig{Path: root}}
	engine := NewEngine(cfg)
	list, err := engine.ListSkills()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range list {
		names = append(names, s.Name)
	}
	if !contains(names, "jira") {
		t.Errorf("expected skill name jira, got %v", names)
	}
	if !contains(names, "notes") {
		t.Errorf("expected skill name notes, got %v", names)
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestWriteTaskFile_NoDescription(t *testing.T) {
	remote := newMockRemote()
	ctx := context.Background()

	issue := &domain.Issue{
		Key:     "BUG-1",
		Summary: "Fix crash",
		Status:  domain.IssueStatusTodo,
	}

	err := writeTaskFile(ctx, remote, "/work/repo", issue, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	content := string(remote.writtenFiles["/work/repo/.aiman_task.md"])
	if !strings.Contains(content, "DO NOT COMMIT") {
		t.Errorf("expected do-not-commit notice, got:\n%s", content)
	}
	if !strings.Contains(content, "_No description provided._") {
		t.Errorf("expected placeholder for empty description, got:\n%s", content)
	}
}
