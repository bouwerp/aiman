package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
)

func TestNewModelShowsGroupHeadersWithoutExtraFilter(t *testing.T) {
	cfg := twoRemoteCfg()
	sessions := []domain.Session{
		{ID: "a", Name: "impl", Group: "WTB-1", RemoteHost: "10.0.1.5"},
		{ID: "b", Name: "q1", Group: "quick", RemoteHost: "10.0.1.5"},
	}
	m := NewModel(cfg, nil, sessions, &mockSessionRepo{}, nil, nil, nil)
	items := m.list.Items()
	if len(items) != 4 {
		t.Fatalf("len=%d want 4 (2 headers + 2 sessions)", len(items))
	}
	h, ok := items[0].(item)
	if !ok || !h.header {
		t.Fatalf("first row must be a group header, got %+v", items[0])
	}
}

func TestCollapsedGroupHidesChildren(t *testing.T) {
	flat := []item{
		{session: domain.Session{ID: "1", Name: "impl", Group: "WTB-1", RemoteHost: "h"}, remoteName: "box"},
		{session: domain.Session{ID: "2", Name: "q1", Group: "quick", RemoteHost: "h"}, remoteName: "box"},
	}
	key := groupCollapseKey("WTB-1", "box")
	got := groupedSessionItems(flat, map[string]bool{key: true})
	if len(got) != 3 {
		t.Fatalf("len=%d want 3 (collapsed header + quick header + q1)", len(got))
	}
	h := got[0].(item)
	if !h.header || !h.collapsed || !strings.HasPrefix(h.Title(), "▸ WTB-1") {
		t.Fatalf("collapsed header %+v title %q", h, h.Title())
	}
	if h.groupN != 1 {
		t.Fatalf("count %d", h.groupN)
	}
}

func TestToggleCollapseKeepsHeaderSelected(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", Name: "impl", Group: "WTB-1", TmuxSession: "impl", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	m.list.Select(0)
	_, _, handled := m.toggleSelectedGroupCollapsed()
	if !handled {
		t.Fatal("toggle should handle a header")
	}
	it, ok := m.list.SelectedItem().(item)
	if !ok || !it.header || !it.collapsed {
		t.Fatalf("want collapsed header selected, got %+v ok=%v", it, ok)
	}
	if len(m.list.Items()) != 1 {
		t.Fatalf("collapsed list len=%d", len(m.list.Items()))
	}
}

func TestRenameGroupUpdatesAllMembers(t *testing.T) {
	cfg := twoRemoteCfg()
	sessions := []domain.Session{
		{ID: "a", Name: "impl", Group: "WTB-1", RemoteHost: "10.0.1.5"},
		{ID: "b", Name: "reviewer", Group: "WTB-1", RemoteHost: "10.0.1.5"},
		{ID: "c", Name: "q1", Group: "quick", RemoteHost: "10.0.1.5"},
	}
	m := NewModel(cfg, nil, sessions, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	m.list.Select(0)
	it := m.list.SelectedItem().(item)
	_, cmd, _ := m.startRenameGroup(it)
	_ = cmd
	m.genericInput = NewTextInputModel("Rename group", "group", "spike")
	got, _ := m.handleRenameGroupUpdate(tea.KeyMsg{Type: tea.KeyEnter})
	model := got.(*Model)
	if model.state != viewStateMain {
		t.Fatalf("state %v err %q", model.state, model.genericInput.Error)
	}
	var wtb, spike int
	for _, s := range model.allSessions {
		switch s.Group {
		case "spike":
			spike++
		case "WTB-1":
			wtb++
		}
	}
	if spike != 2 || wtb != 0 {
		t.Fatalf("spike=%d leftover WTB-1=%d", spike, wtb)
	}
}

func TestAssignGroupOnCreatingPlaceholderDoesNotPersist(t *testing.T) {
	repo := &savingSessionRepo{}
	cfg := twoRemoteCfg()
	m := NewModel(cfg, nil, nil, repo, nil, nil, nil)
	m.selectedRemote = cfg.Remotes[0]
	m.sessionCfg = domain.SessionConfig{Branch: "feature/pb-1", Repo: domain.Repo{Name: "org/repo"}}
	_ = m.startBackgroundCreate()
	id := m.allSessions[0].ID
	if err := m.setSessionGroup(id, "spike"); err != nil {
		t.Fatal(err)
	}
	if m.allSessions[0].Group != "spike" {
		t.Fatalf("group %q", m.allSessions[0].Group)
	}
	if len(repo.saved) != 0 {
		t.Fatalf("placeholder must not be persisted, saved %v", repo.saved)
	}
}

func TestAssignSessionToExistingAndUngrouped(t *testing.T) {
	cfg := twoRemoteCfg()
	sessions := []domain.Session{
		{ID: "a", Name: "impl", Group: "WTB-1", RemoteHost: "10.0.1.5"},
		{ID: "b", Name: "q1", Group: "quick", RemoteHost: "10.0.1.5"},
	}
	m := NewModel(cfg, nil, sessions, &mockSessionRepo{}, nil, nil, nil)
	if err := m.setSessionGroup("b", "WTB-1"); err != nil {
		t.Fatal(err)
	}
	if m.allSessions[1].Group != "WTB-1" {
		t.Fatalf("assign existing got %q", m.allSessions[1].Group)
	}
	if err := m.setSessionGroup("b", domain.GroupUngrouped); err != nil {
		t.Fatal(err)
	}
	if m.allSessions[1].Group != domain.GroupUngrouped {
		t.Fatalf("unassign got %q", m.allSessions[1].Group)
	}
}

func TestAssignNewGroupFromPicker(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "a", Name: "impl", Group: "quick", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	_, _, _ = m.startAssignGroup(s)
	if m.state != viewStateAssignGroup {
		t.Fatalf("state %v", m.state)
	}
	last := m.assignChoices[len(m.assignChoices)-1]
	if !last.isNew {
		t.Fatalf("last choice should be new group: %+v", last)
	}
	m.assignCursor = len(m.assignChoices) - 1
	got, _ := m.handleAssignGroupUpdate(tea.KeyMsg{Type: tea.KeyEnter})
	model := got.(*Model)
	if model.state != viewStateNewGroup {
		t.Fatalf("state %v", model.state)
	}
	model.genericInput = NewTextInputModel("New group", "group name", "spike")
	got, _ = model.handleNewGroupUpdate(tea.KeyMsg{Type: tea.KeyEnter})
	model = got.(*Model)
	if model.state != viewStateMain {
		t.Fatalf("state %v err %q", model.state, model.genericInput.Error)
	}
	if model.allSessions[0].Group != "spike" {
		t.Fatalf("group %q", model.allSessions[0].Group)
	}
}

func TestGKeyOpensAssignGroup(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", Name: "impl", Group: "quick", TmuxSession: "impl", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	got := pressMainKey(m, 'g')
	if got.state != viewStateAssignGroup {
		t.Fatalf("state %v", got.state)
	}
	if got.assigningSessionID != "s1" {
		t.Fatalf("id %s", got.assigningSessionID)
	}
}
