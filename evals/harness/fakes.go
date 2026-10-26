package harness

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/bouwerp/aiman/internal/domain"
)

// FakeRemote implements the remote operations the server calls during a case.
// Everything else is embedded as nil: a case that reaches an unimplemented
// method fails loudly instead of pretending the remote did something.
type FakeRemote struct {
	domain.RemoteExecutor
	Dir  string
	Pane string

	mu       sync.Mutex
	execs    []string
	sessions map[string]string
}

func NewFakeRemote(dir string) *FakeRemote {
	return &FakeRemote{Dir: dir, sessions: map[string]string{}}
}

func (r *FakeRemote) Execute(_ context.Context, cmd string) (string, error) {
	r.mu.Lock()
	r.execs = append(r.execs, cmd)
	r.mu.Unlock()
	return "", nil
}

func (r *FakeRemote) GetRoot() string { return r.Dir }

func (r *FakeRemote) WriteFile(_ context.Context, path string, content []byte) error {
	return os.WriteFile(path, content, 0o600)
}

func (r *FakeRemote) ValidateDir(context.Context, string) error { return nil }

func (r *FakeRemote) ScanTmuxSessions(context.Context) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.sessions))
	for name := range r.sessions {
		out = append(out, name)
	}
	return out, nil
}

func (r *FakeRemote) ScanGitRepos(context.Context) ([]string, error) { return nil, nil }

func (r *FakeRemote) ScanWorktrees(context.Context, string) ([]string, error) { return nil, nil }

func (r *FakeRemote) GetGitRoot(context.Context, string) (string, error) { return "", nil }

func (r *FakeRemote) GetTmuxSessionCWD(_ context.Context, name string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if cwd, ok := r.sessions[name]; ok {
		return cwd, nil
	}
	return r.Dir, nil
}

func (r *FakeRemote) GetTmuxSessionEnv(context.Context, string, string) (string, error) {
	return "", nil
}

func (r *FakeRemote) CaptureTmuxPane(context.Context, string) (string, error) {
	return r.Pane, nil
}

func (r *FakeRemote) StartTmuxSession(_ context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.sessions[name]; !ok {
		r.sessions[name] = r.Dir
	}
	return nil
}

func (r *FakeRemote) Close() error { return nil }

func (r *FakeRemote) addSession(name, dir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[name] = dir
}

// Execs returns a copy of the commands Execute received.
func (r *FakeRemote) Execs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.execs...)
}

// FakeCreator returns a session without cloning a repo or starting a terminal.
type FakeCreator struct {
	Dir string
}

func (c FakeCreator) CreateSession(_ context.Context, cfg domain.SessionConfig) (*domain.Session, error) {
	id := uuid.NewString()
	dir := filepath.Join(c.Dir, "sessions", id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	agent := ""
	if cfg.Agent != nil {
		agent = cfg.Agent.Command
	}
	return &domain.Session{
		ID:               id,
		Name:             cfg.Name,
		Group:            cfg.Group,
		IssueKey:         cfg.IssueKey,
		Branch:           cfg.Branch,
		BaseBranch:       cfg.BaseBranch,
		RepoName:         cfg.Repo.Name,
		WorktreePath:     dir,
		WorkingDirectory: dir,
		TmuxSession:      "tmux-" + id,
		AgentName:        agent,
		Backend:          domain.BackendTmux,
		Status:           domain.SessionStatusActive,
		CreatedAt:        time.Now().UTC(),
	}, nil
}
