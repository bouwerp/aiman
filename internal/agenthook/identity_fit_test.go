package agenthook

import "testing"

func TestNativeIdentityFitsCommand(t *testing.T) {
	cases := []struct {
		cmd, path string
		want      bool
	}{
		{"claude", "/home/code/.claude/projects/x/a.jsonl", true},
		{"grok --always-approve", "/home/code/.claude/projects/x/a.jsonl", false},
		{"grok", "/home/code/.grok/sessions/a.json", true},
		{"codex", "/home/code/.claude/projects/x/a.jsonl", false},
		{"claude", "", true},
		{"unknown-agent", "/somewhere/else", true},
	}
	for _, c := range cases {
		if got := NativeIdentityFitsCommand(c.cmd, c.path); got != c.want {
			t.Errorf("NativeIdentityFitsCommand(%q, %q)=%v want %v", c.cmd, c.path, got, c.want)
		}
	}
}
