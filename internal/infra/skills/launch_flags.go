package skills

import (
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/config"
)

func applyConfiguredLaunchFlags(cmd string, ag domain.Agent, cfg *config.Config) string {
	if cfg == nil {
		return cmd
	}
	d := cfg.LaunchDefaultsFor(ag.Name, ag.Command)
	return applyLaunchDefaults(cmd, agentBaseCommand(ag.Command), d)
}

func applyLaunchDefaults(cmd, base string, d config.AgentDefaults) string {
	model := strings.TrimSpace(d.Model)
	effort := strings.TrimSpace(d.Effort)
	if model != "" {
		cmd = ensureKeyedFlag(cmd, "--model", model)
	}
	if effort == "" {
		return cmd
	}
	cat := agent.LaunchCatalogFor(base)
	if !cat.SupportsEffort() {
		return cmd
	}
	if cat.EffortConfig != "" {
		needle := cat.EffortConfig + "="
		if !strings.Contains(cmd, needle) {
			cmd = fmt.Sprintf("%s -c %s=%s", cmd, cat.EffortConfig, effort)
		}
		return cmd
	}
	if cat.EffortFlag != "" {
		cmd = ensureKeyedFlag(cmd, cat.EffortFlag, effort)
	}
	return cmd
}

// withCodexInteractiveFlags is the flag set a PTY/tmux Codex session needs so
// the TUI stays up: skip approval, sandbox, hook-trust, and in-app update
// prompts. A trust or update dialog in this environment exits Codex and the
// holder drops to a bare shell.
func withCodexInteractiveFlags(cmd, worktree string) string {
	cmd = ensureFlag(cmd, "--dangerously-bypass-approvals-and-sandbox")
	cmd = ensureFlag(cmd, "--dangerously-bypass-hook-trust")
	cmd = ensureFlag(cmd, "--disable in_app_updates")
	if strings.TrimSpace(worktree) != "" {
		cmd = ensureKeyedFlag(cmd, "--cd", worktree)
	}
	return cmd
}

// EnsureInteractiveLaunch adds per-agent flags a detached PTY session needs
// so the TUI does not exit into the holder's fallback shell.
func EnsureInteractiveLaunch(cmd, worktree string) string {
	if agentBaseCommand(cmd) == "codex" {
		return withCodexInteractiveFlags(cmd, worktree)
	}
	return cmd
}

func ensureKeyedFlag(cmd, flag, value string) string {
	if strings.Contains(cmd, flag+" ") || strings.HasSuffix(cmd, flag) {
		return cmd
	}
	return fmt.Sprintf("%s %s %s", cmd, flag, value)
}
