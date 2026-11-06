package limits

import (
	"slices"
	"testing"
)

func TestProviderFor(t *testing.T) {
	tests := []struct{ agent, model, want string }{
		{"claude", "", "claude"},
		{"codex", "gpt-6", "codex"},
		{"grok", "", "grok"},
		{"muse", "", "muse"},
		{"copilot", "", "copilot"},
		{"opencode", "", "opencode"},
		{"deepcode", "", "deepseek"},
		{"Claude Code", "", "claude"},
		{"Codex CLI", "", "codex"},
		{"Grok Build CLI", "", "grok"},
		{"Muse Code", "", "muse"},
		{"GitHub Copilot CLI", "", "copilot"},
		{"gh copilot", "", "copilot"},
		{"Deep Code", "", "deepseek"},
		{"Antigravity CLI", "", ""},
		{"Pi", "openrouter/x", "openrouter"},
		{"pi", "openrouter/anthropic/claude-x:high", "openrouter"},
		{"pi", "DeepSeek/deepseek-chat", "deepseek"},
		{"pi", "ollama/llama", ""},
		{"pi", "", ""},
		{"agy", "", ""},
		{"", "", ""},
	}
	for _, tt := range tests {
		if got := ProviderFor(tt.agent, tt.model); got != tt.want {
			t.Errorf("ProviderFor(%q, %q) = %q, want %q", tt.agent, tt.model, got, tt.want)
		}
	}
}

// A provider ProviderFor names but no source answers to would never show.
func TestEveryProviderHasASource(t *testing.T) {
	var names []string
	for _, s := range DefaultSources(testEnv(t)) {
		names = append(names, s.Name())
	}
	for _, table := range []map[string]string{agentProviders, piProviders} {
		for _, provider := range table {
			if !slices.Contains(names, provider) {
				t.Errorf("provider %q has no source in %v", provider, names)
			}
		}
	}
}

func TestWindowLeftText(t *testing.T) {
	tests := []struct {
		used float64
		want string
	}{{30, "70% left"}, {66.7, "33.3% left"}, {0, "100% left"}, {100, "0% left"}, {120, "0% left"}}
	for _, tt := range tests {
		if got := (Window{UsedPercent: tt.used}).LeftText(); got != tt.want {
			t.Errorf("used %v: %q, want %q", tt.used, got, tt.want)
		}
	}
}
