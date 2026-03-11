package mutagen

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	
	// Label values must be no more than 63 characters and contain only alphanumeric, hyphens, and underscores.
	// We use the name as the base but sanitize it.
	labelValue := name
	reg := regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
	labelValue = reg.ReplaceAllString(labelValue, "-")
	if len(labelValue) > 63 {
		labelValue = labelValue[:63]
	}
	// Ensure it doesn't start or end with a hyphen/underscore if needed (Mutagen might be picky)
	labelValue = strings.Trim(labelValue, "-_")
	
	cmd := exec.CommandContext(ctx, "mutagen", "sync", "create", "--name", name, "--label", "session="+labelValue, localPath, remotePath)
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

	sessions := e.parseSyncListOutput(string(output))

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

func (e *Engine) parseSyncListOutput(output string) []domain.SyncSession {
	var sessions []domain.SyncSession
	var current *domain.SyncSession

	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "-") {
			continue
		}

		if strings.HasPrefix(line, "Name:") {
			if current != nil {
				sessions = append(sessions, *current)
			}
			current = &domain.SyncSession{
				Name: strings.TrimSpace(strings.TrimPrefix(line, "Name:")),
			}
			continue
		}

		if current == nil {
			continue
		}

		switch {
		case strings.HasPrefix(line, "Identifier:"):
			current.ID = strings.TrimSpace(strings.TrimPrefix(line, "Identifier:"))
		case strings.HasPrefix(line, "URL:"):
			url := strings.TrimSpace(strings.TrimPrefix(line, "URL:"))
			if current.LocalPath == "" {
				current.LocalPath = url
			} else {
				current.RemotePath = url
			}
		case strings.HasPrefix(line, "Status:"):
			current.Status = strings.TrimSpace(strings.TrimPrefix(line, "Status:"))
		}
	}

	if current != nil {
		sessions = append(sessions, *current)
	}

	return sessions
}
