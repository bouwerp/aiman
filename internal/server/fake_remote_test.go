package server

import (
	"context"
	"sync"

	"github.com/bouwerp/aiman/internal/domain"
)

type fakeRemote struct {
	mu    sync.Mutex
	execs []string
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
func (f *fakeRemote) ScanGitRepos(context.Context) ([]string, error)  { return nil, nil }
func (f *fakeRemote) ScanWorktrees(context.Context, string) ([]string, error) {
	return nil, nil
}
func (f *fakeRemote) GetGitRoot(context.Context, string) (string, error) { return "", nil }
func (f *fakeRemote) ProvisionRemote(context.Context, []domain.ProvisionStep, chan<- domain.ProvisionProgress) error {
	return nil
}
func (f *fakeRemote) Close() error { return nil }

type fakeCreator struct {
	cfg   domain.SessionConfig
	sess  *domain.Session
	calls int
	// onCreate runs inside CreateSession, standing in for whatever happens on
	// the host while a create is in flight.
	onCreate func()
	// failWith, when set, can fail a create for the config it was given.
	failWith func(domain.SessionConfig) error
	// branches lists the branch of every create attempted.
	branches []string
}

func (f *fakeCreator) CreateSession(_ context.Context, cfg domain.SessionConfig) (*domain.Session, error) {
	f.cfg = cfg
	f.calls++
	f.branches = append(f.branches, cfg.Branch)
	if f.failWith != nil {
		if err := f.failWith(cfg); err != nil {
			return nil, err
		}
	}
	if f.onCreate != nil {
		f.onCreate()
	}
	if f.sess != nil {
		return f.sess, nil
	}
	agent := ""
	if cfg.Agent != nil {
		agent = cfg.Agent.Name
	}
	id := "new-id"
	if cfg.SessionID != "" {
		id = cfg.SessionID
	}
	return &domain.Session{
		ID: id, Name: cfg.Name, Group: cfg.Group, Branch: cfg.Branch,
		BaseBranch: cfg.BaseBranch, AgentName: agent, Status: domain.SessionStatusActive,
		TerminalName: cfg.Branch,
	}, nil
}
