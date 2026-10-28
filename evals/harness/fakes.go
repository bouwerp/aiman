package harness

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/ptyruntime/ptyfake"
)

// FakeRemote implements the remote operations the server calls during a case.
// Everything else is embedded as nil: a case that reaches an unimplemented
// method fails loudly instead of pretending the remote did something.
//
// `aiman pty kill` and `aiman pty forget` go to PTY, the way the CLI reaches
// serve's runtime over the socket.
type FakeRemote struct {
	domain.RemoteExecutor
	Dir string
	PTY *ptyfake.FakePTY

	mu    sync.Mutex
	execs []string
}

func NewFakeRemote(dir string, pty *ptyfake.FakePTY) *FakeRemote {
	return &FakeRemote{Dir: dir, PTY: pty}
}

// ptyCLI matches the runtime calls KillPTYSession makes through the CLI. The
// id is Go-quoted.
var ptyCLI = regexp.MustCompile(`aiman pty (kill|forget) ("(?:[^"\\]|\\.)*")`)

func (r *FakeRemote) Execute(_ context.Context, cmd string) (string, error) {
	r.mu.Lock()
	r.execs = append(r.execs, cmd)
	r.mu.Unlock()
	m := ptyCLI.FindStringSubmatch(cmd)
	if m == nil || r.PTY == nil {
		return "", nil
	}
	id, err := strconv.Unquote(m[2])
	if err != nil {
		return "", err
	}
	if m[1] == "kill" {
		return "", r.PTY.Kill(id)
	}
	return "", r.PTY.Forget(id)
}

func (r *FakeRemote) GetRoot() string { return r.Dir }

func (r *FakeRemote) WriteFile(_ context.Context, path string, content []byte) error {
	return os.WriteFile(path, content, 0o600)
}

func (r *FakeRemote) ValidateDir(context.Context, string) error { return nil }

func (r *FakeRemote) ScanGitRepos(context.Context) ([]string, error) { return nil, nil }

func (r *FakeRemote) ScanWorktrees(context.Context, string) ([]string, error) { return nil, nil }

func (r *FakeRemote) GetGitRoot(context.Context, string) (string, error) { return "", nil }

func (r *FakeRemote) Close() error { return nil }

// Execs returns a copy of the commands Execute received.
func (r *FakeRemote) Execs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.execs...)
}

// FakeCreator returns a session without cloning a repo or starting an agent.
// With PTY set, the session's terminal is added to that runtime.
type FakeCreator struct {
	Dir string
	PTY *ptyfake.FakePTY
}

func (c FakeCreator) CreateSession(_ context.Context, cfg domain.SessionConfig) (*domain.Session, error) {
	id := uuid.NewString()
	dir := filepath.Join(c.Dir, "sessions", id)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, domain.AimanTaskFileName), []byte("# Task\n"), 0o600); err != nil {
		return nil, err
	}
	agent := ""
	if cfg.Agent != nil {
		agent = cfg.Agent.Command
	}
	sess := &domain.Session{
		ID:               id,
		Name:             cfg.Name,
		Group:            cfg.Group,
		IssueKey:         cfg.IssueKey,
		Branch:           cfg.Branch,
		BaseBranch:       cfg.BaseBranch,
		RepoName:         cfg.Repo.Name,
		WorktreePath:     dir,
		WorkingDirectory: dir,
		TerminalName:     "term-" + id,
		AgentName:        agent,
		Backend:          domain.BackendPTY,
		Status:           domain.SessionStatusActive,
		CreatedAt:        time.Now().UTC(),
	}
	if c.PTY != nil {
		c.PTY.Add(ptyruntime.SessionInfo{ID: id, Name: sess.TerminalName, Dir: dir, Command: agent})
	}
	return sess, nil
}
