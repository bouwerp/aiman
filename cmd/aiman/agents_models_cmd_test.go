package main

import (
	"bytes"
	"testing"
)

func TestParseAgentsModels(t *testing.T) {
	call, err := parseAgentsArgs([]string{"models", "claude", "--refresh"})
	if err != nil || call.method != "agents.models" || call.params["agent"] != "claude" || call.params["refresh"] != true {
		t.Fatalf("call %+v err %v", call, err)
	}
	if _, err := parseAgentsArgs([]string{"models"}); err == nil {
		t.Fatal("models with no agent accepted")
	}
}

// TestFormatAgentModelsOnePerLine prints the ids the TUI's model picker reads
// over ssh.
func TestFormatAgentModelsOnePerLine(t *testing.T) {
	var b bytes.Buffer
	err := formatAgentModels(&b, map[string]any{"type": "agent_models", "agent": "claude", "models": []any{"fable", "claude-fable-5-2"}})
	if err != nil || b.String() != "fable\nclaude-fable-5-2\n" {
		t.Fatalf("out %q err %v", b.String(), err)
	}
}
