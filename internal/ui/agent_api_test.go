package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/remotesvc"
	tea "github.com/charmbracelet/bubbletea"
)

func TestMenuHasAgentAPIItem(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	found := false
	for _, it := range m.menu.Items() {
		mi, ok := it.(menuItem)
		if ok && mi.action == viewStateAgentAPI {
			found = true
			if !strings.Contains(strings.ToLower(mi.Title()), "agent api") {
				t.Fatalf("title %q", mi.Title())
			}
			break
		}
	}
	if !found {
		t.Fatal("admin menu missing Agent API")
	}
}

func TestMenuEnterOpensAgentAPISettings(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.state = viewStateMenu
	for i, it := range m.menu.Items() {
		if mi, ok := it.(menuItem); ok && mi.action == viewStateAgentAPI {
			m.menu.Select(i)
			break
		}
	}
	got, _ := m.handleMenuUpdate(tea.KeyMsg{Type: tea.KeyEnter})
	model := got.(*Model)
	if model.state != viewStateAgentAPI {
		t.Fatalf("state %v", model.state)
	}
}

func TestAgentAPIViewListsEachRemote(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.enterAgentAPI()
	out := m.renderAgentAPIView()
	for _, want := range []string{"Agent API", "10.0.1.5", "i install/enable", "STOPPED"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
}

func TestAgentAPIInstallStaysOnSettingsPage(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.state = viewStateAgentAPI
	m.storeDaemon(domain.Daemon{RemoteHost: "10.0.1.5", Kind: string(remotesvc.KindServe)})
	_, _, _ = m.runSelectedServiceOp("install", "Installing")
	if m.state != viewStateLoading {
		t.Fatalf("state %v", m.state)
	}
	if m.loadingNext != viewStateAgentAPI {
		t.Fatalf("loadingNext %v", m.loadingNext)
	}
}

func TestAgentAPICursorMovesBetweenRemotes(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.state = viewStateAgentAPI
	got, _ := m.handleAgentAPIUpdate(tea.KeyMsg{Type: tea.KeyDown})
	model := got.(*Model)
	if model.agentAPICursor != 1 {
		t.Fatalf("cursor %d", model.agentAPICursor)
	}
	d, ok := model.selectedAgentAPIDaemon()
	if !ok || d.RemoteHost == "10.0.1.5" {
		t.Fatalf("selected %+v ok=%v", d, ok)
	}
}
