package server

import (
	"context"
	"io"
	"os/exec"
	"sync"

	"github.com/bouwerp/aiman/internal/domain"
)

type fakeRemote struct {
	mu    sync.Mutex
	pane  string
	execs []string

	// tmuxSessions are the live sessions this host reports, and tmuxEnv maps a
	// session name to its AIMAN_ID — the join key serve discovers them by.
	tmuxSessions []string
	tmuxEnv      map[string]string
	tmuxCWD      map[string]string
}

func (f *fakeRemote) Connect(context.Context) error { return nil }
func (f *fakeRemote) GetRoot() string               { return "/tmp" }
func (f *fakeRemote) Execute(_ context.Context, cmd string) (string, error) {
	f.mu.Lock()
	f.execs = append(f.execs, cmd)
	f.mu.Unlock()
	return "", nil
}
func (f *fakeRemote) WriteFile(context.Context, string, []byte) error { return nil }
func (f *fakeRemote) ValidateDir(context.Context, string) error       { return nil }
func (f *fakeRemote) ScanTmuxSessions(context.Context) ([]string, error) {
	return f.tmuxSessions, nil
}
func (f *fakeRemote) ScanGitRepos(context.Context) ([]string, error) { return nil, nil }
func (f *fakeRemote) ScanWorktrees(context.Context, string) ([]string, error) {
	return nil, nil
}
func (f *fakeRemote) GetGitRoot(context.Context, string) (string, error) { return "", nil }
func (f *fakeRemote) GetTmuxSessionCWD(_ context.Context, name string) (string, error) {
	return f.tmuxCWD[name], nil
}
func (f *fakeRemote) GetTmuxSessionEnv(_ context.Context, name, _ string) (string, error) {
	return f.tmuxEnv[name], nil
}
func (f *fakeRemote) CaptureTmuxPane(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pane, nil
}
func (f *fakeRemote) AttachTmuxSession(string) *exec.Cmd { return nil }
func (f *fakeRemote) StreamTmuxSession(context.Context, string) (io.ReadWriteCloser, error) {
	return nil, nil
}
func (f *fakeRemote) StartTmuxSession(context.Context, string) error { return nil }
func (f *fakeRemote) ProvisionRemote(context.Context, []domain.ProvisionStep, chan<- domain.ProvisionProgress) error {
	return nil
}
func (f *fakeRemote) Close() error { return nil }

type fakeCreator struct {
	cfg  domain.SessionConfig
	sess *domain.Session
}

func (f *fakeCreator) CreateSession(_ context.Context, cfg domain.SessionConfig) (*domain.Session, error) {
	f.cfg = cfg
	if f.sess != nil {
		return f.sess, nil
	}
	agent := ""
	if cfg.Agent != nil {
		agent = cfg.Agent.Name
	}
	return &domain.Session{
		ID: "new-id", Name: cfg.Name, Group: cfg.Group, Branch: cfg.Branch,
		AgentName: agent, Status: domain.SessionStatusActive,
		TmuxSession: cfg.Branch,
	}, nil
}
