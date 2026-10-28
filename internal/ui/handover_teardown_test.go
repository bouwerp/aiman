package ui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/usecase"
)

// recordingExec records each remote command and answers with err.
type recordingExec struct {
	cmds []string
	err  error
}

func (r *recordingExec) Execute(_ context.Context, cmd string) (string, error) {
	r.cmds = append(r.cmds, cmd)
	return "", r.err
}

func (r *recordingExec) WriteFile(context.Context, string, []byte) error { return nil }

func isSkipReason(err error) bool {
	var skip skipReason
	return errors.As(err, &skip)
}

// rpcParams decodes the params of the first `aiman rpc <method>` in cmds.
func rpcParams(t *testing.T, cmds []string, method string) map[string]string {
	t.Helper()
	for _, c := range cmds {
		if !strings.Contains(c, "aiman rpc "+method+" ") {
			continue
		}
		_, rest, ok := strings.Cut(c, "printf %s '")
		if !ok {
			t.Fatalf("no params in %q", c)
		}
		enc, _, _ := strings.Cut(rest, "'")
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			t.Fatalf("decode params: %v", err)
		}
		var params map[string]string
		if err := json.Unmarshal(raw, &params); err != nil {
			t.Fatalf("unmarshal params: %v", err)
		}
		return params
	}
	t.Fatalf("no %s call in %v", method, cmds)
	return nil
}

func TestTerminateSaveContextWritesForgetHandoverFirst(t *testing.T) {
	exec := &recordingExec{}
	m := &Model{cfg: &config.Config{Remotes: []config.Remote{{Host: "regent0", User: "code"}}}}
	m.handoverDial = func(config.Remote) usecase.TerminalExecutor { return exec }
	s := domain.Session{ID: "s1", RemoteHost: "regent0"}

	if err := m.runTerminateStep(0, s, false); !isSkipReason(err) {
		t.Fatalf("step 0 err = %v, want a skip (no snapshot manager)", err)
	}
	params := rpcParams(t, exec.cmds, "handover.write")
	if params["session"] != "s1" || params["reason"] != "forget" {
		t.Fatalf("handover.write params = %v", params)
	}
}

func TestTerminateSaveContextIgnoresHandoverFailure(t *testing.T) {
	exec := &recordingExec{err: errors.New("ssh down")}
	m := &Model{cfg: &config.Config{Remotes: []config.Remote{{Host: "regent0", User: "code"}}}}
	m.handoverDial = func(config.Remote) usecase.TerminalExecutor { return exec }

	err := m.runTerminateStep(0, domain.Session{ID: "s1", RemoteHost: "regent0"}, false)
	if err != nil && !isSkipReason(err) {
		t.Fatalf("handover failure blocked teardown: %v", err)
	}
	if len(exec.cmds) == 0 {
		t.Fatal("handover.write was not attempted")
	}
}

func TestHandoverRoleSessionsLeftOutOfList(t *testing.T) {
	worker := domain.Session{ID: "w1", Name: "impl", TerminalName: "w1", RemoteHost: "10.0.1.5"}
	child := domain.Session{ID: "h1", Name: "handover", TerminalName: "h1", RemoteHost: "10.0.1.5", Role: domain.SessionRoleHandover, ParentID: "w1"}
	m := NewModel(twoRemoteCfg(), nil, []domain.Session{worker, child}, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	if len(m.filteredSessions) != 1 || m.filteredSessions[0].session.ID != "w1" {
		ids := make([]string, 0, len(m.filteredSessions))
		for _, it := range m.filteredSessions {
			ids = append(ids, it.session.ID)
		}
		t.Fatalf("listed sessions = %v, want [w1]", ids)
	}
}

func TestHandoverRoleNotOfferedVerify(t *testing.T) {
	s := domain.Session{ID: "h1", BaseBranch: "main", Role: domain.SessionRoleHandover}
	if offerVerify(s, domain.GitStatus{AheadOfBase: 2}, false) {
		t.Fatal("handover child offered verify")
	}
}
