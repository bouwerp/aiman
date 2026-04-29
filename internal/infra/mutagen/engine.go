package mutagen

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

type Engine struct{}

func NewEngine() *Engine {
	return &Engine{}
}

func (e *Engine) StartSync(ctx context.Context, name, localPath, remotePath string, labels map[string]string, mode domain.SyncMode) error {
	// mutagen sync create --name <id> localPath remotePath
	if localPath == "" || remotePath == "" {
		return fmt.Errorf("invalid sync paths")
	}

	if err := os.MkdirAll(localPath, 0755); err != nil {
		return fmt.Errorf("failed to create local sync directory: %w", err)
	}

	args := []string{"sync", "create", "--name", name}
	if mode != "" {
		args = append(args, "--mode", string(mode))
	}
	for k, v := range labels {
		// Label values must be no more than 63 characters and contain only alphanumeric, hyphens, and underscores.
		labelValue := v
		reg := regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
		labelValue = reg.ReplaceAllString(labelValue, "-")
		if len(labelValue) > 63 {
			labelValue = labelValue[:63]
		}
		labelValue = strings.Trim(labelValue, "-_")
		args = append(args, "--label", fmt.Sprintf("%s=%s", k, labelValue))
	}

	args = append(args, localPath, remotePath)

	// Use a reasonable timeout for the command execution itself to avoid hanging if the daemon is stuck
	cmdCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "mutagen", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create mutagen sync: %w, output: %s", err, string(output))
	}
	return nil
}

func (e *Engine) StopSync(ctx context.Context) error {
	return nil
}

func (e *Engine) TerminateSync(ctx context.Context, name string) {
	if name == "" {
		return
	}
	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = exec.CommandContext(cmdCtx, "mutagen", "sync", "terminate", name).Run() // #nosec G204
}

func (e *Engine) GetStatus(ctx context.Context) (string, error) {
	return "", nil
}

func (e *Engine) ListSyncSessions(ctx context.Context) ([]domain.SyncSession, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "mutagen", "sync", "list")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to list mutagen sessions: %w, output: %s", err, string(output))
	}

	sessions := e.parseSyncListOutput(string(output))
	postProcessMutagenSessions(sessions)
	return sessions, nil
}

func (e *Engine) GetSyncStatus(ctx context.Context, name string) (string, error) {
	if name == "" {
		return "", nil
	}
	sessions, err := e.ListSyncSessions(ctx)
	if err != nil {
		return "", err
	}
	for _, s := range sessions {
		if s.Name == name || s.ID == name {
			return s.Status, nil
		}
	}
	return "", fmt.Errorf("sync session %q not found", name)
}

func postProcessMutagenSessions(sessions []domain.SyncSession) {
	for i := range sessions {
		s := &sessions[i]
		// Ensure RemotePath is the one with the colon
		if !strings.Contains(s.RemotePath, ":") && strings.Contains(s.LocalPath, ":") {
			s.LocalPath, s.RemotePath = s.RemotePath, s.LocalPath
		}
		// Strip user@host: prefix from RemotePath only for SSH-style URLs (must contain @).
		// Avoid splitting on colons in ordinary paths or schemes like https://.
		if parts := strings.SplitN(s.RemotePath, ":", 2); len(parts) > 1 && strings.Contains(parts[0], "@") {
			s.RemoteEndpoint = parts[0]
			s.RemotePath = parts[1]
		}
	}
}

func (e *Engine) parseSyncListOutput(output string) []domain.SyncSession {
	var sessions []domain.SyncSession
	var current *domain.SyncSession
	var inLabels bool

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
			inLabels = false
			continue
		}

		if current == nil {
			continue
		}

		switch {
		case strings.HasPrefix(line, "Identifier:"):
			current.ID = strings.TrimSpace(strings.TrimPrefix(line, "Identifier:"))
			inLabels = false
		case strings.HasPrefix(line, "Labels:"):
			inLabels = true
			current.Labels = make(map[string]string)
		case strings.HasPrefix(line, "Alpha:"), strings.HasPrefix(line, "Beta:"):
			inLabels = false
		case strings.HasPrefix(line, "URL:"):
			url := strings.TrimSpace(strings.TrimPrefix(line, "URL:"))
			if current.LocalPath == "" {
				current.LocalPath = url
			} else {
				current.RemotePath = url
			}
			inLabels = false
		case strings.HasPrefix(line, "Status:"):
			current.Status = strings.TrimSpace(strings.TrimPrefix(line, "Status:"))
			inLabels = false
		default:
			if inLabels && strings.Contains(line, ":") {
				parts := strings.SplitN(line, ":", 2)
				key := strings.TrimSpace(parts[0])
				value := strings.TrimSpace(parts[1])
				current.Labels[key] = value
			}
		}
	}

	if current != nil {
		sessions = append(sessions, *current)
	}

	return sessions
}
