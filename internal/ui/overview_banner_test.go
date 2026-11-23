package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/bouwerp/aiman/internal/server"
)

func doneRun(needs int) *server.OverviewRun {
	end := time.Now()
	return &server.OverviewRun{ID: "r1", State: server.OverviewDone, StartedAt: end.Add(-time.Minute), EndedAt: &end, NeedsYou: &needs}
}

// The banner shows with no session selected and no resources answer.
func TestHeaderShowsOverviewWithoutResourcesOrSelection(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.overviewStatus = map[string]*overviewHostStatus{
		"10.0.1.9": {latest: doneRun(3), read: true},
		"10.0.1.5": {read: true},
	}
	h := ansi.Strip(m.renderHeader("all remotes", 30, 15, false, 120))
	if !strings.Contains(h, "3 need you") {
		t.Fatalf("header %q lacks the banner", h)
	}
}

// A narrow header drops the memory gauge before the overview banner.
func TestNarrowHeaderKeepsOverviewBanner(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.overviewStatus = map[string]*overviewHostStatus{"10.0.1.9": {latest: doneRun(3), read: true}}
	m.res.byHost = map[string]server.ResourcesReport{"10.0.1.9": supportedReport()}
	mem, overview := m.headerGaugeParts()
	if mem == "" || overview == "" {
		t.Fatalf("parts mem=%q overview=%q, want both", mem, overview)
	}
	both := joinGauge(mem, overview)
	w := 200
	for renderA1HeaderGauge("devbox", both, 30, 15, false, w-1) != renderA1Header("devbox", 30, 15, false, w-1) {
		w--
	}
	// w is the narrowest header that holds both parts.
	h := ansi.Strip(m.renderHeader("devbox", 30, 15, false, w-1))
	if !strings.Contains(h, "3 need you") || strings.Contains(h, "mem ") {
		t.Fatalf("narrow header %q, want the banner without the memory gauge", h)
	}
}
