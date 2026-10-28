package ui

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/agentupdate"
	"github.com/bouwerp/aiman/internal/infra/config"
)

type agentsCall struct {
	host   string
	method string
	params map[string]any
}

// fakeAgentsRPC replaces agentsRPC for one test and records each call.
func fakeAgentsRPC(t *testing.T, answer string) *[]agentsCall {
	t.Helper()
	var calls []agentsCall
	prev := agentsRPC
	agentsRPC = func(_ context.Context, r config.Remote, method string, params any) (json.RawMessage, error) {
		raw, _ := json.Marshal(params)
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		calls = append(calls, agentsCall{host: r.Host, method: method, params: p})
		return json.RawMessage(answer), nil
	}
	t.Cleanup(func() { agentsRPC = prev })
	return &calls
}

const agentsStatusAnswer = `{"type":"agents","host":"regent","mode":"auto","window":"03:00-06:00","agents":[
 {"agent":"claude","installed":"2.1.312","latest":"2.1.315","method":"native","status":"update available"},
 {"agent":"codex","installed":"0.164.0","latest":"0.164.0","method":"standalone","status":"current"}]}`

func agentsPage(t *testing.T) (*Model, *[]agentsCall) {
	t.Helper()
	calls := fakeAgentsRPC(t, agentsStatusAnswer)
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.state = viewStateAgentAPI
	_, cmd := m.handleAgentAPIUpdate(pressKey("a"))
	if m.state != viewStateAgentUpdates || cmd == nil {
		t.Fatalf("a: state %v cmd %v", m.state, cmd)
	}
	m.routeRemoteServiceMsg(cmd())
	return m, calls
}

func TestAgentAPIKeyAOpensAgentsPage(t *testing.T) {
	m, calls := agentsPage(t)
	if len(*calls) != 1 || (*calls)[0].method != "agents.status" || (*calls)[0].host != "10.0.1.5" {
		t.Fatalf("calls %+v", *calls)
	}
	out := m.renderAgentUpdatesView()
	for _, want := range []string{"Agents on", "Agent", "Installed", "Latest", "Method", "Status", "claude", "2.1.315", "native", "window 03:00-06:00"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	m.handleAgentUpdatesUpdate(pressKey("esc"))
	if m.state != viewStateAgentAPI {
		t.Fatalf("esc: state %v", m.state)
	}
}

func TestAgentUpdatesRendersEachStatus(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.agentUpdates = &agentUpdatesView{remote: config.Remote{Host: "h"}}
	statuses := []string{
		agentupdate.StatusCurrent, agentupdate.StatusAvailable, agentupdate.StatusLatestUnknown,
		agentupdate.StatusNeedsRoot, agentupdate.StatusUpdating, agentupdate.StatusFailed,
		agentupdate.StatusNotInstalled, "deferred: 2 sessions busy",
	}
	for i, s := range statuses {
		m.agentUpdates.rows = append(m.agentUpdates.rows, agentupdate.AgentStatus{
			AgentState: agentupdate.AgentState{Agent: "a" + string(rune('0'+i))}, Status: s, Held: i == 1,
		})
	}
	out := m.renderAgentUpdatesView()
	for _, s := range statuses {
		if !strings.Contains(out, s) {
			t.Fatalf("missing status %q in:\n%s", s, out)
		}
	}
	if !strings.Contains(out, "update available (held)") {
		t.Fatalf("held marker missing:\n%s", out)
	}
}

func TestAgentUpdatesKeysDispatchRPC(t *testing.T) {
	m, calls := agentsPage(t)
	_, cmd := m.handleAgentUpdatesUpdate(pressKey("u"))
	if cmd == nil {
		t.Fatal("u: no command")
	}
	cmd()
	_, cmd = m.handleAgentUpdatesUpdate(pressKey("U"))
	if cmd == nil {
		t.Fatal("U: no command")
	}
	cmd()
	_, cmd = m.handleAgentUpdatesUpdate(pressKey("r"))
	cmd()
	got := (*calls)[1:]
	want := []agentsCall{
		{host: "10.0.1.5", method: "agents.update", params: map[string]any{"agent": "claude"}},
		{host: "10.0.1.5", method: "agents.update", params: map[string]any{"all": true}},
		{host: "10.0.1.5", method: "agents.status", params: map[string]any{"refresh": true}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("calls %+v\nwant %+v", got, want)
	}
}

func TestAgentUpdatesUpdateAllSkipsWhenCurrent(t *testing.T) {
	m, calls := agentsPage(t)
	m.agentUpdates.rows = m.agentUpdates.rows[1:]
	if _, cmd := m.handleAgentUpdatesUpdate(pressKey("U")); cmd != nil {
		t.Fatal("U with nothing outdated sent an RPC")
	}
	if len(*calls) != 1 {
		t.Fatalf("calls %+v", *calls)
	}
}

func TestAgentUpdatesPollsOnlyWhileUpdating(t *testing.T) {
	m, _ := agentsPage(t)
	if m.agentUpdates.pollQueued {
		t.Fatal("polling with nothing updating")
	}
	updating := []agentupdate.AgentStatus{{AgentState: agentupdate.AgentState{Agent: "claude"}, Status: agentupdate.StatusUpdating}}
	_, cmd := m.routeRemoteServiceMsg(agentsStatusMsg{host: "10.0.1.5", rows: updating})
	if cmd == nil || !m.agentUpdates.pollQueued {
		t.Fatal("no poll while a row is updating")
	}
	if _, cmd := m.routeRemoteServiceMsg(agentsStatusMsg{host: "10.0.1.5", rows: updating}); cmd != nil {
		t.Fatal("second tick chain started")
	}
	if _, cmd := m.routeRemoteServiceMsg(agentsPollMsg{host: "10.0.1.5"}); cmd == nil {
		t.Fatal("poll tick did not re-read status")
	}
	done := []agentupdate.AgentStatus{{AgentState: agentupdate.AgentState{Agent: "claude"}, Status: agentupdate.StatusCurrent}}
	if _, cmd := m.routeRemoteServiceMsg(agentsStatusMsg{host: "10.0.1.5", rows: done}); cmd != nil {
		t.Fatal("polling after the update finished")
	}
	m.routeRemoteServiceMsg(agentsStatusMsg{host: "10.0.1.5", rows: updating})
	m.state = viewStateAgentAPI
	if _, cmd := m.routeRemoteServiceMsg(agentsPollMsg{host: "10.0.1.5"}); cmd != nil {
		t.Fatal("polling after leaving the page")
	}
}

func TestAgentUpdatesMoveDeepcodeToUserPrefix(t *testing.T) {
	m, calls := agentsPage(t)
	deepcode := agentupdate.AgentStatus{
		AgentState: agentupdate.AgentState{Agent: "deepcode", Installed: "0.6.1", Latest: "0.6.2", Method: agentupdate.MethodNPM,
			Path: "/usr/lib/node_modules/@vegamo/deepcode-cli/dist/cli.js"},
		Status: agentupdate.StatusAvailable, RootOwned: true,
	}
	m.agentUpdates.rows = append(m.agentUpdates.rows, deepcode)

	// claude is native: m only explains itself.
	if _, cmd := m.handleAgentUpdatesUpdate(pressKey("m")); cmd != nil || m.agentUpdates.confirmMove != "" {
		t.Fatal("m offered a move for a native install")
	}
	m.handleAgentUpdatesUpdate(pressKey("down"))
	m.handleAgentUpdatesUpdate(pressKey("down"))
	if !strings.Contains(m.renderAgentUpdatesView(), "m move to user prefix") {
		t.Fatal("footer does not offer m for a root-owned npm install")
	}
	m.handleAgentUpdatesUpdate(pressKey("m"))
	if m.agentUpdates.confirmMove != "deepcode" {
		t.Fatalf("confirm %q", m.agentUpdates.confirmMove)
	}
	out := m.renderAgentUpdatesView()
	for _, want := range []string{
		"npm config set prefix ~/.npm-global",
		"npm install -g @vegamo/deepcode-cli@latest",
		"~/.npm-global/bin comes before the system paths",
		"y run  n cancel",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("confirm screen missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sudo ") {
		t.Fatalf("confirm screen mentions sudo as a command:\n%s", out)
	}

	m.handleAgentUpdatesUpdate(pressKey("n"))
	if m.agentUpdates.confirmMove != "" || len(*calls) != 1 {
		t.Fatalf("n: confirm %q calls %+v", m.agentUpdates.confirmMove, *calls)
	}
	m.handleAgentUpdatesUpdate(pressKey("m"))
	_, cmd := m.handleAgentUpdatesUpdate(pressKey("y"))
	if cmd == nil {
		t.Fatal("y: no command")
	}
	cmd()
	last := (*calls)[len(*calls)-1]
	if last.method != "agents.move_prefix" || !reflect.DeepEqual(last.params, map[string]any{"agent": "deepcode"}) {
		t.Fatalf("call %+v", last)
	}
	_, cmd = m.routeRemoteServiceMsg(agentsMoveMsg{host: "10.0.1.5", result: agentupdate.Outcome{Agent: "deepcode", Accepted: true}})
	if cmd == nil || !strings.Contains(m.agentUpdates.notice, "moving deepcode") {
		t.Fatalf("move answer: notice %q", m.agentUpdates.notice)
	}
}

func TestAgentsUpdateNotice(t *testing.T) {
	got := agentsUpdateNotice([]agentupdate.Outcome{{Agent: "claude", Accepted: true}, {Agent: "codex", Reason: "deferred: 1 session busy"}})
	if got != "updating claude · codex: deferred: 1 session busy" {
		t.Fatalf("notice %q", got)
	}
}
