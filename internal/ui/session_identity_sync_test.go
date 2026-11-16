package ui

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/usecase"
)

type identityCall struct {
	host   string
	method string
	params map[string]any
}

// fakeIdentityRPC replaces sessionIdentityRPC for one test, records each call
// and answers with err.
func fakeIdentityRPC(t *testing.T, err error) *[]identityCall {
	t.Helper()
	var calls []identityCall
	prev := sessionIdentityRPC
	sessionIdentityRPC = func(_ context.Context, r config.Remote, method string, params any) (json.RawMessage, error) {
		raw, _ := json.Marshal(params)
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		calls = append(calls, identityCall{host: r.Host, method: method, params: p})
		if err != nil {
			return nil, err
		}
		return json.RawMessage(`{"type":"session"}`), nil
	}
	t.Cleanup(func() { sessionIdentityRPC = prev })
	return &calls
}

// runIdentityCmd runs cmd and feeds its message back through Update.
func runIdentityCmd(t *testing.T, m *Model, cmd tea.Cmd) tea.Cmd {
	t.Helper()
	if cmd == nil {
		t.Fatal("want a serve sync command, got nil")
	}
	msg := cmd()
	if _, ok := msg.(sessionIdentitySyncMsg); !ok {
		t.Fatalf("msg %T, want sessionIdentitySyncMsg", msg)
	}
	_, next := m.Update(msg)
	return next
}

func TestAssignGroupTellsServe(t *testing.T) {
	calls := fakeIdentityRPC(t, nil)
	s := domain.Session{ID: "s1", Name: "impl", Group: domain.GroupUngrouped, RemoteHost: "10.0.1.9"}
	m := NewModel(twoRemoteCfg(), nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	_, _, _ = m.startAssignGroup(s)
	m.state = viewStateNewGroup
	m.genericInput = NewTextInputModel("New group", "group name", "spike")
	got, cmd := m.handleNewGroupUpdate(pressKey("enter"))
	model := got.(*Model)
	if model.allSessions[0].Group != "spike" {
		t.Fatalf("group %q", model.allSessions[0].Group)
	}
	runIdentityCmd(t, model, cmd)
	if len(*calls) != 1 {
		t.Fatalf("calls %+v", *calls)
	}
	c := (*calls)[0]
	if c.host != "10.0.1.9" || c.method != "session.move" || c.params["id"] != "s1" || c.params["group"] != "spike" {
		t.Fatalf("call %+v", c)
	}
}

func TestAssignExistingGroupTellsServe(t *testing.T) {
	calls := fakeIdentityRPC(t, nil)
	sessions := []domain.Session{
		{ID: "a", Name: "impl", Group: "WTB-1", RemoteHost: "10.0.1.5"},
		{ID: "b", Name: "q1", Group: "quick", RemoteHost: "10.0.1.5"},
	}
	m := NewModel(twoRemoteCfg(), nil, sessions, &mockSessionRepo{}, nil, nil, nil)
	_, _, _ = m.startAssignGroup(sessions[1])
	for i, ch := range m.assignChoices {
		if ch.group == "WTB-1" {
			m.assignCursor = i
		}
	}
	_, cmd := m.handleAssignGroupUpdate(pressKey("enter"))
	runIdentityCmd(t, m, cmd)
	if len(*calls) != 1 || (*calls)[0].params["id"] != "b" || (*calls)[0].params["group"] != "WTB-1" {
		t.Fatalf("calls %+v", *calls)
	}
}

func TestRenameGroupTellsServeForEveryMember(t *testing.T) {
	calls := fakeIdentityRPC(t, nil)
	sessions := []domain.Session{
		{ID: "a", Name: "impl", Group: "WTB-1", RemoteHost: "10.0.1.5"},
		{ID: "b", Name: "reviewer", Group: "WTB-1", RemoteHost: "10.0.1.5"},
		{ID: "pending-x", Name: "creating", Group: "WTB-1", RemoteHost: "10.0.1.5"},
		{ID: "c", Name: "q1", Group: "quick", RemoteHost: "10.0.1.5"},
	}
	m := NewModel(twoRemoteCfg(), nil, sessions, &mockSessionRepo{}, nil, nil, nil)
	m.renamingGroup = "WTB-1"
	m.renamingGroupHost = "10.0.1.5"
	m.state = viewStateRenameGroup
	m.genericInput = NewTextInputModel("Rename group", "group", "spike")
	got, cmd := m.handleRenameGroupUpdate(pressKey("enter"))
	runIdentityCmd(t, got.(*Model), cmd)
	ids := map[string]bool{}
	for _, c := range *calls {
		if c.method != "session.move" || c.params["group"] != "spike" || c.host != "10.0.1.5" {
			t.Fatalf("call %+v", c)
		}
		ids[c.params["id"].(string)] = true
	}
	if len(ids) != 2 || !ids["a"] || !ids["b"] {
		t.Fatalf("moved %v, want a and b only", ids)
	}
}

func TestRenameSessionTellsServe(t *testing.T) {
	calls := fakeIdentityRPC(t, nil)
	s := domain.Session{ID: "s1", Name: "q1", Group: "quick", TerminalName: "q1", RemoteHost: "10.0.1.5"}
	m := NewModel(twoRemoteCfg(), nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	updated, _, _ := m.handleMainKeyMsg(pressKey("e"))
	m = updated.(*Model)
	m.genericInput = NewTextInputModel("Rename session", "name", "reviewer")
	got, cmd := m.handleRenameSessionUpdate(pressKey("enter"))
	runIdentityCmd(t, got.(*Model), cmd)
	if len(*calls) != 1 {
		t.Fatalf("calls %+v", *calls)
	}
	c := (*calls)[0]
	if c.method != "session.rename" || c.params["id"] != "s1" || c.params["name"] != "reviewer" {
		t.Fatalf("call %+v", c)
	}
}

func TestPlaceholderGroupMoveDoesNotCallServe(t *testing.T) {
	calls := fakeIdentityRPC(t, nil)
	cfg := twoRemoteCfg()
	m := NewModel(cfg, nil, nil, &savingSessionRepo{}, nil, nil, nil)
	m.selectedRemote = cfg.Remotes[0]
	m.sessionCfg = domain.SessionConfig{Branch: "feature/pb-1", Repo: domain.Repo{Name: "org/repo"}}
	_ = m.startBackgroundCreate()
	_, _, _ = m.startAssignGroup(m.allSessions[0])
	m.state = viewStateNewGroup
	m.genericInput = NewTextInputModel("New group", "group name", "spike")
	_, cmd := m.handleNewGroupUpdate(pressKey("enter"))
	if cmd != nil {
		cmd()
	}
	if len(*calls) != 0 {
		t.Fatalf("placeholder must not reach serve: %+v", *calls)
	}
}

func TestServeSyncFailureToasts(t *testing.T) {
	fakeIdentityRPC(t, errors.New("ssh: connection refused"))
	s := domain.Session{ID: "s1", Name: "impl", Group: "quick", RemoteHost: "10.0.1.5"}
	m := NewModel(twoRemoteCfg(), nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	cmd, err := m.setSessionGroup("s1", "spike")
	if err != nil {
		t.Fatal(err)
	}
	if next := runIdentityCmd(t, m, cmd); next == nil {
		t.Fatal("want a toast clear timer")
	}
	if !m.snapshotToastError || m.snapshotToast == "" {
		t.Fatalf("toast %q error %v", m.snapshotToast, m.snapshotToastError)
	}
}

func TestServeSyncUnsupportedIsSilent(t *testing.T) {
	fakeIdentityRPC(t, usecase.ErrRPCUnsupported)
	s := domain.Session{ID: "s1", Name: "impl", Group: "quick", RemoteHost: "10.0.1.5"}
	m := NewModel(twoRemoteCfg(), nil, []domain.Session{s}, &mockSessionRepo{}, nil, nil, nil)
	cmd, err := m.setSessionGroup("s1", "spike")
	if err != nil {
		t.Fatal(err)
	}
	runIdentityCmd(t, m, cmd)
	if m.snapshotToast != "" {
		t.Fatalf("old serve must stay silent, toast %q", m.snapshotToast)
	}
}
