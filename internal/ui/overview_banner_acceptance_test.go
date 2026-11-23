package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/usecase"
)

// Acceptance tests for overview-status (design rev 2, piece P2).

type overviewCall struct {
	method string
	host   string
	params map[string]any
}

// fakeOverviewByHost answers overview.* per host: answer(host, method,
// params) returns a JSON string or an error.
func fakeOverviewByHost(t *testing.T, answer func(host, method string, params map[string]any) any) *[]overviewCall {
	t.Helper()
	var calls []overviewCall
	prev := overviewRPC
	overviewRPC = func(_ context.Context, r config.Remote, method string, params any) (json.RawMessage, error) {
		var p map[string]any
		b, _ := json.Marshal(params)
		_ = json.Unmarshal(b, &p)
		calls = append(calls, overviewCall{method: method, host: r.Host, params: p})
		switch a := answer(r.Host, method, p).(type) {
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

// runAll runs cmd and routes every message it yields, batches included,
// through the remote-service router, following the commands it returns.
// Ticks are not followed.
func runAll(m *Model, cmd tea.Cmd, depth int) {
	if cmd == nil || depth > 4 {
		return
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-done:
	case <-time.After(200 * time.Millisecond):
		return // a tick or a wait: not followed
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			runAll(m, c, depth+1)
		}
		return
	}
	if msg == nil {
		return
	}
	_, next := m.routeRemoteServiceMsg(msg)
	runAll(m, next, depth+1)
}

func stepsJSON(states ...string) string {
	names := []string{"collect", "bundle", "start_agent", "report", "cleanup"}
	labels := []string{"collect sessions", "write bundle", "start agent", "write report", "clean up"}
	var parts []string
	for i, st := range states {
		s := fmt.Sprintf(`{"name":%q,"label":%q,"state":%q`, names[i], labels[i], st)
		if st == "done" {
			s += `,"detail":"detail-` + names[i] + `"`
		}
		if st == "failed" {
			s += `,"error":"boot wait timed out"`
		}
		parts = append(parts, s+"}")
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func latestJSON(state string, started, ended time.Time, needs int, errText string, steps string) string {
	end := ""
	if !ended.IsZero() {
		end = fmt.Sprintf(`,"ended_at":%q`, ended.UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf(`{"type":"overview_run","latest":{"id":"r9","agent":"claude","state":%q,"started_at":%q%s,"needs_you":%d,"error":%q,"sessions":4,"steps":%s}}`,
		state, started.UTC().Format(time.RFC3339), end, needs, errText, steps)
}

func TestOverviewBannerStatesFromTheStartupRead(t *testing.T) {
	now := time.Now()
	ended := time.Date(now.Year(), now.Month(), now.Day(), 10, 36, 0, 0, time.Local)
	answers := map[string]string{
		"10.0.1.5": latestJSON("running", now.Add(-24*time.Second), time.Time{}, 0, "", stepsJSON("done", "done", "running", "pending", "pending")),
		"10.0.1.9": latestJSON("done", ended.Add(-time.Minute), ended, 3, "", stepsJSON("done", "done", "done", "done", "done")),
	}
	calls := fakeOverviewByHost(t, func(host, method string, _ map[string]any) any {
		if method != "overview.get" {
			return nil
		}
		return answers[host]
	})
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.width, m.height = 120, 40
	runAll(m, m.fetchOverviewStatusAll(), 0)

	if len(*calls) != 2 {
		t.Fatalf("startup reads %+v, want one per host", *calls)
	}
	for _, c := range *calls {
		if c.method != "overview.get" || c.params["summary"] != true {
			t.Fatalf("startup read %+v, want overview.get {summary: true}", c)
		}
	}
	running := ansi.Strip(m.overviewBannerFor("10.0.1.5"))
	if !strings.Contains(running, "overview") || !strings.Contains(running, "start agent") {
		t.Fatalf("running banner %q", running)
	}
	done := ansi.Strip(m.overviewBannerFor("10.0.1.9"))
	if !strings.Contains(done, "10:36") || !strings.Contains(done, "3 need you") {
		t.Fatalf("done banner %q", done)
	}
	if got := m.overviewBannerFor("unknown-host"); got != "" {
		t.Fatalf("banner for an unknown host %q", got)
	}
	// The header shows the gauge host's banner on any view.
	m.res.byHost = map[string]server.ResourcesReport{"10.0.1.9": {}}
	m.state = viewStateMain
	if h := ansi.Strip(m.headerGauge()); !strings.Contains(h, "3 need you") {
		t.Fatalf("header %q lacks the banner", h)
	}
}

func TestOverviewBannerShowsAFailureAndNothingWithoutARun(t *testing.T) {
	now := time.Now()
	fakeOverviewByHost(t, func(host, method string, _ map[string]any) any {
		if host == "10.0.1.5" {
			return latestJSON("failed", now.Add(-time.Minute), now, 0, "start agent: boot wait timed out",
				stepsJSON("done", "done", "failed", "pending", "pending"))
		}
		return &usecase.RPCError{Code: server.CodeNotFound, Message: "no overview yet"}
	})
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	runAll(m, m.fetchOverviewStatusAll(), 0)
	if b := ansi.Strip(m.overviewBannerFor("10.0.1.5")); !strings.Contains(b, "failed") {
		t.Fatalf("failed banner %q", b)
	}
	if b := m.overviewBannerFor("10.0.1.9"); b != "" {
		t.Fatalf("banner %q for a host with no run", b)
	}
}

func TestOverviewPageShowsStepsAndTheError(t *testing.T) {
	now := time.Now()
	failed := fmt.Sprintf(`{"type":"overview_run","run":{"id":"r9","agent":"claude","state":"failed","started_at":%q,"error":"start agent: boot wait timed out","sessions":4,"steps":%s}}`,
		now.Add(-time.Minute).UTC().Format(time.RFC3339), stepsJSON("done", "done", "failed", "pending", "pending"))
	fakeOverviewByHost(t, func(_, method string, p map[string]any) any {
		if method != "overview.get" {
			return nil
		}
		if p["summary"] == true {
			return latestJSON("failed", now.Add(-time.Minute), now, 0, "start agent: boot wait timed out",
				stepsJSON("done", "done", "failed", "pending", "pending"))
		}
		return failed
	})
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.width, m.height = 120, 40
	runAll(m, m.enterOverview(), 0)
	out := ansi.Strip(m.renderOverviewView())
	for _, want := range []string{"✓", "collect sessions", "detail-collect", "✗", "start agent", "boot wait timed out", "·", "write report"} {
		if !strings.Contains(out, want) {
			t.Errorf("page lacks %q:\n%s", want, out)
		}
	}
	if !strings.Contains(strings.ToLower(out), "press r to run again") {
		t.Errorf("page lacks the run again hint:\n%s", out)
	}
}

func TestOverviewPageShowsTheRunningStep(t *testing.T) {
	now := time.Now()
	running := fmt.Sprintf(`{"type":"overview_run","run":{"id":"r9","agent":"claude","state":"running","started_at":%q,"session":"s1","sessions":4,"steps":%s}}`,
		now.Add(-40*time.Second).UTC().Format(time.RFC3339), stepsJSON("done", "done", "done", "running", "pending"))
	fakeOverviewByHost(t, func(_, method string, p map[string]any) any {
		if method != "overview.get" {
			return nil
		}
		if p["summary"] == true {
			return latestJSON("running", now.Add(-40*time.Second), time.Time{}, 0, "", stepsJSON("done", "done", "done", "running", "pending"))
		}
		return running
	})
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.width, m.height = 120, 40
	runAll(m, m.enterOverview(), 0)
	out := ansi.Strip(m.renderOverviewView())
	if !strings.Contains(out, "▸") || !strings.Contains(out, "write report") {
		t.Fatalf("page lacks the running step:\n%s", out)
	}
}

func TestOverviewEventWithNoFreeSlotIsNotDropped(t *testing.T) {
	calls := fakeOverviewByHost(t, func(_, method string, _ map[string]any) any {
		if method != "overview.get" {
			return nil
		}
		return latestJSON("running", time.Now(), time.Time{}, 0, "", stepsJSON("running", "pending", "pending", "pending", "pending"))
	})
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	host := "10.0.1.5"
	var releases []func(bool)
	for {
		release, ok := m.budgetTry(host)
		if !ok {
			break
		}
		releases = append(releases, release)
		if len(releases) > 64 {
			t.Fatal("the host budget never filled")
		}
	}
	if cmd := m.overviewEventFetch(host); cmd != nil {
		runAll(m, cmd, 0)
	}
	if len(*calls) != 0 {
		t.Fatalf("read with no free slot: %+v", *calls)
	}
	for _, r := range releases {
		r(false)
	}
	runAll(m, m.drainPending(), 0)
	if len(*calls) != 1 || (*calls)[0].method != "overview.get" || (*calls)[0].host != host {
		t.Fatalf("drain calls %+v, want one overview.get for %s", *calls, host)
	}
	if b := ansi.Strip(m.overviewBannerFor(host)); !strings.Contains(b, "collect sessions") {
		t.Fatalf("banner %q after the drained read", b)
	}
}
