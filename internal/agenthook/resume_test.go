package agenthook

import "testing"

func TestWithResumePerAgent(t *testing.T) {
	tests := []struct {
		cmd, id, want string
	}{
		{"claude --dangerously-skip-permissions", "abc", "claude --resume abc --dangerously-skip-permissions"},
		{"codex --dangerously-bypass-approvals-and-sandbox", "t1", "codex resume t1 --dangerously-bypass-approvals-and-sandbox"},
		{"grok --always-approve", "g1", "grok --resume g1 --always-approve"},
		{"cursor-agent --force .", "c1", "cursor-agent --resume c1 --force ."},
		{"copilot --allow-all", "p1", "copilot --resume=p1 --allow-all"},
		{"agy --dangerously-skip-permissions --add-dir .", "a1", "agy --conversation a1 --dangerously-skip-permissions --add-dir ."},
		{"opencode", "o1", "opencode --session o1"},
		{"pi --new", "s1", "pi --session s1 --new"},
		{"claude --resume already", "x", "claude --resume already"},
		{"claude", "", "claude"},
	}
	for _, tt := range tests {
		if got := WithResume(tt.cmd, tt.id); got != tt.want {
			t.Fatalf("WithResume(%q, %q) = %q want %q", tt.cmd, tt.id, got, tt.want)
		}
	}
}
