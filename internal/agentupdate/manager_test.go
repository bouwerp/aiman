package agentupdate

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// hostExec is a fake host with two agents: claude (native, swap-safe) and
// deepcode (npm, in place). An update command bumps the installed version to
// the latest one, unless failWith is set.
type hostExec struct {
	mu        sync.Mutex
	installed map[string]string
	latest    map[string]string
	failWith  string
	updates   []string
}

func newHostExec() *hostExec {
	return &hostExec{
		installed: map[string]string{"claude": "2.1.312", "deepcode": "0.6.1"},
		latest:    map[string]string{"claude": "2.1.315", "deepcode": "0.6.2"},
	}
}

func (h *hostExec) Run(_ context.Context, c Command) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	j := strings.Join(c.Argv, " ")
	switch {
	case strings.Contains(j, "command -v claude"):
		return "/home/u/.local/share/claude/versions/" + h.installed["claude"] + "\n", nil
	case strings.Contains(j, "command -v deepcode"):
		return "/usr/lib/node_modules/@vegamo/deepcode-cli/dist/cli.js\n", nil
	case j == "claude --version":
		return h.installed["claude"] + " (Claude Code)\n", nil
	case j == "deepcode --version":
		return h.installed["deepcode"] + "\n", nil
	case strings.HasPrefix(j, "npm view @vegamo/deepcode-cli"):
		return h.latest["deepcode"] + "\n", nil
	case j == "claude update":
		return h.apply("claude", j)
	case strings.HasPrefix(j, "npm install -g @vegamo/deepcode-cli"):
		return h.apply("deepcode", j)
	}
	return "", errors.New("exit status 127")
}

func (h *hostExec) apply(agent, j string) (string, error) {
	h.updates = append(h.updates, j)
	if h.failWith != "" {
		return h.failWith, errors.New("exit status 1")
	}
	h.installed[agent] = h.latest[agent]
	return "updated\n", nil
}

func (h *hostExec) ran() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.updates...)
}

func testEntries() []Entry {
	c, _ := Lookup("claude")
	d, _ := Lookup("deepcode")
	return []Entry{c, d}
}

type managerFixture struct {
	m        *Manager
	host     *hostExec
	sessions []LiveSession
	hooks    int
	now      time.Time
	mu       sync.Mutex
}

func newFixture(t *testing.T, mode Mode, hold ...string) *managerFixture {
	t.Helper()
	f := &managerFixture{host: newHostExec(), now: time.Date(2026, 10, 28, 4, 0, 0, 0, time.Local)}
	pol, err := NewPolicy(string(mode), "03:00-06:00", hold)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	f.m = &Manager{
		Prober: &Prober{Exec: f.host, HTTP: &fakeHTTP{bodies: map[string]string{
			"https://downloads.claude.ai/claude-code-releases/stable": "2.1.315\n",
		}}},
		Runner:    NewRunner(f.host, filepath.Join(dir, "logs", "agent-updates.log")),
		Entries:   testEntries(),
		StatePath: filepath.Join(dir, "agent-versions.json"),
		Policy:    pol,
		Sessions: func(context.Context) ([]LiveSession, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return append([]LiveSession(nil), f.sessions...), nil
		},
		AfterUpdate: func(string) {
			f.mu.Lock()
			f.hooks++
			f.mu.Unlock()
		},
		Now:  func() time.Time { return f.now },
		Logf: t.Logf,
	}
	return f
}

func statusOf(t *testing.T, list []AgentStatus, agent string) AgentStatus {
	t.Helper()
	for _, s := range list {
		if s.Agent == agent {
			return s
		}
	}
	t.Fatalf("%s missing from %+v", agent, list)
	return AgentStatus{}
}

func TestManagerStatusProbesWhenNeverChecked(t *testing.T) {
	f := newFixture(t, ModeNotify)
	f.m.Entries = append(f.m.Entries, Entry{Agent: "pi"})
	list, err := f.m.Status(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, list, "claude"); got.Status != StatusAvailable || got.Installed != "2.1.312" || got.Latest != "2.1.315" {
		t.Errorf("claude = %+v", got)
	}
	if got := statusOf(t, list, "pi"); got.Status != StatusNotInstalled {
		t.Errorf("pi = %+v", got)
	}
	st, err := LoadState(f.m.StatePath)
	if err != nil || st.Agents["claude"].Installed != "2.1.312" {
		t.Fatalf("state not saved: %+v %v", st, err)
	}
}

func TestManagerStatusCurrentAndLatestUnknown(t *testing.T) {
	f := newFixture(t, ModeNotify)
	f.host.latest["deepcode"] = "0.6.1"
	f.m.Prober.HTTP = &fakeHTTP{}
	list, err := f.m.Status(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, list, "deepcode"); got.Status != StatusCurrent {
		t.Errorf("deepcode = %+v", got)
	}
	if got := statusOf(t, list, "claude"); got.Status != StatusLatestUnknown {
		t.Errorf("claude = %+v", got)
	}
}

func TestManagerStatusShowsGateReason(t *testing.T) {
	f := newFixture(t, ModeNotify)
	f.sessions = []LiveSession{{Agent: "claude", HookState: "working"}, {Agent: "deepcode", HookState: "idle"}}
	list, err := f.m.Status(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if got := statusOf(t, list, "claude"); got.Status != "deferred: 1 session busy" {
		t.Errorf("claude = %+v", got)
	}
	if got := statusOf(t, list, "deepcode"); got.Status != "deferred: 1 session running" {
		t.Errorf("deepcode = %+v", got)
	}
}

func TestManagerManualUpdateReprobesAndReinstallsHooks(t *testing.T) {
	f := newFixture(t, ModeOff)
	ctx := context.Background()
	out, err := f.m.Request(ctx, "claude", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || !out[0].Accepted {
		t.Fatalf("outcome %+v", out)
	}
	f.m.Wait()
	if got := f.host.ran(); len(got) != 1 || got[0] != "claude update" {
		t.Fatalf("updates %v", got)
	}
	if f.hooks != 1 {
		t.Fatalf("hooks reinstalled %d times", f.hooks)
	}
	list, _ := f.m.Status(ctx, false)
	got := statusOf(t, list, "claude")
	if got.Installed != "2.1.315" || got.Status != StatusCurrent || got.LastUpdate == nil || !got.LastUpdate.OK || got.LastUpdate.From != "2.1.312" {
		t.Fatalf("after update %+v last %+v", got, got.LastUpdate)
	}
}

func TestManagerManualUpdateNeedsRoot(t *testing.T) {
	f := newFixture(t, ModeOff)
	f.host.failWith = "npm ERR! code EACCES\n"
	ctx := context.Background()
	if _, err := f.m.Request(ctx, "deepcode", false); err != nil {
		t.Fatal(err)
	}
	f.m.Wait()
	if f.hooks != 0 {
		t.Fatal("hooks reinstalled after a failed update")
	}
	list, _ := f.m.Status(ctx, false)
	got := statusOf(t, list, "deepcode")
	if got.Status != StatusNeedsRoot || !strings.Contains(got.Reason, "npm install -g @vegamo/deepcode-cli@latest") {
		t.Fatalf("deepcode %+v", got)
	}
	f.host.failWith = "boom\n"
	if _, err := f.m.Request(ctx, "claude", false); err != nil {
		t.Fatal(err)
	}
	f.m.Wait()
	list, _ = f.m.Status(ctx, false)
	if got := statusOf(t, list, "claude"); got.Status != StatusFailed {
		t.Fatalf("claude %+v", got)
	}
}

func TestAgentsUpdateDeferredWhileWorking(t *testing.T) {
	f := newFixture(t, ModeAuto)
	f.sessions = []LiveSession{{Agent: "claude", HookState: "working"}, {Agent: "claude", HookState: "working"}}
	out, err := f.m.Request(context.Background(), "claude", false)
	if err != nil {
		t.Fatal(err)
	}
	f.m.Wait()
	if len(out) != 1 || out[0].Accepted || out[0].Reason != "deferred: 2 sessions busy" {
		t.Fatalf("outcome %+v", out)
	}
	if got := f.host.ran(); len(got) != 0 {
		t.Fatalf("ran %v while sessions were working", got)
	}
}

func TestManagerRequestUnknownAndNotInstalled(t *testing.T) {
	f := newFixture(t, ModeOff)
	ctx := context.Background()
	if _, err := f.m.Request(ctx, "nosuch", false); err == nil {
		t.Fatal("unknown agent accepted")
	}
	f.m.Entries = append(f.m.Entries, Entry{Agent: "pi"})
	out, err := f.m.Request(ctx, "pi", false)
	if err != nil {
		t.Fatal(err)
	}
	if out[0].Accepted || out[0].Reason != StatusNotInstalled {
		t.Fatalf("pi outcome %+v", out)
	}
}

func TestManagerRequestAllTakesOutdatedOnly(t *testing.T) {
	f := newFixture(t, ModeOff)
	f.host.latest["deepcode"] = "0.6.1"
	out, err := f.m.Request(context.Background(), "", true)
	if err != nil {
		t.Fatal(err)
	}
	f.m.Wait()
	if len(out) != 1 || out[0].Agent != "claude" || !out[0].Accepted {
		t.Fatalf("outcome %+v", out)
	}
}

func TestSweepAutoAppliesOnlyInsideWindow(t *testing.T) {
	f := newFixture(t, ModeAuto)
	f.now = time.Date(2026, 10, 28, 12, 0, 0, 0, time.Local)
	f.m.Sweep(context.Background())
	if got := f.host.ran(); len(got) != 0 {
		t.Fatalf("applied outside the window: %v", got)
	}
	f.now = time.Date(2026, 10, 28, 4, 30, 0, 0, time.Local)
	f.m.Sweep(context.Background())
	got := f.host.ran()
	if len(got) != 2 {
		t.Fatalf("inside the window ran %v, want both agents", got)
	}
	if f.hooks != 2 {
		t.Fatalf("hooks %d", f.hooks)
	}
}

func TestSweepSkipsHeldAgents(t *testing.T) {
	f := newFixture(t, ModeAuto, "claude")
	f.m.Sweep(context.Background())
	got := f.host.ran()
	if len(got) != 1 || !strings.Contains(got[0], "deepcode") {
		t.Fatalf("ran %v, want only deepcode", got)
	}
	list, _ := f.m.Status(context.Background(), false)
	if c := statusOf(t, list, "claude"); !c.Held || c.Status != StatusAvailable {
		t.Fatalf("claude %+v", c)
	}
}

func TestSweepNotifyModeNeverApplies(t *testing.T) {
	f := newFixture(t, ModeNotify)
	var seen []string
	f.m.OnNewVersion = func(a AgentState) { seen = append(seen, a.Agent+"@"+a.Latest) }
	f.m.Sweep(context.Background())
	f.m.Sweep(context.Background())
	if got := f.host.ran(); len(got) != 0 {
		t.Fatalf("notify mode ran %v", got)
	}
	if len(seen) != 2 {
		t.Fatalf("new-version hook calls %v, want one per agent", seen)
	}
}

func TestSweepDefersBusyAgentAndRetries(t *testing.T) {
	f := newFixture(t, ModeAuto, "deepcode")
	f.sessions = []LiveSession{{Agent: "claude", HookState: "working"}}
	f.m.Sweep(context.Background())
	if got := f.host.ran(); len(got) != 0 {
		t.Fatalf("ran %v while busy", got)
	}
	f.sessions = nil
	f.m.ApplyDue(context.Background())
	if got := f.host.ran(); len(got) != 1 {
		t.Fatalf("retry ran %v", got)
	}
}

func TestSweepOffModeDoesNothing(t *testing.T) {
	f := newFixture(t, ModeOff)
	f.m.Sweep(context.Background())
	if _, err := LoadState(f.m.StatePath); err != nil {
		t.Fatal(err)
	}
	if got := f.host.ran(); len(got) != 0 {
		t.Fatalf("off mode ran %v", got)
	}
}
