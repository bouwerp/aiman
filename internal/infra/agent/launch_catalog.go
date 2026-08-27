package agent

import "strings"

// LaunchCatalog is the model and reasoning-effort values one agent CLI (or
// its API) documents. Empty Efforts means the CLI has no effort control.
type LaunchCatalog struct {
	Models       []string
	Efforts      []string
	EffortFlag   string // e.g. --effort, --reasoning-effort, --thinking
	EffortConfig string // Codex: model_reasoning_effort via -c
}

// SupportsEffort reports whether the CLI exposes a reasoning-effort control.
func (c LaunchCatalog) SupportsEffort() bool {
	return len(c.Efforts) > 0
}

// CommandBase is the first token of an agent command, lowercased.
func CommandBase(command string) string {
	fields := strings.Fields(strings.ToLower(strings.TrimSpace(command)))
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// LaunchCatalogFor returns the CLI-provided model and effort lists for a
// binary name (claude, grok, agy, …). Unknown binaries have no lists.
func LaunchCatalogFor(base string) LaunchCatalog {
	switch strings.ToLower(strings.TrimSpace(base)) {
	case "claude", "claude-code":
		return claudeCatalog
	case "grok", "grok-build":
		return grokCatalog
	case "agy":
		return agyCatalog
	case "codex":
		return codexCatalog
	case "kilo", "kilocode":
		return kiloCatalog
	case "copilot":
		return copilotCatalog
	case "cursor-agent":
		return cursorCatalog
	case "pi":
		return piCatalog
	default:
		return LaunchCatalog{}
	}
}

// claude --help / code.claude.com model-config aliases and --effort values.
var claudeCatalog = LaunchCatalog{
	Models: []string{
		"sonnet", "opus", "haiku", "fable", "best", "opusplan",
		"sonnet[1m]", "opus[1m]", "claude-fable-5",
	},
	Efforts:    []string{"low", "medium", "high", "xhigh", "max"},
	EffortFlag: "--effort",
}

// grok models and grok --help --reasoning-effort (alias --effort).
var grokCatalog = LaunchCatalog{
	Models:     []string{"grok-4.6", "grok-4.5", "grok-build"},
	Efforts:    []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"},
	EffortFlag: "--reasoning-effort",
}

// agy models and agy --help --effort.
var agyCatalog = LaunchCatalog{
	Models: []string{
		"gemini-3.7-flash-high", "gemini-3.7-flash-medium", "gemini-3.7-flash-low",
		"gemini-3.6-flash-high", "gemini-3.6-flash-medium", "gemini-3.6-flash-low",
		"gemini-3.5-flash-high", "gemini-3.5-flash-medium", "gemini-3.5-flash-low",
		"gemini-3.1-pro-high", "gemini-3.1-pro-low",
		"claude-sonnet-4-6", "claude-opus-4-6-thinking", "gpt-oss-120b-medium",
	},
	Efforts:    []string{"low", "medium", "high"},
	EffortFlag: "--effort",
}

// Codex --model plus -c model_reasoning_effort (docs: minimal|low|medium|high|xhigh).
var codexCatalog = LaunchCatalog{
	Models: []string{
		"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna",
		"gpt-5.5", "gpt-5.4", "gpt-5.4-mini", "gpt-5.3-codex", "o3",
	},
	Efforts:      []string{"minimal", "low", "medium", "high", "xhigh"},
	EffortConfig: "model_reasoning_effort",
}

// kilo models: provider/model IDs from `kilo models`. --variant is reasoning effort.
var kiloCatalog = LaunchCatalog{
	Models: []string{
		"kilo/auto",
		"kilo/minimax-m2.1",
		"anthropic/claude-sonnet-4-6",
		"anthropic/claude-opus-4-6",
		"google/gemini-3.1-pro",
		"openai/gpt-5.2",
		"xai/grok-4.5",
	},
	Efforts:    []string{"minimal", "low", "medium", "high", "max"},
	EffortFlag: "--variant",
}

// copilot --model examples / GitHub Copilot CLI model IDs and --effort choices.
var copilotCatalog = LaunchCatalog{
	Models: []string{
		"auto",
		"gpt-5-mini", "gpt-5.3-codex", "gpt-5.4", "gpt-5.4-mini", "gpt-5.5",
		"gpt-5.6-luna", "gpt-5.6-sol", "gpt-5.6-terra",
		"claude-fable-5", "claude-haiku-4.5", "claude-opus-4.5", "claude-opus-4.6",
		"claude-opus-4.7", "claude-opus-4.8", "claude-opus-5",
		"claude-sonnet-4.5", "claude-sonnet-4.6", "claude-sonnet-5",
		"gemini-3.1-pro", "gemini-3.5-flash", "gemini-3.6-flash", "gemini-3.7-flash",
		"grok-4.5", "grok-4.6", "kimi-k2.7-code", "kimi-k3",
	},
	Efforts:    []string{"none", "low", "medium", "high", "xhigh", "max"},
	EffortFlag: "--effort",
}

// cursor-agent --list-models. Effort is encoded in the model id, not a flag.
var cursorCatalog = LaunchCatalog{
	Models: cursorModels,
}

// pi --model examples from `pi --help` plus google IDs from `pi --list-models`.
// Effort is --thinking.
var piCatalog = LaunchCatalog{
	Models: []string{
		"openai/gpt-4o",
		"openai/gpt-4o-mini",
		"sonnet",
		"google/gemini-2.5-pro",
		"google/gemini-2.5-flash",
		"google/gemini-3-flash-preview",
		"google/gemini-3.1-pro-preview",
	},
	Efforts:    []string{"off", "minimal", "low", "medium", "high", "xhigh"},
	EffortFlag: "--thinking",
}
