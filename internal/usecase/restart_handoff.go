package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

var restartSummaryPollInterval = 2 * time.Second

// CaptureSessionHandoffBestEffort asks the session's agent for a restart
// handoff without ever blocking the restart: any error becomes a note for the
// caller to log, and the restart proceeds without a handoff. A legacy tmux
// row is never asked; its note carries the stop command.
func CaptureSessionHandoffBestEffort(ctx context.Context, remote TerminalExecutor, session domain.Session, summaryPath string) (created bool, note string) {
	if session.IsLegacyTmux() {
		return false, fmt.Sprintf("restart handoff not captured: %v", domain.NewLegacyTmuxError(session))
	}
	created, err := CaptureRestartSessionSummaryPTY(ctx, remote, session.ID, summaryPath)
	if err != nil {
		return false, fmt.Sprintf("restart handoff not captured: %v", err)
	}
	return created, ""
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

func restartSummaryPrompt(summaryPath, tempPath string) string {
	return fmt.Sprintf(
		"Before this session is restarted, write a concise markdown handoff to `%s`. Include completed work, files changed, current state, blockers, and the exact next steps for the next agent. Write to `%s` first, then rename it atomically to `%s` when complete. Do not print the summary in chat. When the file is fully written, respond only with SESSION_SUMMARY_SAVED.",
		summaryPath, tempPath, summaryPath,
	)
}

// CaptureRestartSessionSummaryPTY asks the running agent to write a restart
// handoff file, waits for it to appear, then sends Ctrl+C so the next agent
// starts from a clean pane. An empty pane means nothing live to hand off.
// Prompt and interrupt go through `aiman pty input`.
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
		"sleep 1; aiman pty input %q --file %q >/dev/null 2>&1 && sleep 1 && aiman pty input %q --key enter",
		strings.TrimSpace(sessionID), promptPath, strings.TrimSpace(sessionID))); err != nil {
		return false, err
	}

	written, err := waitForRemoteFile(ctx, remote, summaryPath, restartSummaryPollInterval)
	if ctx.Err() != nil {
		_, _ = remote.Execute(context.Background(), remoteAimanPreamble+fmt.Sprintf(
			"aiman pty input %q --key ctrl-c", strings.TrimSpace(sessionID)))
		return false, nil
	}
	if err != nil || !written {
		return false, err
	}
	if _, err := remote.Execute(ctx, remoteAimanPreamble+fmt.Sprintf(
		"aiman pty input %q --key ctrl-c", strings.TrimSpace(sessionID))); err != nil {
		return true, err
	}
	return true, nil
}
