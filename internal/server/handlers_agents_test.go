package server

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/agentupdate"
	"github.com/bouwerp/aiman/internal/domain"
)

// agentHost fakes a host with claude installed natively at 2.1.312 while
// 2.1.315 is published. `claude update` bumps it.
type agentHost struct {
	mu        sync.Mutex
	installed string
	updates   int
}

func (h *agentHost) Run(_ context.Context, c agentupdate.Command) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	j := strings.Join(c.Argv, " ")
	switch {
	case strings.Contains(j, "command -v claude"):
		return "/home/u/.local/share/claude/versions/" + h.installed + "\n", nil
	case j == "claude --version":
		return h.installed + " (Claude Code)\n", nil
	case j == "claude update":
		h.updates++
		h.installed = "2.1.315"
		return "ok\n", nil
	}
	return "", errors.New("exit status 127")
}

func (h *agentHost) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.updates
}

type agentHTTP struct{}

func (agentHTTP) Get(context.Context, string) ([]byte, error) { return []byte("2.1.315\n"), nil }

func agentsServer(t *testing.T) (*Server, *agentupdate.Manager, *agentHost, domain.SessionRepository) {
	t.Helper()
	repo := testRepo(t)
	srv := New(nil, repo, nil, nil, nil, nil, "test")
	host := &agentHost{installed: "2.1.312"}
	claude, _ := agentupdate.Lookup("claude")
	pol, err := agentupdate.NewPolicy("notify", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	m := &agentupdate.Manager{
		Prober:    &agentupdate.Prober{Exec: host, HTTP: agentHTTP{}},
		Runner:    agentupdate.NewRunner(host, filepath.Join(dir, "agent-updates.log")),
		Entries:   []agentupdate.Entry{claude},
		StatePath: filepath.Join(dir, "agent-versions.json"),
		Policy:    pol,
		Sessions:  srv.LiveAgentSessions,
		Logf:      t.Logf,
	}
	srv.SetAgentUpdates(m, "regent")
	return srv, m, host, repo
}

type agentsStatusResult struct {
	Type   string                    `json:"type"`
	Host   string                    `json:"host"`
	Mode   string                    `json:"mode"`
	Window string                    `json:"window"`
	Agents []agentupdate.AgentStatus `json:"agents"`
}

type agentsUpdateResult struct {
	Type    string                `json:"type"`
	Host    string                `json:"host"`
	Results []agentupdate.Outcome `json:"results"`
}

func TestAgentsRPCDisabled(t *testing.T) {
	srv := New(nil, testRepo(t), nil, nil, nil, nil, "test")
	for _, method := range []string{"agents.status", "agents.update"} {
		resp := rpc(t, srv, "", method, map[string]any{"all": true})
		if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
			t.Fatalf("%s: %+v", method, resp)
		}
	}
}

func TestAgentsStatusAndUpdateRoundTrip(t *testing.T) {
	srv, m, host, _ := agentsServer(t)
	var st agentsStatusResult
	rpcOK(t, srv, "", "agents.status", map[string]any{"refresh": true}, &st)
	if st.Type != "agents" || st.Host != "regent" || st.Mode != "notify" || st.Window != "03:00-06:00" || len(st.Agents) != 1 {
		t.Fatalf("status %+v", st)
	}
	a := st.Agents[0]
	if a.Agent != "claude" || a.Installed != "2.1.312" || a.Latest != "2.1.315" || a.Method != agentupdate.MethodNative || a.Status != agentupdate.StatusAvailable {
		t.Fatalf("claude %+v", a)
	}

	var up agentsUpdateResult
	rpcOK(t, srv, "", "agents.update", map[string]any{"agent": "claude"}, &up)
	if up.Type != "agents_update" || len(up.Results) != 1 || !up.Results[0].Accepted || up.Results[0].Agent != "claude" {
		t.Fatalf("update %+v", up)
	}
	m.Wait()
	if host.count() != 1 {
		t.Fatalf("updates %d", host.count())
	}
	rpcOK(t, srv, "", "agents.status", map[string]any{}, &st)
	if a := st.Agents[0]; a.Installed != "2.1.315" || a.Status != agentupdate.StatusCurrent {
		t.Fatalf("after update %+v", a)
	}
}

func TestAgentsUpdateParams(t *testing.T) {
	srv, _, _, _ := agentsServer(t)
	for _, p := range []map[string]any{{}, {"agent": "claude", "all": true}, {"agent": "nosuch"}} {
		b, _ := json.Marshal(p)
		if resp := rpc(t, srv, "", "agents.update", p); resp.Error == nil || resp.Error.Code != CodeInvalidParams {
			t.Fatalf("%s: %+v", b, resp)
		}
	}
}

func TestAgentsUpdateDeferredWhileWorking(t *testing.T) {
	srv, m, host, repo := agentsServer(t)
	ctx := context.Background()
	for _, s := range []domain.Session{
		{ID: "w1", TmuxSession: "w1", AgentName: "claude --model opus", Status: domain.SessionStatusActive,
			HookState: domain.AgentStateWorking, HookStateSource: agenthook.SourceLifecycle, HookStateAt: time.Now()},
		{ID: "i1", TmuxSession: "i1", AgentName: "claude", Status: domain.SessionStatusActive,
			HookState: domain.AgentStateIdle, HookStateSource: agenthook.SourceLifecycle, HookStateAt: time.Now()},
		{ID: "gone", TmuxSession: "gone", AgentName: "claude", Status: domain.SessionStatusInactive,
			HookState: domain.AgentStateWorking, HookStateSource: agenthook.SourceLifecycle, HookStateAt: time.Now()},
	} {
		if err := repo.Save(ctx, &s); err != nil {
			t.Fatal(err)
		}
	}
	var up agentsUpdateResult
	rpcOK(t, srv, "", "agents.update", map[string]any{"agent": "claude"}, &up)
	m.Wait()
	if len(up.Results) != 1 || up.Results[0].Accepted || up.Results[0].Reason != "deferred: 1 session busy" {
		t.Fatalf("update %+v", up)
	}
	if host.count() != 0 {
		t.Fatal("updated while a session was working")
	}
	var st agentsStatusResult
	rpcOK(t, srv, "", "agents.status", map[string]any{}, &st)
	if st.Agents[0].Status != "deferred: 1 session busy" {
		t.Fatalf("status %+v", st.Agents[0])
	}
}

// npmHost fakes deepcode in the system npm prefix; the move sets the user
// prefix and reinstalls there.
type npmHost struct {
	mu  sync.Mutex
	ran []string
}

func (h *npmHost) Run(_ context.Context, c agentupdate.Command) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	j := strings.Join(c.Argv, " ")
	switch {
	case strings.Contains(j, "command -v deepcode"):
		return "/usr/lib/node_modules/@vegamo/deepcode-cli/dist/cli.js\n", nil
	case j == "deepcode --version", strings.HasPrefix(j, "npm view"):
		return "0.6.1\n", nil
	case strings.HasPrefix(j, "npm "):
		h.ran = append(h.ran, j)
		return "", nil
	}
	return "", errors.New("exit status 127")
}

func TestAgentsMovePrefix(t *testing.T) {
	srv, m, _, _ := agentsServer(t)
	host := &npmHost{}
	deepcode, _ := agentupdate.Lookup("deepcode")
	m.Entries = []agentupdate.Entry{deepcode}
	m.Prober = &agentupdate.Prober{Exec: host, HTTP: agentHTTP{}}
	m.Runner = agentupdate.NewRunner(host, "")
	m.Home = "/home/u"
	m.RootOwned = func(string) bool { return true }

	var st agentsStatusResult
	rpcOK(t, srv, "", "agents.status", map[string]any{"refresh": true}, &st)
	if !st.Agents[0].RootOwned {
		t.Fatalf("status %+v", st.Agents[0])
	}
	var res struct {
		Type   string              `json:"type"`
		Host   string              `json:"host"`
		Result agentupdate.Outcome `json:"result"`
	}
	rpcOK(t, srv, "", "agents.move_prefix", map[string]any{"agent": "deepcode"}, &res)
	m.Wait()
	if res.Type != "agents_move_prefix" || res.Host != "regent" || !res.Result.Accepted {
		t.Fatalf("move %+v", res)
	}
	if got := strings.Join(host.ran, "\n"); got != "npm config set prefix /home/u/.npm-global\nnpm install -g @vegamo/deepcode-cli@latest" {
		t.Fatalf("ran %q", got)
	}
	if resp := rpc(t, srv, "", "agents.move_prefix", map[string]any{}); resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("empty agent %+v", resp)
	}
}
