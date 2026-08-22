package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestGroupedSessionItemsHeadersAndRollup(t *testing.T) {
	flat := []item{
		{session: domain.Session{ID: "2", Name: "reviewer", Group: "WTB-1925"}, needsInput: true, remoteName: "regent0"},
		{session: domain.Session{ID: "1", Name: "impl", Group: "WTB-1925"}, activity: "busy", remoteName: "regent0"},
		{session: domain.Session{ID: "3", Name: "q1", Group: "quick"}, activity: "idle", remoteName: "regent0"},
	}
	got := groupedSessionItems(flat)
	if len(got) != 5 {
		t.Fatalf("len=%d want 5 (2 headers + 3 sessions)", len(got))
	}
	h1, ok := got[0].(item)
	if !ok || !h1.header || h1.session.Group != "WTB-1925" {
		t.Fatalf("first header: %+v", got[0])
	}
	if h1.activity != "waiting" {
		t.Fatalf("rollup %q want waiting", h1.activity)
	}
	a, _ := got[1].(item)
	b, _ := got[2].(item)
	if a.session.Name != "impl" || b.session.Name != "reviewer" {
		t.Fatalf("order %q %q", a.session.Name, b.session.Name)
	}
}

func TestRenderSessionPanelBlankWhenHeaderSelected(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{
		ID: "s1", Name: "impl", Group: "WTB-1", TmuxSession: "impl",
		RemoteHost: "10.0.1.5", RepoName: "org/repo", WorktreePath: "/home/u/repo",
		Status: domain.SessionStatusActive,
	}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	m.SetSize(140, 44)
	m.applyRemoteFilter()
	m.list.Select(0)
	if it, ok := m.list.SelectedItem().(item); !ok || !it.header {
		t.Fatal("expected the group header at index 0")
	}
	m.viewport.SetContent("stale pane from previous session")
	m.gitStatus = domain.GitStatus{Branch: "feature/x"}

	if got := strings.TrimSpace(m.renderSessionPanel(90)); got != "" {
		t.Fatalf("header must render an empty preview, got:\n%s", got)
	}
	out := m.renderMainView()
	for _, leak := range []string{"stale pane", "feature/x", "/home/u/repo"} {
		if strings.Contains(out, leak) {
			t.Fatalf("main view leaked %q on a group header:\n%s", leak, out)
		}
	}
}

func TestApplyRemoteFilterSelectsFirstSessionNotHeader(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", Name: "impl", Group: "WTB-1", TmuxSession: "impl", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	it, ok := m.selectedSessionItem()
	if !ok {
		t.Fatal("expected a session, not a group header")
	}
	if it.session.ID != "s1" {
		t.Fatalf("selected %s", it.session.ID)
	}
}

func TestInitialTmuxTickSelectsSessionNotHeader(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", Name: "impl", Group: "WTB-1", TmuxSession: "impl", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	m.list.Select(0)
	m.initialLoad = true
	_, _ = m.applyTmuxTick(tmuxTickMsg{}, nil)
	it, ok := m.selectedSessionItem()
	if !ok || it.session.ID != "s1" {
		t.Fatalf("after first tick want session s1, ok=%v id=%q header=%v", ok, it.session.ID, it.header)
	}
}
