package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/agentupdate"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// claudeHost fakes a host where claude 1.0.0 is installed and 1.0.1 is out.
type claudeHost struct {
	mu        sync.Mutex
	installed string
	fail      bool
	env       []map[string]string
}

func (h *claudeHost) Run(_ context.Context, c agentupdate.Command) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.env = append(h.env, c.Env)
	j := strings.Join(c.Argv, " ")
	switch {
	case strings.Contains(j, "command -v claude"):
		return "/home/u/.local/share/claude/versions/" + h.installed + "\n", nil
	case j == "claude --version":
		return h.installed + " (Claude Code)\n", nil
	case j == "claude update":
		if h.fail {
			return "boom\n", errors.New("exit status 1")
		}
		h.installed = "1.0.1"
		return "ok\n", nil
	}
	return "", errors.New("exit status 127")
}

type claudeLatest struct{}

func (claudeLatest) Get(context.Context, string) ([]byte, error) { return []byte("1.0.1\n"), nil }

func TestServeReinstallsHooksAfterUpdate(t *testing.T) {
	for _, fail := range []bool{false, true} {
		dir := t.TempDir()
		host := &claudeHost{installed: "1.0.0", fail: fail}
		ensured := 0
		m := newAgentUpdates(config.AgentUpdateConfig{Mode: "notify"}, dir, agentUpdateDeps{
			Exec:        host,
			HTTP:        claudeLatest{},
			EnsureHooks: func() { ensured++ },
			Logf:        t.Logf,
		})
		claude, _ := agentupdate.Lookup("claude")
		m.Entries = []agentupdate.Entry{claude}
		out, err := m.Request(context.Background(), "claude", false)
		if err != nil || len(out) != 1 || !out[0].Accepted {
			t.Fatalf("fail=%v: %+v %v", fail, out, err)
		}
		m.Wait()
		want := 1
		if fail {
			want = 0
		}
		if ensured != want {
			t.Fatalf("fail=%v: hooks reinstalled %d times, want %d", fail, ensured, want)
		}
		if _, err := os.Stat(filepath.Join(dir, "agent-versions.json")); err != nil {
			t.Fatalf("state: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "logs", "agent-updates.log")); err != nil {
			t.Fatalf("log: %v", err)
		}
	}
}

func TestNewAgentUpdatesPolicy(t *testing.T) {
	dir := t.TempDir()
	m := newAgentUpdates(config.AgentUpdateConfig{Hold: []string{"muse"}}, dir, agentUpdateDeps{Logf: t.Logf})
	if m.Policy.Mode != agentupdate.ModeAuto || m.Policy.Window.String() != "03:00-06:00" || m.CheckEvery != 6*time.Hour || !m.Policy.Held("muse") {
		t.Fatalf("defaults: %+v every %s", m.Policy, m.CheckEvery)
	}
	if m.FirstProbeDelay != 2*time.Minute {
		t.Fatalf("first probe %s", m.FirstProbeDelay)
	}
	bad := newAgentUpdates(config.AgentUpdateConfig{Mode: "sometimes", Window: "nope", CheckEvery: time.Hour}, dir, agentUpdateDeps{Logf: t.Logf})
	if bad.Policy.Mode != agentupdate.ModeNotify || bad.CheckEvery != time.Hour {
		t.Fatalf("invalid config should fall back to notify: %+v", bad.Policy)
	}
}

type fakeNotifier struct {
	mu   sync.Mutex
	sent []string
}

func (f *fakeNotifier) Emit(_ context.Context, sessionID, label string, kind domain.NotifyKind, detail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, fmt.Sprintf("%s|%s|%s|%s", sessionID, label, kind, detail))
	return nil
}

func TestAgentUpdateNotificationTextPerMode(t *testing.T) {
	inWindow := func() time.Time { return time.Date(2026, 10, 28, 4, 0, 0, 0, time.Local) }
	cases := []struct {
		name string
		cfg  config.AgentUpdateConfig
		fail bool
		want []string
	}{
		{"notify", config.AgentUpdateConfig{Mode: "notify"}, false,
			[]string{"|claude|agent_update|claude 1.0.0 → 1.0.1 on regent (press a on Agent API)"}},
		{"auto", config.AgentUpdateConfig{Mode: "auto"}, false,
			[]string{"|claude|agent_update|updated claude 1.0.0 → 1.0.1"}},
		{"auto failed", config.AgentUpdateConfig{Mode: "auto"}, true,
			[]string{"|claude|agent_update|claude update failed: claude update: exit status 1"}},
		{"auto held", config.AgentUpdateConfig{Mode: "auto", Hold: []string{"claude"}}, false,
			[]string{"|claude|agent_update|claude 1.0.0 → 1.0.1 on regent (press a on Agent API)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host := &claudeHost{installed: "1.0.0", fail: tc.fail}
			m := newAgentUpdates(tc.cfg, t.TempDir(), agentUpdateDeps{Exec: host, HTTP: claudeLatest{}, Logf: t.Logf})
			claude, _ := agentupdate.Lookup("claude")
			m.Entries = []agentupdate.Entry{claude}
			m.Now = inWindow
			n := &fakeNotifier{}
			wireAgentUpdateNotify(context.Background(), m, n, "regent")
			m.Sweep(context.Background())
			m.Sweep(context.Background())
			m.Wait()
			if strings.Join(n.sent, "\n") != strings.Join(tc.want, "\n") {
				t.Fatalf("sent %q, want %q", n.sent, tc.want)
			}
		})
	}
}

func TestManualUpdateIsNotNotified(t *testing.T) {
	host := &claudeHost{installed: "1.0.0"}
	m := newAgentUpdates(config.AgentUpdateConfig{Mode: "auto", Hold: []string{"claude"}}, t.TempDir(), agentUpdateDeps{Exec: host, HTTP: claudeLatest{}, Logf: t.Logf})
	claude, _ := agentupdate.Lookup("claude")
	m.Entries = []agentupdate.Entry{claude}
	n := &fakeNotifier{}
	wireAgentUpdateNotify(context.Background(), m, n, "regent")
	if _, err := m.Request(context.Background(), "claude", false); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	for _, s := range n.sent {
		if strings.Contains(s, "updated") {
			t.Fatalf("manual update notified: %q", n.sent)
		}
	}
}

func TestAgentUpdatePathExecExtendsPath(t *testing.T) {
	host := &claudeHost{installed: "1.0.0"}
	ex := pathExec{base: host, home: "/home/u"}
	if _, err := ex.Run(context.Background(), agentupdate.Command{Argv: []string{"claude", "--version"}}); err != nil {
		t.Fatal(err)
	}
	p := host.env[0]["PATH"]
	if !strings.Contains(p, "/home/u/.local/bin") || !strings.Contains(p, "/home/u/.npm-global/bin") {
		t.Fatalf("PATH = %q", p)
	}
	if _, err := ex.Run(context.Background(), agentupdate.Command{Argv: []string{"claude", "--version"}, Env: map[string]string{"PATH": "/x"}}); err != nil {
		t.Fatal(err)
	}
	if host.env[1]["PATH"] != "/x" {
		t.Fatalf("explicit PATH overwritten: %q", host.env[1]["PATH"])
	}
}
