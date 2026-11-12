package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/bouwerp/aiman/internal/cgroup"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/usecase"
)

func resourcesModel(t *testing.T) (*Model, domain.Session) {
	t.Helper()
	sess := domain.Session{ID: "s1", Name: "fix", RemoteHost: "devbox", TerminalName: "fix", Status: domain.SessionStatusActive}
	return newTestModelWithSession(t, sess), sess
}

func supportedReport() server.ResourcesReport {
	r := server.ResourcesReport{}
	r.Supported = true
	r.UserSlice = &cgroup.Usage{MemoryCurrent: 18 << 30, MemoryHigh: 24 << 30}
	r.Sessions = []cgroup.SessionUsage{{ID: "s1", Usage: cgroup.Usage{
		MemoryCurrent: 26 << 28, MemoryHigh: 6 << 30, CPUPercent: 150,
	}}}
	return r
}

func TestSessionResourceText(t *testing.T) {
	cases := map[string]cgroup.Usage{
		"5.2G/6G 150%": {MemoryCurrent: 5583457485, MemoryHigh: 6 << 30, CPUPercent: 150.2},
		"512M 0%":      {MemoryCurrent: 512 << 20, MemoryHigh: cgroup.Infinity},
		"1G 3%":        {MemoryCurrent: 1 << 30, CPUPercent: 3.4},
	}
	for want, u := range cases {
		if got := sessionResourceText(u); got != want {
			t.Errorf("sessionResourceText(%+v) = %q, want %q", u, got, want)
		}
	}
}

func TestMemoryNearLimit(t *testing.T) {
	if !memoryNearLimit(90, 100) || memoryNearLimit(89, 100) || memoryNearLimit(100, cgroup.Infinity) || memoryNearLimit(100, 0) {
		t.Fatal("memoryNearLimit thresholds are wrong")
	}
}

func TestRenderHeaderGauge(t *testing.T) {
	if got := ansi.Strip(renderHeaderGauge(&cgroup.Usage{MemoryCurrent: 18 << 30, MemoryHigh: 24 << 30})); got != "mem 18G/24G" {
		t.Fatalf("gauge = %q", got)
	}
	for _, u := range []*cgroup.Usage{nil, {MemoryCurrent: 1 << 30, MemoryHigh: cgroup.Infinity}, {MemoryCurrent: 1 << 30}} {
		if got := renderHeaderGauge(u); got != "" {
			t.Errorf("gauge for %+v = %q, want none", u, got)
		}
	}
}

func TestRenderA1HeaderGaugeFitsOrDrops(t *testing.T) {
	gauge := renderHeaderGauge(&cgroup.Usage{MemoryCurrent: 18 << 30, MemoryHigh: 24 << 30})
	wide := ansi.Strip(renderA1HeaderGauge("devbox", gauge, 3, 1, false, 80))
	if !strings.Contains(wide, "aiman · devbox · mem 18G/24G") || !strings.HasSuffix(wide, "1 needs you") {
		t.Fatalf("wide header = %q", wide)
	}
	narrow := renderA1HeaderGauge("devbox", gauge, 3, 1, false, 36)
	if narrow != renderA1Header("devbox", 3, 1, false, 36) {
		t.Fatalf("narrow header kept the gauge: %q", ansi.Strip(narrow))
	}
	if got := renderA1HeaderGauge("devbox", "", 3, 1, false, 80); got != renderA1Header("devbox", 3, 1, false, 80) {
		t.Fatal("no gauge must render the plain header")
	}
}

func TestResourcesMsgStoresSupportedAndDropsTheRest(t *testing.T) {
	m, sess := resourcesModel(t)
	m.res.inflight = map[string]bool{"devbox": true}
	m.applyResourcesMsg(resourcesMsg{host: "devbox", report: supportedReport()})
	if m.res.inflight["devbox"] {
		t.Fatal("answer left the host in flight")
	}
	u, ok := m.sessionResources(sess)
	if !ok || u.CPUPercent != 150 {
		t.Fatalf("session usage: %+v, %v", u, ok)
	}
	meta := ansi.Strip(m.withSessionResources("claude", sess, 80))
	if meta != "claude · 6.5G/6G 150%" {
		t.Fatalf("meta = %q", meta)
	}
	if got := m.withSessionResources("claude", sess, 12); got != "claude" {
		t.Fatalf("narrow meta = %q", got)
	}
	if got := ansi.Strip(m.headerGauge()); got != "mem 18G/24G" {
		t.Fatalf("header gauge = %q", got)
	}
	if _, ok := m.sessionResources(domain.Session{ID: "other", RemoteHost: "devbox"}); ok {
		t.Fatal("a session with no entry got a readout")
	}

	// An old serve, any error and an unsupported host all show nothing.
	for _, msg := range []resourcesMsg{
		{host: "devbox", err: usecase.ErrRPCUnsupported},
		{host: "devbox", err: errors.New("ssh: timeout")},
		{host: "devbox", report: server.ResourcesReport{}},
	} {
		m.applyResourcesMsg(resourcesMsg{host: "devbox", report: supportedReport()})
		m.applyResourcesMsg(msg)
		if m.headerGauge() != "" || m.withSessionResources("claude", sess, 80) != "claude" {
			t.Fatalf("%+v still draws resources", msg)
		}
	}
}

func TestResourcesFetchOncePerHostAndTickOnlyOnSessionsTab(t *testing.T) {
	m, _ := resourcesModel(t)
	if hosts := m.resourceHosts(); len(hosts) != 1 || hosts[0] != "devbox" {
		t.Fatalf("hosts = %v", hosts)
	}
	if m.fetchResourcesAll() == nil || !m.res.inflight["devbox"] {
		t.Fatal("first fetch did not start")
	}
	if cmd := m.fetchResourcesAll(); cmd != nil {
		t.Fatal("a second fetch started while the first is in flight")
	}

	if m.resourcesAfterDiscovery() == nil || !m.res.ticking {
		t.Fatal("discovery did not start the tick")
	}
	// Later discoveries add no second tick chain; with the fetch in flight
	// there is nothing to do.
	if cmd := m.resourcesAfterDiscovery(); cmd != nil {
		t.Fatal("a second discovery started another tick")
	}

	// Off the sessions tab a tick only re-arms.
	delete(m.res.inflight, "devbox")
	m.state, m.currentTab = viewStateMain, tabDaemons
	if m.applyResourcesMsg(resourcesTickMsg{}) == nil || m.res.inflight["devbox"] {
		t.Fatal("tick off the sessions tab fetched or stopped the chain")
	}
	m.currentTab = tabSessions
	if m.applyResourcesMsg(resourcesTickMsg{}) == nil || !m.res.inflight["devbox"] {
		t.Fatal("tick on the sessions tab did not fetch")
	}
}

func TestUpdateRoutesResourcesMsgOffTheList(t *testing.T) {
	m, _ := resourcesModel(t)
	m.state = viewStateMenu
	if _, _ = m.Update(resourcesMsg{host: "devbox", report: supportedReport()}); len(m.res.byHost) != 1 {
		t.Fatal("an answer that arrived off the session list was dropped")
	}
}
