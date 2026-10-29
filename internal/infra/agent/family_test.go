package agent

import "testing"

func TestFamily(t *testing.T) {
	tests := []struct {
		name    string
		command string
		model   string
		want    string
	}{
		{name: "claude binary", command: "claude", want: "anthropic"},
		{name: "codex binary with model", command: "codex", model: "gpt-5.4", want: "openai"},
		{name: "grok binary", command: "grok", want: "xai"},
		{name: "agy binary", command: "agy", want: "google"},
		{name: "absolute binary path", command: "/home/u/.local/bin/claude", want: "anthropic"},
		{name: "pi provider prefix with thinking suffix", command: "pi", model: "anthropic/claude-opus-4-7:high", want: "anthropic"},
		{name: "pi openai provider prefix", command: "pi", model: "openai/gpt-5.4", want: "openai"},
		{name: "pi provider prefix not a family falls back to model id", command: "pi", model: "openrouter/anthropic/claude-sonnet-4.5", want: "anthropic"},
		{name: "copilot gpt model", command: "copilot", model: "gpt-5-mini", want: "openai"},
		{name: "copilot claude model", command: "copilot", model: "claude-sonnet-4.5", want: "anthropic"},
		{name: "cursor gemini model", command: "cursor-agent", model: "gemini-3-pro", want: "google"},
		{name: "deepcode deepseek model", command: "deepcode", model: "deepseek-v4", want: "deepseek"},
		{name: "kilo with no model", command: "kilo", want: ""},
		{name: "unknown model", command: "pi", model: "mistral-large", want: ""},
		{name: "empty command and model", command: "", model: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Family(tt.command, tt.model); got != tt.want {
				t.Fatalf("Family(%q, %q) = %q, want %q", tt.command, tt.model, got, tt.want)
			}
		})
	}
}
