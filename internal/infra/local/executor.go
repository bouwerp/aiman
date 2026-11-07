package local

import (
	"context"
	"fmt"
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

// WriteFile treats a leading $HOME as the SSH executor does, where the remote
// shell expands it, so one path works on both.
func (e *Executor) WriteFile(ctx context.Context, path string, content []byte) error {
	path, err := expandHome(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0600)
}

func expandHome(path string) (string, error) {
	rest, ok := strings.CutPrefix(path, "$HOME/")
	if !ok {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	return filepath.Join(home, rest), nil
}

func (e *Executor) ValidateDir(ctx context.Context, path string) error { return nil }
func (e *Executor) ScanGitRepos(ctx context.Context) ([]string, error) { return nil, nil }
func (e *Executor) ScanWorktrees(ctx context.Context, repoPath string) ([]string, error) {
	return nil, nil
}
func (e *Executor) GetGitRoot(ctx context.Context, path string) (string, error) { return "", nil }

func (e *Executor) ProvisionRemote(ctx context.Context, steps []domain.ProvisionStep, progress chan<- domain.ProvisionProgress) error {
	return nil
}
func (e *Executor) Close() error { return nil }
