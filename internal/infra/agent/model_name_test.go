package agent

import "testing"

func TestModelName(t *testing.T) {
	for id, want := range map[string]string{
		"claude-fable-5-2":            "Fable 5.2",
		"claude-fable-5-1":            "Fable 5.1",
		"claude-opus-5-5":             "Opus 5.5",
		"claude-fable-5":              "Fable 5",
		"claude-haiku-4-5-20251001":   "Haiku 4.5",
		"claude-sonnet-5-5@fast":      "Sonnet 5.5 fast",
		"gpt-6.2-sol":                 "gpt-6.2-sol",
		"openrouter/qwen/qwen3-coder": "openrouter/qwen/qwen3-coder",
		"":                            "",
	} {
		if got := ModelName(id); got != want {
			t.Errorf("ModelName(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestModelLabel(t *testing.T) {
	for _, c := range []struct{ launch, resolved, want string }{
		{"fable", "claude-fable-5-2", "fable → Fable 5.2"},
		{"", "claude-fable-5-2", "Fable 5.2"},
		{"opus", "", "opus"},
		{"gpt-6.2-sol", "gpt-6.2-sol", "gpt-6.2-sol"},
		{"", "", ""},
	} {
		if got := ModelLabel(c.launch, c.resolved); got != c.want {
			t.Errorf("ModelLabel(%q, %q) = %q, want %q", c.launch, c.resolved, got, c.want)
		}
	}
}
