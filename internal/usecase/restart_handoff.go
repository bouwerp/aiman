package usecase

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

var shellCommands = map[string]struct{}{
	"bash": {},
	"fish": {},
	"sh":   {},
	"zsh":  {},
}

var restartSummaryPollInterval = 2 * time.Second

// CaptureRestartSessionSummary asks the currently running agent (if any) to write a
// restart handoff file, waits for it to appear, then sends Ctrl+C so the new agent
// can start from a clean pane. It returns false when the tmux pane is already at a
// shell prompt or no tmux session is available.
func CaptureRestartSessionSummary(ctx context.Context, remote domain.RemoteExecutor, tmuxSession, summaryPath string) (bool, error) {
	tempPath := summaryPath + ".tmp"
	if _, err := remote.Execute(ctx, fmt.Sprintf("rm -f %q %q", summaryPath, tempPath)); err != nil {
		return false, err
	}

	paneCommand, err := currentPaneCommand(ctx, remote, tmuxSession)
	if err != nil {
		return false, err
	}
	if isShellCommand(paneCommand) {
		return false, nil
	}

	prompt := restartSummaryPrompt(summaryPath, tempPath)
	sendCmd := fmt.Sprintf(
		"if tmux has-session -t %q 2>/dev/null; then tmux send-keys -t %q -l %q && sleep 1 && tmux send-keys -t %q Enter; fi",
		tmuxSession, tmuxSession, prompt, tmuxSession,
	)
	if _, err := remote.Execute(ctx, sendCmd); err != nil {
		return false, err
	}

	written, err := waitForRemoteFile(ctx, remote, summaryPath, restartSummaryPollInterval)
	if err != nil {
		if ctx.Err() != nil {
			_, _ = remote.Execute(context.Background(), fmt.Sprintf("if tmux has-session -t %q 2>/dev/null; then tmux send-keys -t %q C-c; fi", tmuxSession, tmuxSession))
			return false, nil
		}
		return false, err
	}
	if !written {
		return false, nil
	}

	if _, err := remote.Execute(ctx, fmt.Sprintf("if tmux has-session -t %q 2>/dev/null; then tmux send-keys -t %q C-c; fi", tmuxSession, tmuxSession)); err != nil {
		return true, err
	}
	return true, nil
}

func currentPaneCommand(ctx context.Context, remote domain.RemoteExecutor, tmuxSession string) (string, error) {
	out, err := remote.Execute(ctx, fmt.Sprintf("tmux display-message -p -t %q '#{pane_current_command}' 2>/dev/null || true", tmuxSession))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func waitForRemoteFile(ctx context.Context, remote domain.RemoteExecutor, path string, interval time.Duration) (bool, error) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		exists, err := remoteFileExists(ctx, remote, path)
		if err != nil {
			return false, err
		}
		if exists {
			return true, nil
		}

		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-ticker.C:
		}
	}
}

func remoteFileExists(ctx context.Context, remote domain.RemoteExecutor, path string) (bool, error) {
	out, err := remote.Execute(ctx, fmt.Sprintf("if [ -f %q ]; then printf 1; fi", path))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "1", nil
}

func isShellCommand(cmd string) bool {
	cmd = strings.TrimSpace(strings.ToLower(filepath.Base(cmd)))
	if cmd == "" {
		return true
	}
	_, ok := shellCommands[cmd]
	return ok
}

func restartSummaryPrompt(summaryPath, tempPath string) string {
	return fmt.Sprintf(
		"Before this session is restarted, write a concise markdown handoff to `%s`. Include completed work, files changed, current state, blockers, and the exact next steps for the next agent. Write to `%s` first, then rename it atomically to `%s` when complete. Do not print the summary in chat. When the file is fully written, respond only with SESSION_SUMMARY_SAVED.",
		summaryPath, tempPath, summaryPath,
	)
}
