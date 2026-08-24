package skills

import (
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

func applyConfiguredLaunchFlags(cmd string, agent domain.Agent, cfg *config.Config) string {
	if cfg == nil {
		return cmd
	}
	d := cfg.LaunchDefaultsFor(agent.Name, agent.Command)
	return applyLaunchDefaults(cmd, agentBaseCommand(agent.Command), d)
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
	switch base {
	case "claude", "claude-code":
		cmd = ensureKeyedFlag(cmd, "--effort", effort)
	case "grok", "grok-build":
		cmd = ensureKeyedFlag(cmd, "--reasoning-effort", effort)
	case "codex":
		flag := fmt.Sprintf("-c model_reasoning_effort=%s", effort)
		if !strings.Contains(cmd, "model_reasoning_effort=") {
			cmd = cmd + " " + flag
		}
	}
	return cmd
}

func ensureKeyedFlag(cmd, flag, value string) string {
	if strings.Contains(cmd, flag+" ") || strings.HasSuffix(cmd, flag) {
		return cmd
	}
	return fmt.Sprintf("%s %s %s", cmd, flag, value)
}
