package agentupdate

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

// fakeExec answers by matching a substring of the joined argv and records
// every command it is asked to run.
type fakeExec struct {
	mu      sync.Mutex
	answers map[string]string
	fail    map[string]error
	calls   []Command
}

func (f *fakeExec) Run(_ context.Context, c Command) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	joined := strings.Join(c.Argv, " ")
	for k, err := range f.fail {
		if strings.Contains(joined, k) {
			return "", err
		}
	}
	for k, v := range f.answers {
		if strings.Contains(joined, k) {
			return v, nil
		}
	}
	return "", errors.New("exit status 1")
}

type fakeHTTP struct {
	mu     sync.Mutex
	bodies map[string]string
	urls   []string
}

func (f *fakeHTTP) Get(_ context.Context, url string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.urls = append(f.urls, url)
	if b, ok := f.bodies[url]; ok {
		return []byte(b), nil
	}
	return nil, errors.New("404")
}

func everythingInstalled() *fakeExec {
	return &fakeExec{answers: map[string]string{
		"command -v claude":             "/home/u/.local/share/claude/versions/2.1.312\n",
		"command -v agy":                "/home/u/.local/bin/agy\n",
		"command -v grok":               "/home/u/.grok/downloads/grok-1.0.46\n",
		"command -v codex":              "/home/u/.codex/packages/standalone/releases/0.164.0/codex\n",
		"command -v kilo":               "/home/u/.kilo/bin/kilo\n",
		"command -v copilot":            "/home/u/.local/bin/copilot\n",
		"command -v cursor-agent":       "/home/u/.local/share/cursor-agent/versions/2026.10.20-ab12cd3/cursor-agent\n",
		"command -v pi":                 "/home/u/.pi/agent/install/releases/0.71.2/pi\n",
		"command -v muse":               "/home/u/.local/share/muse/muse-bin-1.4.13\n",
		"command -v deepcode":           "/usr/lib/node_modules/@vegamo/deepcode-cli/dist/cli.js\n",
		"command -v ageni":              "/home/u/.local/bin/ageni\n",
		"claude --version":              "2.1.312 (Claude Code)\n",
		"agy --version":                 "agy 0.9.3\n",
		"grok --version":                "grok 1.0.46 (3f2a9c1) [stable]\n",
		"codex --version":               "codex-cli 0.164.0\n",
		"kilo --version":                "7.1.4\n",
		"copilot --version":             "GitHub Copilot CLI 1.0.80.\n",
		"cursor-agent --version":        "2026.10.20-ab12cd3\n",
		"current-version":               "0.71.2\n",
		"muse --version":                "Muse Code 1.4.13 (linux-x64)\n",
		"deepcode --version":            "0.6.1\n",
		"ageni --version":               "ageni v0.38.121 (built 2026-10-25)\n",
		".grok/version.json":            `{"latest_version":"1.0.50"}`,
		".codex/version.json":           `{"latest_version":"0.165.0","last_checked_at":"2026-10-28T00:00:00Z"}`,
		"npm view @kilocode/cli":        "7.2.0\n",
		"npm view @github/copilot":      "1.0.81\n",
		"npm view @vegamo/deepcode-cli": "0.6.2\n",
	}}
}

func TestProbeNeverRunsUpdateSubcommand(t *testing.T) {
	ex := everythingInstalled()
	p := &Prober{Exec: ex, HTTP: &fakeHTTP{}}
	p.ProbeAll(context.Background(), Catalog())
	if len(ex.calls) == 0 {
		t.Fatal("no commands recorded")
	}
	for _, c := range ex.calls {
		joined := " " + strings.Join(c.Argv, " ")
		for _, bad := range []string{" update", " upgrade", "LAUNCHER_INSTALL"} {
			if strings.Contains(joined, bad) {
				t.Errorf("probe ran %q", joined)
			}
		}
		for k := range c.Env {
			if strings.Contains(k, "LAUNCHER_INSTALL") {
				t.Errorf("probe env has %s", k)
			}
		}
		if strings.Contains(joined, "ageni") && !strings.Contains(joined, "--version") && !strings.Contains(joined, "command -v") {
			t.Errorf("ageni probed with %q", joined)
		}
	}
}

func TestProbeSetsMuseNoAutoUpdate(t *testing.T) {
	ex := everythingInstalled()
	p := &Prober{Exec: ex, HTTP: &fakeHTTP{}}
	e, _ := Lookup("muse")
	p.Probe(context.Background(), e)
	seen := false
	for _, c := range ex.calls {
		if strings.Contains(strings.Join(c.Argv, " "), "muse") {
			seen = true
			if c.Env["MUSE_NO_AUTO_UPDATE"] != "1" {
				t.Errorf("muse command without MUSE_NO_AUTO_UPDATE: %v", c.Argv)
			}
		}
	}
	if !seen {
		t.Fatal("muse never probed")
	}
}

func TestProbeFillsState(t *testing.T) {
	ex := everythingInstalled()
	h := &fakeHTTP{bodies: map[string]string{
		"https://pi.dev/api/latest-version":                       `{"version":"0.72.0"}`,
		"https://downloads.claude.ai/claude-code-releases/stable": "2.1.315\n",
	}}
	p := &Prober{Exec: ex, HTTP: h}
	got := p.ProbeAll(context.Background(), Catalog())
	want := map[string][2]string{
		"claude":   {"2.1.312", "2.1.315"},
		"grok":     {"1.0.46", "1.0.50"},
		"codex":    {"0.164.0", "0.165.0"},
		"kilo":     {"7.1.4", "7.2.0"},
		"copilot":  {"1.0.80", "1.0.81"},
		"pi":       {"0.71.2", "0.72.0"},
		"deepcode": {"0.6.1", "0.6.2"},
		"agy":      {"0.9.3", ""},
		"ageni":    {"0.38.121", ""},
	}
	for agent, w := range want {
		s, ok := got[agent]
		if !ok {
			t.Errorf("%s missing", agent)
			continue
		}
		if s.Installed != w[0] || s.Latest != w[1] {
			t.Errorf("%s: installed %q latest %q, want %v", agent, s.Installed, s.Latest, w)
		}
		if s.CheckedAt.IsZero() {
			t.Errorf("%s: no checked_at", agent)
		}
	}
	if got["deepcode"].Method != MethodNPM || got["claude"].Method != MethodNative {
		t.Errorf("methods: %s %s", got["deepcode"].Method, got["claude"].Method)
	}
	if got["claude"].Path != "/home/u/.local/share/claude/versions/2.1.312" {
		t.Errorf("path %q", got["claude"].Path)
	}
}

func TestProbeSkipsMissingAgent(t *testing.T) {
	ex := &fakeExec{answers: map[string]string{
		"command -v claude": "/home/u/.local/share/claude/versions/2.1.312\n",
		"claude --version":  "2.1.312 (Claude Code)\n",
	}}
	p := &Prober{Exec: ex, HTTP: &fakeHTTP{}}
	got := p.ProbeAll(context.Background(), Catalog())
	if len(got) != 1 {
		t.Fatalf("got %d agents, want 1: %v", len(got), got)
	}
	if got["claude"].Installed != "2.1.312" || got["claude"].Method != MethodNative {
		t.Errorf("claude: %+v", got["claude"])
	}
}

func TestProbeRecordsVersionError(t *testing.T) {
	ex := &fakeExec{answers: map[string]string{
		"command -v codex": "/home/u/.codex/packages/standalone/releases/0.164.0/codex\n",
	}, fail: map[string]error{"codex --version": errors.New("boom")}}
	p := &Prober{Exec: ex, HTTP: &fakeHTTP{}}
	e, _ := Lookup("codex")
	s, installed := p.Probe(context.Background(), e)
	if !installed || s.Error == "" {
		t.Fatalf("want error state, got %+v installed=%v", s, installed)
	}
}

func TestProbeRunsWithGuardEnv(t *testing.T) {
	ex := everythingInstalled()
	p := &Prober{Exec: ex, HTTP: &fakeHTTP{}}
	e, _ := Lookup("claude")
	p.Probe(context.Background(), e)
	for _, c := range ex.calls {
		if strings.Contains(strings.Join(c.Argv, " "), "claude --version") && c.Env["DISABLE_AUTOUPDATER"] != "1" {
			t.Errorf("claude probe without guard env: %+v", c)
		}
	}
}
