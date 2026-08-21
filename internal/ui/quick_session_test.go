package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	tea "github.com/charmbracelet/bubbletea"
)

func TestQuickStartKeyUsesDefaultRemote(t *testing.T) {
	cfg := twoRemoteCfg()
	cfg.ActiveRemote = "10.0.1.9"
	m := NewModel(cfg, nil, nil, &mockSessionRepo{}, nil, nil, nil)
	updated, cmd, handled := m.handleMainKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'N'}})
	if !handled {
		t.Fatal("N should be handled")
	}
	got := updated.(*Model)
	if !got.sessionCfg.Quick || !got.sessionCfg.AdHoc {
		t.Fatalf("expected quick ad-hoc config, got %+v", got.sessionCfg)
	}
	if got.selectedRemote.Host != "10.0.1.9" {
		t.Fatalf("selected remote %q", got.selectedRemote.Host)
	}
	if got.state != viewStateLoading {
		t.Fatalf("state %v want loading", got.state)
	}
	if cmd == nil {
		t.Fatal("expected fetchAgents command")
	}
}

func TestQuickStartNoRemotesStaysOnMain(t *testing.T) {
	m := &Model{cfg: &config.Config{}, state: viewStateMain}
	got := pressMainKey(m, 'N')
	if got.state != viewStateMain {
		t.Fatalf("state %v", got.state)
	}
}

func TestRenameKeyOpensInput(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", Name: "q1", Group: "quick", TmuxSession: "q1", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	updated, _, handled := m.handleMainKeyMsg(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if !handled {
		t.Fatal("e should be handled")
	}
	got := updated.(*Model)
	if got.state != viewStateRenameSession {
		t.Fatalf("state %v", got.state)
	}
	if got.genericInput.Value() != "q1" {
		t.Fatalf("prefill %q", got.genericInput.Value())
	}
}
