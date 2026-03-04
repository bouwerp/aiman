package mutagen

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
)

type Engine struct{}

func NewEngine() *Engine {
	return &Engine{}
}

func (e *Engine) StartSync(ctx context.Context, localPath, remotePath string) error {
	// mutagen sync create --name <id> localPath remotePath
	if localPath == "" || remotePath == "" {
		return fmt.Errorf("invalid sync paths")
	}

	if err := os.MkdirAll(localPath, 0755); err != nil {
		return fmt.Errorf("failed to create local sync directory: %w", err)
	}

	name := filepath.Base(localPath)
	cmd := exec.CommandContext(ctx, "mutagen", "sync", "create", "--name", name, localPath, remotePath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create mutagen sync: %w, output: %s", err, string(output))
	}
	return nil
}

func (e *Engine) StopSync(ctx context.Context) error {
	return nil
}

func (e *Engine) GetStatus(ctx context.Context) (string, error) {
	return "", nil
}

func (e *Engine) ListSyncSessions(ctx context.Context) ([]domain.SyncSession, error) {
	cmd := exec.CommandContext(ctx, "mutagen", "sync", "list")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, nil
	}

	var sessions []domain.SyncSession
	var current *domain.SyncSession

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "-") {
			continue
		}

		if strings.HasPrefix(line, "Identifier:") {
			if current != nil {
				sessions = append(sessions, *current)
			}
			current = &domain.SyncSession{
				ID: strings.TrimSpace(strings.TrimPrefix(line, "Identifier:")),
			}
			continue
		}

		if current == nil {
			continue
		}

		if strings.HasPrefix(line, "URL:") {
			url := strings.TrimSpace(strings.TrimPrefix(line, "URL:"))
			if current.LocalPath == "" {
				current.LocalPath = url
			} else {
				current.RemotePath = url
			}
		}

		if strings.HasPrefix(line, "Status:") {
			current.Status = strings.TrimSpace(strings.TrimPrefix(line, "Status:"))
		}
	}

	if current != nil {
		sessions = append(sessions, *current)
	}

	// Post-process to fix Local vs Remote paths
	for i := range sessions {
		s := &sessions[i]
		// Ensure RemotePath is the one with the colon
		if !strings.Contains(s.RemotePath, ":") && strings.Contains(s.LocalPath, ":") {
			s.LocalPath, s.RemotePath = s.RemotePath, s.LocalPath
		}
		// Strip connection prefix from RemotePath
		if parts := strings.SplitN(s.RemotePath, ":", 2); len(parts) > 1 {
			s.RemotePath = parts[1]
		}
	}

	return sessions, nil
}
