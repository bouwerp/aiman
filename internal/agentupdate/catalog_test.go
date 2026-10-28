package agentupdate

import "testing"

func TestParseVersionPerAgent(t *testing.T) {
	cases := []struct {
		agent, out, want string
	}{
		// Real strings captured on host regent.
		{"claude", "2.1.312 (Claude Code)\n", "2.1.312"},
		{"grok", "grok 1.0.46 (3f2a9c1 2026-10-20) [stable]\n", "1.0.46"},
		{"codex", "codex-cli 0.164.0\n", "0.164.0"},
		{"copilot", "GitHub Copilot CLI 1.0.80.\nRun 'copilot --help' for usage.\n", "1.0.80"},
		{"muse", "Muse Code 1.4.13 (linux-x64)\n", "1.4.13"},
		{"ageni", "ageni v0.38.121 (built 2026-10-25T10:00:00Z)\n", "0.38.121"},
		// Plausible strings; the exact output is not verified.
		{"agy", "agy 0.9.3\n", "0.9.3"},
		{"kilo", "7.1.4\n", "7.1.4"},
		{"cursor-agent", "2026.10.20-ab12cd3\n", "2026.10.20-ab12cd3"},
		{"pi", "0.71.2\n", "0.71.2"},
		{"deepcode", "0.6.1\n", "0.6.1"},
	}
	for _, c := range cases {
		e, ok := Lookup(c.agent)
		if !ok {
			t.Fatalf("%s: not in catalog", c.agent)
		}
		got, err := e.ParseVersion(c.out)
		if err != nil {
			t.Fatalf("%s: %v", c.agent, err)
		}
		if got != c.want {
			t.Errorf("%s: got %q want %q", c.agent, got, c.want)
		}
	}
}

func TestParseVersionRejectsNoise(t *testing.T) {
	e, _ := Lookup("claude")
	if _, err := e.ParseVersion("command not found"); err == nil {
		t.Fatal("expected error")
	}
}

func TestCatalogCoversSupportedAgents(t *testing.T) {
	for _, a := range []string{"claude", "agy", "grok", "codex", "kilo", "copilot", "cursor-agent", "pi", "muse", "deepcode", "ageni"} {
		e, ok := Lookup(a)
		if !ok {
			t.Errorf("%s missing", a)
			continue
		}
		if len(e.Update.Argv) == 0 {
			t.Errorf("%s has no update command", a)
		}
	}
}

func TestDetectMethod(t *testing.T) {
	cases := []struct {
		agent, path string
		want        Method
	}{
		{"claude", "/home/u/.local/share/claude/versions/2.1.312", MethodNative},
		{"claude", "/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js", MethodNPM},
		{"grok", "/home/u/.grok/downloads/grok-1.0.46", MethodManaged},
		{"codex", "/home/u/.codex/packages/standalone/releases/0.164.0/codex", MethodStandalone},
		{"kilo", "/home/u/.kilo/bin/kilo", MethodCurl},
		{"pi", "/home/u/.pi/agent/install/releases/0.71.2/pi", MethodManaged},
		{"muse", "/home/u/.local/share/muse/muse-bin-1.4.13", MethodManaged},
		{"deepcode", "/usr/lib/node_modules/@vegamo/deepcode-cli/dist/cli.js", MethodNPM},
		{"deepcode", "/home/u/.bun/install/global/node_modules/@vegamo/deepcode-cli/cli.js", MethodBun},
		{"deepcode", "/home/u/.local/share/pnpm/global/5/node_modules/@vegamo/deepcode-cli/cli.js", MethodPNPM},
		{"deepcode", "/home/u/.config/yarn/global/node_modules/@vegamo/deepcode-cli/cli.js", MethodYarn},
		{"agy", "/home/u/.local/bin/agy", MethodStandalone},
		{"copilot", "/home/u/.local/bin/copilot", MethodStandalone},
		{"ageni", "/home/u/.local/bin/ageni", MethodStandalone},
	}
	for _, c := range cases {
		e, _ := Lookup(c.agent)
		if got := e.DetectMethod(c.path); got != c.want {
			t.Errorf("%s %s: got %s want %s", c.agent, c.path, got, c.want)
		}
	}
}

func TestSwapSafe(t *testing.T) {
	for _, m := range []Method{MethodNative, MethodStandalone, MethodManaged, MethodCurl} {
		if !m.SwapSafe() {
			t.Errorf("%s should be swap safe", m)
		}
	}
	for _, m := range []Method{MethodNPM, MethodYarn, MethodPNPM, MethodBun, MethodUnknown} {
		if m.SwapSafe() {
			t.Errorf("%s should not be swap safe", m)
		}
	}
}

func TestUpdateCommandFollowsPackageManager(t *testing.T) {
	e, _ := Lookup("deepcode")
	if got := e.UpdateCommand(MethodNPM).String(); got != "npm install -g @vegamo/deepcode-cli@latest" {
		t.Errorf("npm: %q", got)
	}
	if got := e.UpdateCommand(MethodBun).String(); got != "bun add -g @vegamo/deepcode-cli@latest" {
		t.Errorf("bun: %q", got)
	}
	c, _ := Lookup("claude")
	if got := c.UpdateCommand(MethodNative).String(); got != "claude update" {
		t.Errorf("claude native: %q", got)
	}
	m, _ := Lookup("muse")
	cmd := m.UpdateCommand(MethodManaged)
	if cmd.Env["MUSE_LAUNCHER_INSTALL"] != "1" || cmd.String() != "muse" {
		t.Errorf("muse: %+v", cmd)
	}
	p, _ := Lookup("pi")
	if got := p.UpdateCommand(MethodManaged).String(); got != "pi update --self" {
		t.Errorf("pi: %q", got)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.46", "1.0.46", 0},
		{"1.0.46", "1.0.47", -1},
		{"1.10.0", "1.9.9", 1},
		{"2.1", "2.1.0", 0},
		{"2026.10.20-ab12cd3", "2026.10.21-ff00aa1", -1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("%s vs %s: got %d want %d", c.a, c.b, got, c.want)
		}
	}
}
