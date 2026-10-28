package agent

import (
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"unicode"
)

func TestGuardForEachAgent(t *testing.T) {
	cases := []struct {
		base  string
		env   map[string]string
		flags []string
	}{
		{"claude", map[string]string{"DISABLE_AUTOUPDATER": "1"}, nil},
		{"agy", map[string]string{"AGY_CLI_DISABLE_AUTO_UPDATE": "1"}, nil},
		{"grok", map[string]string{"GROK_DISABLE_AUTOUPDATER": "1"}, []string{"--no-auto-update"}},
		{"codex", nil, []string{"-c check_for_update_on_startup=false", "--disable in_app_updates"}},
		{"kilo", map[string]string{"KILO_DISABLE_AUTOUPDATE": "1"}, nil},
		{"copilot", map[string]string{"COPILOT_AUTO_UPDATE": "false"}, []string{"--no-auto-update"}},
		{"cursor-agent", nil, []string{"--disable-auto-update"}},
		{"pi", map[string]string{"PI_SKIP_VERSION_CHECK": "1"}, nil},
		{"muse", map[string]string{"MUSE_NO_AUTO_UPDATE": "1"}, nil},
		{"deepcode", nil, nil},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		seen[c.base] = true
		g := GuardFor(c.base)
		if len(g.Env) != len(c.env) || (len(c.env) > 0 && !reflect.DeepEqual(g.Env, c.env)) {
			t.Errorf("%s env = %v, want %v", c.base, g.Env, c.env)
		}
		if len(g.Flags) != len(c.flags) || (len(c.flags) > 0 && !reflect.DeepEqual(g.Flags, c.flags)) {
			t.Errorf("%s flags = %v, want %v", c.base, g.Flags, c.flags)
		}
	}
	all := GuardedAgents()
	sort.Strings(all)
	for _, base := range all {
		if !seen[base] {
			t.Errorf("guard for %s has no test row", base)
		}
	}
	if !GuardFor("ageni").Empty() || !GuardFor("").Empty() {
		t.Error("agents without a known switch must get an empty guard")
	}
}

// DISABLE_UPDATES blocks a manual `claude update` too, and PI_OFFLINE cuts
// the model provider off; neither is a self-update guard.
func TestGuardsAvoidSwitchesWithSideEffects(t *testing.T) {
	for _, base := range GuardedAgents() {
		for k := range GuardFor(base).Env {
			if k == "DISABLE_UPDATES" || k == "PI_OFFLINE" {
				t.Errorf("%s guard sets %s", base, k)
			}
		}
	}
}

func TestGuardForAliases(t *testing.T) {
	for alias, want := range map[string]string{
		"claude-code": "claude", "grok-build": "grok", "kilocode": "kilo",
		"cursor": "cursor-agent", "deepcode-launch.sh": "deepcode", "CODEX": "codex",
	} {
		if !reflect.DeepEqual(GuardFor(alias), GuardFor(want)) {
			t.Errorf("GuardFor(%q) differs from GuardFor(%q)", alias, want)
		}
	}
}

func TestGuardForReturnsACopy(t *testing.T) {
	g := GuardFor("claude")
	g.Env["DISABLE_AUTOUPDATER"] = "0"
	if GuardFor("claude").Env["DISABLE_AUTOUPDATER"] != "1" {
		t.Fatal("mutating a returned guard changed the table")
	}
}

// Enter or a digit selects "Update now" (Codex) or "Install" (Deep Code), which
// installs and, for Deep Code, exits the agent.
func TestUpdateDialogKeysNeverConfirm(t *testing.T) {
	if len(UpdateDialogs) == 0 {
		t.Fatal("no update dialogs")
	}
	for _, d := range UpdateDialogs {
		k := strings.ToLower(strings.TrimSpace(d.Key))
		if k == "" || k == "enter" || k == "return" || k == "cr" || k == "y" {
			t.Errorf("%s dismiss key %q confirms the update", d.Agent, d.Key)
		}
		for _, r := range k {
			if unicode.IsDigit(r) {
				t.Errorf("%s dismiss key %q contains a digit", d.Agent, d.Key)
			}
		}
		if _, err := regexp.Compile(d.Re); err != nil {
			t.Errorf("%s pattern: %v", d.Agent, err)
		}
		if strings.ContainsAny(d.Re, `'\`) {
			t.Errorf("%s pattern %q must stay free of quotes and escapes for the remote grep -E", d.Agent, d.Re)
		}
	}
}

func TestBaseOf(t *testing.T) {
	for cmd, want := range map[string]string{
		"codex --cd /w": "codex",
		"DEEPCODE_MODEL=x DEEPCODE_REASONING_EFFORT=high deepcode": "deepcode",
		"$HOME/.aiman/bin/deepcode-launch.sh":                      "deepcode",
		"/home/u/.local/bin/claude --model opus":                   "claude",
		"  Grok-Build --no-auto-update":                            "grok",
		"kilocode --auto":                                          "kilo",
		"cursor-agent .":                                           "cursor-agent",
		"":                                                         "",
		"FOO=1":                                                    "",
	} {
		if got := BaseOf(cmd); got != want {
			t.Errorf("BaseOf(%q) = %q, want %q", cmd, got, want)
		}
	}
}
