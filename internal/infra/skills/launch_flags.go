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

	// Determine the effective model to check if effort is already baked in
	resolvedModel := model
	if resolvedModel == "" {
		idx := strings.Index(cmd, "--model ")
		if idx >= 0 {
			parts := strings.Fields(cmd[idx+8:])
			if len(parts) > 0 {
				resolvedModel = parts[0]
			}
		}
	}

	if base == "agy" || base == "antigravity" {
		if strings.HasSuffix(resolvedModel, "-low") ||
			strings.HasSuffix(resolvedModel, "-medium") ||
			strings.HasSuffix(resolvedModel, "-high") {
			return cmd // Effort is baked into the model string
		}
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

func ensureKeyedFlag(cmd, flag, value string) string {
	if strings.Contains(cmd, flag+" ") || strings.HasSuffix(cmd, flag) {
		return cmd
	}
	return fmt.Sprintf("%s %s %s", cmd, flag, value)
}
