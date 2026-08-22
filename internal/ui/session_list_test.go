package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestSessionStateColorByActivity(t *testing.T) {
	tests := []struct {
		it   item
		want lipgloss.Color
	}{
		{item{activity: "busy"}, stateColorWorking},
		{item{activity: "creating"}, stateColorWorking},
		{item{needsInput: true}, stateColorWaiting},
		{item{activity: "idle"}, stateColorIdle},
		{item{activity: "stale"}, stateColorError},
		{item{activity: "create-failed"}, stateColorError},
		{item{session: domain.Session{AgentEnded: true}}, stateColorEnded},
		{item{header: true, activity: "waiting"}, stateColorWaiting},
		{item{header: true, activity: "working"}, stateColorWorking},
		{item{header: true, activity: "idle"}, stateColorIdle},
	}
	for _, tt := range tests {
		if got := sessionStateColor(tt.it); got != tt.want {
			t.Fatalf("state=%q needs=%v ended=%v header=%v: got %q want %q",
				tt.it.activity, tt.it.needsInput, tt.it.session.AgentEnded, tt.it.header, got, tt.want)
		}
	}
}

func TestSessionTitleStaysUncolored(t *testing.T) {
	it := item{session: domain.Session{Name: "impl"}, activity: "busy"}
	if strings.Contains(it.Title(), "\x1b") {
		t.Fatalf("Title() must stay plain for tests, got %q", it.Title())
	}
}

func TestSessionListDelegatePaintsState(t *testing.T) {
	orig := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(orig) })

	busy := item{session: domain.Session{Name: "impl"}, activity: "busy"}
	idle := item{session: domain.Session{Name: "q1"}, activity: "idle"}
	wait := item{session: domain.Session{Name: "reviewer"}, needsInput: true}
	items := []list.Item{busy, idle, wait}
	l := list.New(items, newSessionListDelegate(), 48, 12)
	d := newSessionListDelegate()
	var b strings.Builder
	d.Render(&b, l, 0, busy)
	busyOut := b.String()
	b.Reset()
	d.Render(&b, l, 1, idle)
	idleOut := b.String()
	b.Reset()
	d.Render(&b, l, 2, wait)
	waitOut := b.String()
	if !strings.Contains(busyOut, "impl") || !strings.Contains(busyOut, "\x1b") {
		t.Fatalf("busy row: %q", busyOut)
	}
	if busyOut == idleOut || idleOut == waitOut {
		t.Fatalf("expected distinct colors\nbusy=%q\nidle=%q\nwait=%q", busyOut, idleOut, waitOut)
	}
}

func TestGroupedSessionItemsHeadersAndRollup(t *testing.T) {
	flat := []item{
		{session: domain.Session{ID: "2", Name: "reviewer", Group: "WTB-1925"}, needsInput: true, remoteName: "regent0"},
		{session: domain.Session{ID: "1", Name: "impl", Group: "WTB-1925"}, activity: "busy", remoteName: "regent0"},
		{session: domain.Session{ID: "3", Name: "q1", Group: "quick"}, activity: "idle", remoteName: "regent0"},
	}
	got := groupedSessionItems(flat, nil)
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

func TestGroupedSessionTitlesNestUnderHeader(t *testing.T) {
	flat := []item{
		{session: domain.Session{ID: "2", Name: "reviewer", Group: "WTB-1925"}, needsInput: true, remoteName: "regent0"},
		{session: domain.Session{ID: "1", Name: "impl", Group: "WTB-1925"}, activity: "busy", remoteName: "regent0"},
		{session: domain.Session{ID: "3", Name: "q1", Group: "quick"}, activity: "idle", remoteName: "regent0"},
	}
	got := groupedSessionItems(flat, nil)
	h := got[0].(item)
	ht := h.Title()
	if !strings.HasPrefix(ht, "▾ WTB-1925") {
		t.Fatalf("header title %q", ht)
	}
	if !strings.Contains(ht, "· 2") {
		t.Fatalf("header should show session count, got %q", ht)
	}
	if !strings.Contains(ht, "[regent0]") {
		t.Fatalf("header should carry the remote, got %q", ht)
	}
	if h.Description() != "" {
		t.Fatalf("header description should be empty, got %q", h.Description())
	}

	mid := got[1].(item)
	last := got[2].(item)
	if !strings.HasPrefix(mid.Title(), "  ├─ ") {
		t.Fatalf("first child %q", mid.Title())
	}
	if !strings.HasPrefix(last.Title(), "  └─ ") {
		t.Fatalf("last child %q", last.Title())
	}
	if strings.Contains(mid.Title(), "[regent0]") || strings.Contains(last.Title(), "[regent0]") {
		t.Fatalf("children should not repeat the remote tag: %q / %q", mid.Title(), last.Title())
	}

	q := got[4].(item)
	if !strings.HasPrefix(q.Title(), "  └─ ") {
		t.Fatalf("sole child of a group is a last branch, got %q", q.Title())
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

func TestSnapOffGroupHeaderDownSelectsChildSession(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", Name: "impl", Group: "WTB-1", TmuxSession: "impl", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	m.list.Select(0)
	if it, ok := m.list.SelectedItem().(item); !ok || !it.header {
		t.Fatal("setup: want header at 0")
	}
	m.snapOffGroupHeader(1)
	it, ok := m.selectedSessionItem()
	if !ok || it.session.ID != "s1" {
		t.Fatalf("down from header want session s1, ok=%v id=%q", ok, it.session.ID)
	}
}

func TestSnapOffGroupHeaderUpSelectsPreviousGroupSession(t *testing.T) {
	cfg := twoRemoteCfg()
	sessions := []domain.Session{
		{ID: "a", Name: "impl", Group: "WTB-1", TmuxSession: "a", RemoteHost: "10.0.1.5"},
		{ID: "b", Name: "q1", Group: "quick", TmuxSession: "b", RemoteHost: "10.0.1.5"},
	}
	m := NewModel(cfg, nil, sessions, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	items := m.list.Items()
	headerIdx := -1
	for i, it := range items {
		if si, ok := it.(item); ok && si.header && si.session.Group == "quick" {
			headerIdx = i
			break
		}
	}
	if headerIdx < 0 {
		t.Fatal("missing quick header")
	}
	m.list.Select(headerIdx)
	m.snapOffGroupHeader(-1)
	it, ok := m.selectedSessionItem()
	if !ok || it.session.ID != "a" {
		t.Fatalf("up from next-group header want session a, ok=%v id=%q", ok, it.session.ID)
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
