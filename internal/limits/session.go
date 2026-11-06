package limits

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/infra/agent"
)

// ClaudeStatuslineOff reports whether this host has opted out of the Claude
// status line wrapper. The marker lives on the host because clients hold no
// configuration for limits.
func ClaudeStatuslineOff(home string) bool {
	_, err := os.Stat(claudeStatuslineOffPath(home))
	return err == nil
}

func claudeStatuslineOffPath(home string) string {
	return filepath.Join(home, ".aiman", "limits", "claude-statusline.off")
}

// agentProviders names the source behind each agent that has one provider.
var agentProviders = map[string]string{
	"claude":   claudeProvider,
	"codex":    "codex",
	"grok":     grokProvider,
	"muse":     "muse",
	"copilot":  copilotName,
	"opencode": openCodeName,
	"deepcode": deepSeekName,
}

// piProviders names the source behind a Pi model's provider segment. Pi reports
// through whichever provider it runs on, so any other segment has no source.
var piProviders = map[string]string{
	"openrouter": openRouterName,
	"deepseek":   deepSeekName,
	"opencode":   openCodeName,
}

// ProviderFor returns the source that reports limits for an agent running a
// model, or "" when it has none.
func ProviderFor(agentName, model string) string {
	base := agent.BaseOf(agentName)
	if base != "pi" {
		return agentProviders[base]
	}
	segment, _, ok := strings.Cut(strings.ToLower(strings.TrimSpace(model)), "/")
	if !ok {
		return ""
	}
	return piProviders[segment]
}
