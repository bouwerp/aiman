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

// CaptureRestartSessionSummaryBestEffort wraps CaptureRestartSessionSummary so a
// restart is never blocked by a handoff that can't be captured. Any error —
// not only the context-timeout case CaptureRestartSessionSummary already
// handles gracefully — is swallowed and reported back as a short note for
// the caller to log; the restart itself always proceeds without a handoff.
func CaptureRestartSessionSummaryBestEffort(ctx context.Context, remote domain.RemoteExecutor, tmuxSession, summaryPath string) (created bool, note string) {
	created, err := CaptureRestartSessionSummary(ctx, remote, tmuxSession, summaryPath)
	if err != nil {
		return false, fmt.Sprintf("restart handoff not captured: %v", err)
	}
	return created, ""
}

// CaptureSessionHandoffBestEffort captures a restart handoff for either
// backend, giving the built-in PTY path the same "never block a restart"
// guarantee the tmux path has: any error becomes a note for the caller to
// log, and the restart proceeds without a handoff.
func CaptureSessionHandoffBestEffort(ctx context.Context, remote domain.RemoteExecutor, session domain.Session, summaryPath string) (created bool, note string) {
	var err error
	if session.IsPTY() {
		created, err = CaptureRestartSessionSummaryPTY(ctx, remote, session.ID, summaryPath)
	} else {
		created, err = CaptureRestartSessionSummary(ctx, remote, session.TmuxSession, summaryPath)
	}
	if err != nil {
		return false, fmt.Sprintf("restart handoff not captured: %v", err)
	}
	return created, ""
}

func currentPaneCommand(ctx context.Context, remote domain.RemoteExecutor, tmuxSession string) (string, error) {
	out, err := remote.Execute(ctx, fmt.Sprintf("tmux display-message -p -t %q '#{pane_current_command}' 2>/dev/null || true", tmuxSession))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func waitForRemoteFile(ctx context.Context, remote TerminalExecutor, path string, interval time.Duration) (bool, error) {
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

func remoteFileExists(ctx context.Context, remote TerminalExecutor, path string) (bool, error) {
	out, err := remote.Execute(ctx, fmt.Sprintf("if [ -f %q ]; then printf 1; fi", path))
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "1", nil
}

func isShellCommand(cmd string) bool {
	// Emptiness must be decided before filepath.Base: Base("") returns ".",
	// which never matched the empty check below — so a nonexistent tmux
	// session (pane probe returns "") was treated as a running agent and the
	// caller sat out its whole handoff timeout waiting on a file no one
	// could ever write. Reviving a worktree hits exactly that case.
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return true
	}
	_, ok := shellCommands[strings.ToLower(filepath.Base(cmd))]
	return ok
}

func restartSummaryPrompt(summaryPath, tempPath string) string {
	return fmt.Sprintf(
		"Before this session is restarted, write a concise markdown handoff to `%s`. Include completed work, files changed, current state, blockers, and the exact next steps for the next agent. Write to `%s` first, then rename it atomically to `%s` when complete. Do not print the summary in chat. When the file is fully written, respond only with SESSION_SUMMARY_SAVED.",
		summaryPath, tempPath, summaryPath,
	)
}

// CaptureRestartSessionSummaryPTY is the built-in-PTY variant of
// CaptureRestartSessionSummary: it cannot ask tmux for the foreground command,
// so an empty pane stands in for "already at a shell", and interrupt/prompt go
// through `aiman pty input`.
func CaptureRestartSessionSummaryPTY(ctx context.Context, remote TerminalExecutor, sessionID, summaryPath string) (bool, error) {
	tempPath := summaryPath + ".tmp"
	if _, err := remote.Execute(ctx, fmt.Sprintf("rm -f %q %q", summaryPath, tempPath)); err != nil {
		return false, err
	}

	pane, err := CapturePTYPane(ctx, remote, sessionID, 40)
	if err != nil || strings.TrimSpace(pane) == "" {
		// No pane output at all: nothing live to hand off.
		return false, nil
	}

	prompt := restartSummaryPrompt(summaryPath, tempPath)
	promptPath := fmt.Sprintf("/tmp/aiman-prompt-%s", strings.TrimSpace(sessionID))
	if err := remote.WriteFile(ctx, promptPath, []byte(prompt)); err != nil {
		return false, err
	}
	if _, err := remote.Execute(ctx, remoteAimanPreamble+fmt.Sprintf(
		"sleep 1; aiman pty input %q --file %q >/dev/null 2>&1 && sleep 1 && aiman pty input %q --data '\\r'",
		strings.TrimSpace(sessionID), promptPath, strings.TrimSpace(sessionID))); err != nil {
		return false, err
	}

	written, err := waitForRemoteFile(ctx, remote, summaryPath, restartSummaryPollInterval)
	if ctx.Err() != nil {
		_, _ = remote.Execute(context.Background(), remoteAimanPreamble+fmt.Sprintf(
			"aiman pty input %q --data '\\x03'", strings.TrimSpace(sessionID)))
		return false, nil
	}
	if err != nil || !written {
		return false, err
	}
	if _, err := remote.Execute(ctx, remoteAimanPreamble+fmt.Sprintf(
		"aiman pty input %q --data '\\x03'", strings.TrimSpace(sessionID))); err != nil {
		return true, err
	}
	return true, nil
}
