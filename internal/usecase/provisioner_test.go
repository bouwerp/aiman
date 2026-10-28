package usecase

import (
	"strings"
	"testing"
)

func TestProvisionerInstallsClaudeNatively(t *testing.T) {
	var cmd string
	for _, s := range NewProvisioner(nil).GetSteps() {
		if s.ID == "claude-code" {
			cmd = s.Command
		}
	}
	if cmd == "" {
		t.Fatal("no claude-code step")
	}
	for _, want := range []string{
		"curl -fsSL https://claude.ai/install.sh | bash",
		`export PATH="$HOME/.local/bin:$PATH"`,
		"Claude Code already installed",
		"&& command -v claude;",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("missing %q in %s", want, cmd)
		}
	}
	for _, banned := range []string{"npm", "sudo", "@anthropic-ai/claude-code"} {
		if strings.Contains(cmd, banned) {
			t.Fatalf("claude step uses %q: %s", banned, cmd)
		}
	}
}
