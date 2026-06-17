package local

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/bouwerp/aiman/internal/domain"
)

type Executor struct {
	root string
}

func NewExecutor(root string) *Executor {
	return &Executor{root: root}
}

func (e *Executor) Connect(ctx context.Context) error { return nil }
func (e *Executor) GetRoot() string                   { return e.root }

func (e *Executor) Execute(ctx context.Context, cmdStr string) (string, error) {
	cmd := exec.CommandContext(ctx, "bash", "-l", "-c", cmdStr)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (e *Executor) WriteFile(ctx context.Context, path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0644)
}

func (e *Executor) ValidateDir(ctx context.Context, path string) error     { return nil }
func (e *Executor) ScanTmuxSessions(ctx context.Context) ([]string, error) { return nil, nil }
func (e *Executor) ScanGitRepos(ctx context.Context) ([]string, error)     { return nil, nil }
func (e *Executor) ScanWorktrees(ctx context.Context, repoPath string) ([]string, error) {
	return nil, nil
}
func (e *Executor) GetGitRoot(ctx context.Context, path string) (string, error) { return "", nil }
func (e *Executor) GetTmuxSessionCWD(ctx context.Context, sessionName string) (string, error) {
	return "", nil
}
func (e *Executor) GetTmuxSessionEnv(ctx context.Context, sessionName, envVar string) (string, error) {
	return "", nil
}

func (e *Executor) CaptureTmuxPane(ctx context.Context, sessionName string) (string, error) {
	cmdStr := "tmux capture-pane -p -t " + sessionName + " -S -100"
	return e.Execute(ctx, cmdStr)
}

func (e *Executor) AttachTmuxSession(sessionName string) *exec.Cmd { return nil }
func (e *Executor) StreamTmuxSession(ctx context.Context, sessionName string) (io.ReadWriteCloser, error) {
	return nil, nil
}
func (e *Executor) StartTmuxSession(ctx context.Context, name string) error { return nil }
func (e *Executor) ProvisionRemote(ctx context.Context, steps []domain.ProvisionStep, progress chan<- domain.ProvisionProgress) error {
	return nil
}
func (e *Executor) Close() error { return nil }
