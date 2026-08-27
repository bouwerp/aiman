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
	if cmd == "echo 1" {
		return "1", nil
	}
	if m.canRun != nil && m.canRun(cmd) {
		return "", nil
	}
	return "", fmt.Errorf("command failed")
}

func TestScanAgents_DetectsAntigravity(t *testing.T) {
	exec := &mockExecutor{
		canRun: func(cmd string) bool {
			return strings.Contains(cmd, "agy")
		},
	}
	scanner := NewScanner(exec)

	agents, err := scanner.ScanAgents(context.Background())
	if err != nil {
		t.Fatalf("ScanAgents returned error: %v", err)
	}

	found := false
	for _, a := range agents {
		if a.Name == "Antigravity CLI" {
			found = true
			if a.Command != "agy" {
				t.Errorf("expected command to be agy, got %s", a.Command)
			}
			break
		}
	}
	if !found {
		t.Error("expected Antigravity CLI to be detected as an available agent")
	}
}

func TestScanAgents_DetectsKiloCode(t *testing.T) {
	exec := &mockExecutor{
		canRun: func(cmd string) bool {
			return strings.Contains(cmd, "kilo")
		},
	}
	scanner := NewScanner(exec)

	agents, err := scanner.ScanAgents(context.Background())
	if err != nil {
		t.Fatalf("ScanAgents returned error: %v", err)
	}

	found := false
	for _, a := range agents {
		if a.Name == "Kilo Code" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected Kilo Code to be detected as an available agent")
	}
}

func TestScanAgents_DetectsKiloCodeCLIFallback(t *testing.T) {
	exec := &mockExecutor{
		canRun: func(cmd string) bool {
			return strings.Contains(cmd, "kilocode")
		},
	}
	scanner := NewScanner(exec)

	agents, err := scanner.ScanAgents(context.Background())
	if err != nil {
		t.Fatalf("ScanAgents returned error: %v", err)
	}

	found := false
	for _, a := range agents {
		if a.Name == "Kilo Code" {
			found = true
			if a.Command != "kilocode" {
				t.Errorf("expected command to be kilocode, got %s", a.Command)
			}
			break
		}
	}
	if !found {
		t.Error("expected Kilo Code to be detected via kilocode fallback")
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

	if !scanner.commandExists(context.Background(), "kilo") {
		t.Error("expected commandExists to succeed via zsh -lc fallback")
	}
}

func TestCommandExists_KiloOfficialBinIncluded(t *testing.T) {
	exec := &mockExecutor{
		canRun: func(cmd string) bool {
			return strings.Contains(cmd, ".kilo/bin") && strings.Contains(cmd, "command -v kilo")
		},
	}
	scanner := NewScanner(exec)

	if !scanner.commandExists(context.Background(), "kilo") {
		t.Error("official curl install puts kilo in ~/.kilo/bin")
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

func TestScanAgents_DetectsAgeni(t *testing.T) {
	exec := &mockExecutor{
		canRun: func(cmd string) bool {
			return strings.Contains(cmd, "ageni")
		},
	}
	scanner := NewScanner(exec)

	agents, err := scanner.ScanAgents(context.Background())
	if err != nil {
		t.Fatalf("ScanAgents returned error: %v", err)
	}

	found := false
	for _, a := range agents {
		if a.Name == "Ageni" {
			found = true
			if a.Command != "ageni" {
				t.Errorf("expected command to be ageni, got %s", a.Command)
			}
			break
		}
	}
	if !found {
		t.Error("expected Ageni to be detected as an available agent")
	}
}

func TestScanAgents_DetectsGrok(t *testing.T) {
	exec := &mockExecutor{
		canRun: func(cmd string) bool {
			return strings.Contains(cmd, "grok")
		},
	}
	scanner := NewScanner(exec)

	agents, err := scanner.ScanAgents(context.Background())
	if err != nil {
		t.Fatalf("ScanAgents returned error: %v", err)
	}

	found := false
	for _, a := range agents {
		if a.Name == "Grok Build CLI" {
			found = true
			if a.Command != "grok" {
				t.Errorf("expected command to be grok, got %s", a.Command)
			}
			break
		}
	}
	if !found {
		t.Error("expected Grok Build CLI to be detected as an available agent")
	}
}

func TestScanAgents_DetectsGrokFallback(t *testing.T) {
	exec := &mockExecutor{
		canRun: func(cmd string) bool {
			return strings.Contains(cmd, "grok-build")
		},
	}
	scanner := NewScanner(exec)

	agents, err := scanner.ScanAgents(context.Background())
	if err != nil {
		t.Fatalf("ScanAgents returned error: %v", err)
	}

	found := false
	for _, a := range agents {
		if a.Name == "Grok Build CLI" {
			found = true
			if a.Command != "grok-build" {
				t.Errorf("expected command to be grok-build, got %s", a.Command)
			}
			break
		}
	}
	if !found {
		t.Error("expected Grok Build CLI to be detected via grok-build fallback")
	}
}

func TestScanAgents_DetectsCodex(t *testing.T) {
	exec := &mockExecutor{
		canRun: func(cmd string) bool {
			return strings.Contains(cmd, "codex")
		},
	}
	scanner := NewScanner(exec)

	agents, err := scanner.ScanAgents(context.Background())
	if err != nil {
		t.Fatalf("ScanAgents returned error: %v", err)
	}

	found := false
	for _, a := range agents {
		if a.Name == "Codex CLI" {
			found = true
			if a.Command != "codex" {
				t.Errorf("expected command to be codex, got %s", a.Command)
			}
			break
		}
	}
	if !found {
		t.Error("expected Codex CLI to be detected as an available agent")
	}
}

func TestFindKnown(t *testing.T) {
	a, ok := FindKnown("Codex CLI")
	if !ok {
		t.Fatal("expected Codex CLI to be a known agent")
	}
	if a.Command != "codex" {
		t.Errorf("Command = %q, want codex", a.Command)
	}

	if _, ok := FindKnown("Some Unheard Of CLI"); ok {
		t.Error("expected an unknown agent name to miss")
	}
	if _, ok := FindKnown(""); ok {
		t.Error("expected an empty name to miss")
	}
	if _, ok := FindKnown("  Codex CLI  "); !ok {
		t.Error("expected surrounding whitespace to be trimmed before matching")
	}
	a, ok = FindKnown("codex")
	if !ok || a.Command != "codex" {
		t.Errorf("FindKnown(codex) = %+v ok=%v, want the Codex CLI binary", a, ok)
	}
}
