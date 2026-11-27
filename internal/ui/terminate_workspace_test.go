package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/usecase"
)

func TestWorkspaceTerminateCallsServe(t *testing.T) {
	calls := fakeIdentityRPC(t, nil)
	s := wsTerminateSession()
	s.RemoteHost = "10.0.1.9"
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	if err := m.runTerminateStep(workspaceServeStep, s, true); err != nil {
		t.Fatalf("serve step: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("calls %+v", *calls)
	}
	c := (*calls)[0]
	if c.method != "session.terminate" || c.params["session"] != "ws-1" || c.params["discard"] != true || c.params["keep_children"] != true {
		t.Fatalf("call %+v", c)
	}
}

func TestWorkspaceTerminateRefusalKeepsSession(t *testing.T) {
	refusal := &usecase.RPCError{Code: "worktree_dirty", Message: "aiman has 2 changed files"}
	fakeIdentityRPC(t, refusal)
	s := wsTerminateSession()
	s.RemoteHost = "10.0.1.9"
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.allSessions = append(m.allSessions, s)
	m.startBackgroundTerminate(s, false)
	err := m.runTerminateStep(workspaceServeStep, s, false)
	if err == nil {
		t.Fatal("want serve's refusal")
	}
	_, cmd := m.applyTerminateStepMsg(terminateStepMsg{sessionID: s.ID, index: workspaceServeStep, err: err}, nil)
	if cmd == nil {
		t.Fatal("want a toast")
	}
	if m.isTerminatingSession(s.ID) {
		t.Fatal("teardown must stop after serve refuses")
	}
	if len(m.allSessions) != 1 {
		t.Fatalf("session must stay listed, have %d", len(m.allSessions))
	}
}
