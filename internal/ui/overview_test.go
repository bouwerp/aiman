package ui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/usecase"
)

// fakeOverviewRPC answers each method from answers, or with err when the
// answer is an error, and records the methods called.
func fakeOverviewRPC(t *testing.T, answers map[string]any) *[]string {
	t.Helper()
	var calls []string
	prev := overviewRPC
	overviewRPC = func(_ context.Context, _ config.Remote, method string, _ any) (json.RawMessage, error) {
		calls = append(calls, method)
		switch a := answers[method].(type) {
		case error:
			return nil, a
		case string:
			return json.RawMessage(a), nil
		}
		return nil, &usecase.RPCError{Code: server.CodeInvalidParams, Message: "unexpected " + method}
	}
	t.Cleanup(func() { overviewRPC = prev })
	return &calls
}

const doneOverview = `{"type":"overview_run","run":{"id":"r1","agent":"codex","state":"done","started_at":"2026-11-20T07:00:00Z","sessions":3,
 "markdown":"## In progress\n- alpha: adding the parser\n\n## Needs you\n- approve alpha's design\n- answer beta\n\n## Next\n1. alpha: open the PR\n\n## Risks\n- none\n",
 "report":{"in_progress":["alpha: adding the parser"],"needs_you":["approve alpha's design","answer beta"],"next":["alpha: open the PR"],"risks":[]}}}`

func overviewPage(t *testing.T, answers map[string]any) (*Model, *[]string) {
	t.Helper()
	calls := fakeOverviewRPC(t, answers)
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.width, m.height = 100, 40
	cmd := m.enterOverview()
	if m.state != viewStateOverview || cmd == nil {
		t.Fatalf("O: state %v cmd %v", m.state, cmd)
	}
	m.routeRemoteServiceMsg(cmd())
	return m, calls
}

func TestOverviewPageShowsTheNewestReportAndSetsTheHeaderCount(t *testing.T) {
	m, calls := overviewPage(t, map[string]any{"overview.get": doneOverview})
	if len(*calls) != 1 || (*calls)[0] != "overview.get" {
		t.Fatalf("calls %v", *calls)
	}
	out := ansi.Strip(m.renderOverviewView())
	for _, want := range []string{"Overview of", "done", "codex", "3 sessions", "In progress", "alpha: adding the parser", "approve alpha's design", "r run"} {
		if !strings.Contains(out, want) {
			t.Errorf("page lacks %q:\n%s", want, out)
		}
	}
	host := m.overview.remote.Host
	if m.overviewNeeds[host] != 2 {
		t.Fatalf("needs %v", m.overviewNeeds)
	}
}

func TestOverviewPageWithNoRunOffersOne(t *testing.T) {
	m, calls := overviewPage(t, map[string]any{
		"overview.get": &usecase.RPCError{Code: server.CodeNotFound, Message: "no overview yet"},
		"overview.run": `{"type":"overview_run","run":{"id":"r2","agent":"claude","state":"running","started_at":"2026-11-20T07:00:00Z","sessions":2}}`,
	})
	if out := ansi.Strip(m.renderOverviewView()); !strings.Contains(out, "No overview yet. Press r to run one.") {
		t.Fatalf("page:\n%s", out)
	}
	_, cmd := m.handleOverviewUpdate(pressRune('r'))
	if cmd == nil {
		t.Fatal("r started nothing")
	}
	_, poll := m.routeRemoteServiceMsg(cmd())
	if (*calls)[len(*calls)-1] != "overview.run" || m.overview.run == nil || m.overview.run.State != server.OverviewRunning {
		t.Fatalf("calls %v run %+v", *calls, m.overview.run)
	}
	if poll == nil || !m.overview.pollQueued {
		t.Fatal("a running run is not polled")
	}
	if out := ansi.Strip(m.renderOverviewView()); !strings.Contains(out, "Running for") {
		t.Fatalf("page:\n%s", out)
	}
	// A second r while it runs starts nothing.
	if _, again := m.handleOverviewUpdate(pressRune('r')); again != nil {
		t.Fatal("r started a second run")
	}
}

func TestOverviewEventRereadsTheHost(t *testing.T) {
	m, calls := overviewPage(t, map[string]any{"overview.get": doneOverview})
	host := m.overview.remote.Host
	m.state = viewStateMain
	cmd := m.overviewEventFetch(host)
	if cmd == nil {
		t.Fatal("no fetch for a known host")
	}
	m.routeRemoteServiceMsg(cmd())
	if len(*calls) != 2 || m.overviewNeeds[host] != 2 {
		t.Fatalf("calls %v needs %v", *calls, m.overviewNeeds)
	}
	if m.overviewEventFetch("unknown-host") != nil {
		t.Fatal("fetched for a host with no remote")
	}
}
