package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// Trust never reaches a pull request checkout, whoever asks for it.
func TestTrustSkipsPRWorktree(t *testing.T) {
	r := &piTrustRemote{probe: "/home/code\npi\n"}
	TrustWorkspacePaths(context.Background(), r, "/h/.aiman/pr-reviews/s/work", "/w/app@pr-7")
	for _, cmd := range r.cmds {
		if strings.Contains(cmd, "app@pr-7") {
			t.Fatalf("trust command names the PR checkout: %q", cmd)
		}
	}
	for path, body := range r.written {
		if strings.Contains(string(body), "app@pr-7") {
			t.Fatalf("%s trusts the PR checkout: %s", path, body)
		}
	}
	if !strings.Contains(string(r.written["/home/code/.pi/agent/trust.json"]), "/h/.aiman/pr-reviews/s/work") {
		t.Fatal("the scratch directory was not trusted")
	}
}

// No launch path starts an agent inside a pull request checkout.
func TestCreatePTYSessionRefusesPRWorktree(t *testing.T) {
	r := &terminalRemote{}
	err := CreatePTYSession(context.Background(), r, PTYSpec{ID: "sid", Dir: "/w/app@pr-7/sub", Command: "claude"})
	if !errors.Is(err, domain.ErrPRWorktreeLaunch) {
		t.Fatalf("err %v, want ErrPRWorktreeLaunch", err)
	}
	if len(r.commands) != 0 || len(r.written) != 0 {
		t.Fatalf("ran %v, wrote %v", r.commands, r.written)
	}
}

type oneSessionStore struct{ id string }

func (s oneSessionStore) Get(_ context.Context, id string) (*domain.Session, error) {
	if id == s.id {
		return &domain.Session{ID: id}, nil
	}
	return nil, errors.New("not found")
}

func TestCreateSessionChecksCallerSessionID(t *testing.T) {
	const taken = "6f1c7a3e-2b4d-4c8e-9a10-0123456789ab"
	m := &FlowManager{}
	m.SetSessionStore(oneSessionStore{id: taken})
	agent := &domain.Agent{Name: "claude", Command: "claude"}
	for _, id := range []string{"not-a-uuid", "../x", taken} {
		if _, err := m.CreateSession(context.Background(), domain.SessionConfig{SessionID: id, Agent: agent}); err == nil || !strings.Contains(err.Error(), "session id") {
			t.Errorf("id %q: err %v, want a session id refusal", id, err)
		}
	}
}

// Create refuses a working directory inside a PR checkout before it trusts
// or launches anything.
func TestCreateSessionRefusesPRWorktreeDir(t *testing.T) {
	r := &terminalRemote{}
	m := &FlowManager{sshManager: nil}
	cfg := domain.SessionConfig{
		Agent: &domain.Agent{Name: "claude", Command: "claude"}, AdHoc: true,
		PreparedWorktree: "/w/app@pr-7", SSHManager: &trustRecorderRemote{terminalRemote: r},
	}
	if _, err := m.CreateSession(context.Background(), cfg); !errors.Is(err, domain.ErrPRWorktreeLaunch) {
		t.Fatalf("err %v, want ErrPRWorktreeLaunch", err)
	}
	if len(r.commands) != 0 {
		t.Fatalf("ran %v", r.commands)
	}
}

// trustRecorderRemote is a RemoteExecutor that records through a
// terminalRemote; anything else it is asked panics.
type trustRecorderRemote struct {
	domain.RemoteExecutor
	terminalRemote *terminalRemote
}

func (r *trustRecorderRemote) Execute(ctx context.Context, cmd string) (string, error) {
	return r.terminalRemote.Execute(ctx, cmd)
}

func (r *trustRecorderRemote) WriteFile(ctx context.Context, path string, content []byte) error {
	return r.terminalRemote.WriteFile(ctx, path, content)
}

func (r *trustRecorderRemote) GetRoot() string { return "/w" }
