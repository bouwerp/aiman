package local

import (
	"context"
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
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0600)
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
