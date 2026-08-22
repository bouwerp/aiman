package local

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0600)
}

func (e *Executor) ValidateDir(ctx context.Context, path string) error { return nil }
func (e *Executor) ScanTmuxSessions(ctx context.Context) ([]string, error) {
	out, err := e.Execute(ctx, "tmux list-sessions -F '#{session_name}' 2>/dev/null")
	if err != nil {
		return nil, nil
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}
func (e *Executor) ScanGitRepos(ctx context.Context) ([]string, error) { return nil, nil }
func (e *Executor) ScanWorktrees(ctx context.Context, repoPath string) ([]string, error) {
	return nil, nil
}
func (e *Executor) GetGitRoot(ctx context.Context, path string) (string, error) { return "", nil }
func (e *Executor) GetTmuxSessionEnv(ctx context.Context, sessionName, envVar string) (string, error) {
	out, err := e.Execute(ctx, fmt.Sprintf("tmux show-environment -t %q %q 2>/dev/null", sessionName, envVar))
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if i := strings.IndexByte(out, '='); i >= 0 {
		return out[i+1:], nil
	}
	return out, nil
}

func (e *Executor) GetTmuxSessionCWD(ctx context.Context, sessionName string) (string, error) {
	out, err := e.Execute(ctx, fmt.Sprintf("tmux display-message -p -t %q '#{pane_current_path}' 2>/dev/null", sessionName))
	return strings.TrimSpace(out), err
}

func (e *Executor) CaptureTmuxPane(ctx context.Context, sessionName string) (string, error) {
	return e.Execute(ctx, fmt.Sprintf("tmux capture-pane -p -t %q -S -100", sessionName))
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
