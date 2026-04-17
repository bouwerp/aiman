package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
)

// knownAgents defines the agents we know how to scan for.
var knownAgents = []domain.Agent{
	{
		Name:        "Claude Code",
		Command:     "claude",
		Description: "Anthropic's Claude Code CLI",
	},
	{
		Name:        "Gemini CLI",
		Command:     "gemini",
		Description: "Google's Gemini CLI",
	},
	{
		Name:        "OpenCode",
		Command:     "opencode",
		Description: "OpenCode interactive CLI",
	},
	{
		Name:        "GitHub Copilot CLI",
		Command:     "copilot",
		Description: "GitHub Copilot in the CLI",
	},
	{
		Name:        "Cursor",
		Command:     "cursor-agent",
		Description: "Cursor AI Code Editor CLI",
	},
	{
		Name:        "Pi",
		Command:     "pi",
		Description: "Pi Coding Agent (shittycodingagent.ai)",
	},
}

// Executor defines the interface for executing remote commands.
type Executor interface {
	Execute(ctx context.Context, cmd string) (string, error)
}

// Scanner scans for available agents on a remote server.
type Scanner struct {
	executor Executor
}

// NewScanner creates a new agent scanner.
func NewScanner(executor Executor) *Scanner {
	return &Scanner{
		executor: executor,
	}
}

// ScanAgents checks which agents are available on the remote server.
// It returns a list of agents that have their binaries in PATH.
func (s *Scanner) ScanAgents(ctx context.Context) ([]domain.Agent, error) {
	var available []domain.Agent

	for _, agent := range knownAgents {
		found := false
		baseCmd := strings.Split(agent.Command, " ")[0]
		found = s.commandExists(ctx, baseCmd)

		if found {
			found = s.verifyAgent(ctx, agent)
		}

		// Special fallbacks for Claude Code
		if !found && agent.Name == "Claude Code" {
			fallbacks := []string{"claude-code", "claude"}
			for _, fb := range fallbacks {
				if fb == agent.Command {
					continue
				}
				if s.commandExists(ctx, fb) {
					agent.Command = fb
					found = true
				}
				if found {
					break
				}
			}
		}

		// Special fallbacks for Cursor
		if !found && agent.Name == "Cursor" {
			fallbacks := []string{"cursor-agent", "cursor-tui", "cursor"}
			for _, fb := range fallbacks {
				if fb == agent.Command {
					continue
				}
				if s.commandExists(ctx, fb) {
					agent.Command = fb
					found = true
				}
				if found {
					break
				}
			}
		}

		// Support both OpenCode binary names.
		if !found && agent.Name == "OpenCode" {
			fallbacks := []string{"opencode", "opencode-cli"}
			for _, fb := range fallbacks {
				if fb == agent.Command {
					continue
				}
				if s.commandExists(ctx, fb) {
					agent.Command = fb
					found = true
				}
				if found {
					break
				}
			}
		}

		// Support both standalone Copilot CLI (`copilot`) and the gh extension (`gh copilot`).
		if !found && strings.Contains(strings.ToLower(agent.Name), "copilot") {
			candidates := []domain.Agent{
				{Name: agent.Name, Command: "copilot", Description: agent.Description},
				{Name: agent.Name, Command: "gh copilot", Description: agent.Description},
				{Name: agent.Name, Command: "ghcs", Description: agent.Description},
				{Name: agent.Name, Command: "github-copilot-cli", Description: agent.Description},
			}
			for _, cand := range candidates {
				base := strings.Split(cand.Command, " ")[0]
				if !s.commandExists(ctx, base) {
					continue
				}
				// Keep copilot detection permissive to avoid false negatives due to
				// shell/profile/auth differences on remote hosts.
				if strings.Contains(cand.Command, " ") && !s.verifyAgent(ctx, cand) {
					continue
				}
				agent.Command = cand.Command
				found = true
				break
			}
			// Last resort: if gh exists, expose gh copilot as an option even when
			// strict checks fail (helps when remote gh copilot help exits non-zero).
			if !found && s.commandExists(ctx, "gh") {
				agent.Command = "gh copilot"
				found = true
			}
		}

		if found {
			available = append(available, agent)
		}
	}

	return available, nil
}

func (s *Scanner) commandExists(ctx context.Context, cmd string) bool {
	pathSuffix := "$HOME/.local/bin:$HOME/.npm-global/bin:$HOME/bin:$HOME/.bun/bin:$HOME/.local/share/pnpm:$HOME/.pnpm:$HOME/.yarn/bin:$HOME/.cargo/bin:$HOME/go/bin:/usr/local/go/bin:/usr/local/bin:/opt/homebrew/bin"
	checks := []string{
		fmt.Sprintf("command -v %s >/dev/null 2>&1", cmd),
		fmt.Sprintf("bash -lc 'command -v %s >/dev/null 2>&1'", cmd),
		fmt.Sprintf("zsh -lc 'command -v %s >/dev/null 2>&1'", cmd),
		fmt.Sprintf("PATH=\"$PATH:%s\" command -v %s >/dev/null 2>&1", pathSuffix, cmd),
		fmt.Sprintf("bash -lc 'export PATH=\"$PATH:%s\"; command -v %s >/dev/null 2>&1'", pathSuffix, cmd),
	}
	for _, c := range checks {
		if _, err := s.executor.Execute(ctx, c); err == nil {
			return true
		}
	}
	return false
}

func (s *Scanner) verifyAgent(ctx context.Context, agent domain.Agent) bool {
	// For simple binaries, existence is enough.
	if !strings.Contains(agent.Command, " ") {
		return true
	}

	// gh copilot needs extension verification, and --version is not reliable.
	if agent.Command == "gh copilot" {
		checks := []string{
			"gh help copilot >/dev/null 2>&1",
			"gh copilot -h >/dev/null 2>&1",
			"gh copilot --help >/dev/null 2>&1",
			"bash -lc 'gh help copilot >/dev/null 2>&1'",
			"bash -lc 'gh copilot -h >/dev/null 2>&1'",
			"bash -lc 'gh copilot --help >/dev/null 2>&1'",
			"gh extension list 2>/dev/null | grep -Eq 'github/gh-copilot|\\bcopilot\\b'",
			"bash -lc 'gh extension list 2>/dev/null | grep -Eq \"github/gh-copilot|\\\\bcopilot\\\\b\"'",
		}
		for _, c := range checks {
			if _, err := s.executor.Execute(ctx, c); err == nil {
				return true
			}
		}
		return false
	}

	// Generic multi-word fallback.
	checks := []string{
		fmt.Sprintf("%s --help >/dev/null 2>&1", agent.Command),
		fmt.Sprintf("bash -lc '%s --help >/dev/null 2>&1'", agent.Command),
	}
	for _, c := range checks {
		if _, err := s.executor.Execute(ctx, c); err == nil {
			return true
		}
	}
	return false
}

// ScanAgentsMsg is a bubbletea message containing the scan results.
type ScanAgentsMsg struct {
	Agents []domain.Agent
	Err    error
}

// ScanCmd returns a bubbletea command that performs the agent scan.
func ScanCmd(scanner domain.AgentScanner) func() tea.Msg {
	return func() tea.Msg {
		ctx := context.Background()
		agents, err := scanner.ScanAgents(ctx)
		return ScanAgentsMsg{
			Agents: agents,
			Err:    err,
		}
	}
}
