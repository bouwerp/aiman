package skills

import (
	"fmt"
	"os"
	"strings"
	"unicode"

	"github.com/bouwerp/aiman/internal/agenthook"
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
	cat := agent.LaunchCatalogFor(base)
	if cat.ModelEnv != "" || cat.EffortEnv != "" {
		return applyEnvLaunchDefaults(cmd, model, effort, cat)
	}
	if model != "" {
		cmd = ensureKeyedFlag(cmd, "--model", model)
	}
	if effort == "" {
		return cmd
	}
	if effortIsBakedIntoModel(base, resolvedModel(cmd, model)) {
		return cmd
	}
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

// applyEnvLaunchDefaults prefixes KEY=value for CLIs that have no model flag.
// A value already in the process environment is left alone: a shell assignment
// in the command would override it. An assignment already on the command is
// also left alone.
func applyEnvLaunchDefaults(cmd, model, effort string, cat agent.LaunchCatalog) string {
	if effort != "" && cat.EffortEnv != "" && cat.SupportsEffort() && os.Getenv(cat.EffortEnv) == "" {
		cmd = ensureEnvAssign(cmd, cat.EffortEnv, effort)
	}
	if model != "" && cat.ModelEnv != "" && os.Getenv(cat.ModelEnv) == "" {
		cmd = ensureEnvAssign(cmd, cat.ModelEnv, model)
	}
	return cmd
}

func ensureEnvAssign(cmd, key, value string) string {
	if !safeEnvKey(key) || !safeEnvValue(value) || strings.Contains(cmd, key+"=") {
		return cmd
	}
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return key + "=" + value
	}
	return key + "=" + value + " " + cmd
}

func safeEnvKey(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || unicode.IsLetter(r):
		case i > 0 && unicode.IsDigit(r):
		default:
			return false
		}
	}
	return true
}

func safeEnvValue(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r == '_' || r == '-' || r == '.' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		return false
	}
	return true
}

// resolvedModel is the model the command will actually run with: the configured
// default when there is one, otherwise whatever --model the command already
// carries.
func resolvedModel(cmd, configured string) string {
	if configured != "" {
		return configured
	}
	const flag = "--model "
	idx := strings.Index(cmd, flag)
	if idx < 0 {
		return ""
	}
	fields := strings.Fields(cmd[idx+len(flag):])
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// effortIsBakedIntoModel reports whether the model name already carries the
// reasoning effort, making a separate effort flag a contradiction.
//
// agy names its models that way — gemini-3.7-flash-low, gpt-oss-120b-medium —
// while also accepting --effort, so a session configured with both was launched
// asking for two different efforts at once.
func effortIsBakedIntoModel(base, model string) bool {
	if base != "agy" && base != "antigravity" {
		return false
	}
	for _, suffix := range []string{"-low", "-medium", "-high"} {
		if strings.HasSuffix(model, suffix) {
			return true
		}
	}
	return false
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
// so the TUI stays in the foreground instead of exiting the holder.
func EnsureInteractiveLaunch(cmd, worktree string) string {
	base := agentBaseCommand(cmd)
	if base == "codex" {
		return withCodexInteractiveFlags(cmd, worktree)
	}
	if base == "kilo" || base == "kilocode" {
		return ensureFlag(cmd, "--auto")
	}
	if base == "grok" || base == "grok-build" {
		return ensureFlag(cmd, "--no-auto-update")
	}
	if strings.Contains(base, "cursor") {
		return ensureFlag(cmd, "--disable-auto-update")
	}
	if base == "muse" {
		// Revive rebuilds from the binary name, so it never sees the
		// PrepareSession flags. A resume without --yolo exits in the
		// permission UI before the conversation is restored.
		cmd = ensureFlag(cmd, "--trust-workspace")
		return ensureFlag(cmd, "--yolo")
	}
	if base == "deepcode" || strings.Contains(base, "deepcode-launch.sh") {
		return agenthook.DeepcodeCommand(cmd)
	}
	return cmd
}

func ensureKeyedFlag(cmd, flag, value string) string {
	if strings.Contains(cmd, flag+" ") || strings.HasSuffix(cmd, flag) {
		return cmd
	}
	return fmt.Sprintf("%s %s %s", cmd, flag, value)
}
