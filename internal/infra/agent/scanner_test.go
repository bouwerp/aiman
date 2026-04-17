package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// mockExecutor records executed commands and returns success/failure based on
// a predicate function.
type mockExecutor struct {
	commands []string
	canRun   func(cmd string) bool
}

func (m *mockExecutor) Execute(_ context.Context, cmd string) (string, error) {
	m.commands = append(m.commands, cmd)
	if m.canRun != nil && m.canRun(cmd) {
		return "", nil
	}
	return "", fmt.Errorf("command failed")
}

func TestScanAgents_DetectsOpenCode(t *testing.T) {
	exec := &mockExecutor{
		canRun: func(cmd string) bool {
			// Simulate opencode being discoverable via command -v.
			return strings.Contains(cmd, "opencode")
		},
	}
	scanner := NewScanner(exec)

	agents, err := scanner.ScanAgents(context.Background())
	if err != nil {
		t.Fatalf("ScanAgents returned error: %v", err)
	}

	found := false
	for _, a := range agents {
		if a.Name == "OpenCode" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected OpenCode to be detected as an available agent")
	}
}

func TestScanAgents_DetectsOpenCodeCLIFallback(t *testing.T) {
	exec := &mockExecutor{
		canRun: func(cmd string) bool {
			// Only the opencode-cli binary exists.
			return strings.Contains(cmd, "opencode-cli")
		},
	}
	scanner := NewScanner(exec)

	agents, err := scanner.ScanAgents(context.Background())
	if err != nil {
		t.Fatalf("ScanAgents returned error: %v", err)
	}

	found := false
	for _, a := range agents {
		if a.Name == "OpenCode" {
			found = true
			if a.Command != "opencode-cli" {
				t.Errorf("expected command to be opencode-cli, got %s", a.Command)
			}
			break
		}
	}
	if !found {
		t.Error("expected OpenCode to be detected via opencode-cli fallback")
	}
}

func TestCommandExists_TriesZshLogin(t *testing.T) {
	exec := &mockExecutor{
		canRun: func(cmd string) bool {
			// Only zsh login shell finds the command.
			return strings.HasPrefix(cmd, "zsh -lc")
		},
	}
	scanner := NewScanner(exec)

	if !scanner.commandExists(context.Background(), "opencode") {
		t.Error("expected commandExists to succeed via zsh -lc fallback")
	}
}

func TestCommandExists_GoPathIncluded(t *testing.T) {
	exec := &mockExecutor{
		canRun: func(cmd string) bool {
			// Only succeeds when $HOME/go/bin is in the PATH.
			return strings.Contains(cmd, "go/bin") && strings.Contains(cmd, "command -v mybin")
		},
	}
	scanner := NewScanner(exec)

	if !scanner.commandExists(context.Background(), "mybin") {
		t.Error("expected commandExists to succeed when go/bin is in extended PATH")
	}
}
