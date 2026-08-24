package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestRenameEnterConfirmsNewName(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", Name: "q1", Group: "quick", TmuxSession: "q1", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	updated, _, _ := m.handleMainKeyMsg(pressKey("e"))
	m = updated.(*Model)
	m.genericInput = NewTextInputModel("Rename session", "name", "reviewer")
	got, _ := m.handleRenameSessionUpdate(pressKey("enter"))
	model := got.(*Model)
	if model.state != viewStateMain {
		t.Fatalf("state %v, err %q", model.state, model.genericInput.Error)
	}
	if model.allSessions[0].Name != "reviewer" {
		t.Fatalf("name %q", model.allSessions[0].Name)
	}
}

func TestRenameEnterAsCtrlJConfirms(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", Name: "q1", Group: "quick", TmuxSession: "q1", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	updated, _, _ := m.handleMainKeyMsg(pressKey("e"))
	m = updated.(*Model)
	m.genericInput = NewTextInputModel("Rename session", "name", "spike")
	got, _ := m.handleRenameSessionUpdate(pressKey("ctrl+j"))
	model := got.(*Model)
	if model.state != viewStateMain {
		t.Fatalf("state %v, err %q", model.state, model.genericInput.Error)
	}
	if model.allSessions[0].Name != "spike" {
		t.Fatalf("name %q", model.allSessions[0].Name)
	}
}

func TestRenameInvalidNameStaysAndShowsError(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", Name: "q1", Group: "quick", TmuxSession: "q1", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	updated, _, _ := m.handleMainKeyMsg(pressKey("e"))
	m = updated.(*Model)
	m.genericInput = NewTextInputModel("Rename session", "name", "1bad")
	got, _ := m.handleRenameSessionUpdate(pressKey("enter"))
	model := got.(*Model)
	if model.state != viewStateRenameSession {
		t.Fatalf("state %v", model.state)
	}
	if model.allSessions[0].Name != "q1" {
		t.Fatalf("name changed to %q", model.allSessions[0].Name)
	}
	if model.genericInput.Error == "" || !strings.Contains(model.genericInput.viewString(), model.genericInput.Error) {
		t.Fatalf("expected visible error, got %q view %q", model.genericInput.Error, model.genericInput.viewString())
	}
}

func TestRenameConfirmsEvenIfListLosesSelection(t *testing.T) {
	cfg := twoRemoteCfg()
	s := domain.Session{ID: "s1", Name: "q1", Group: "quick", TmuxSession: "q1", RemoteHost: "10.0.1.5"}
	m := NewModel(cfg, nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	updated, _, _ := m.handleMainKeyMsg(pressKey("e"))
	m = updated.(*Model)
	m.genericInput = NewTextInputModel("Rename session", "name", "reviewer")
	m.list.Select(0) // group header
	got, _ := m.handleRenameSessionUpdate(pressKey("enter"))
	model := got.(*Model)
	if model.state != viewStateMain {
		t.Fatalf("state %v, err %q", model.state, model.genericInput.Error)
	}
	if model.allSessions[0].Name != "reviewer" {
		t.Fatalf("name %q", model.allSessions[0].Name)
	}
}
