package usecase

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

type restartRemote struct {
	commands             []string
	paneCommand          string
	fileExists           bool
	sendPromptWritesFile bool
}

func (r *restartRemote) Connect(context.Context) error { return nil }
func (r *restartRemote) GetRoot() string               { return "" }
func (r *restartRemote) Execute(_ context.Context, cmd string) (string, error) {
	r.commands = append(r.commands, cmd)
	switch {
	case strings.HasPrefix(cmd, "rm -f "):
		r.fileExists = false
		return "", nil
	case strings.Contains(cmd, "#{pane_current_command}"):
		return r.paneCommand, nil
	case strings.Contains(cmd, "SESSION_SUMMARY_SAVED"):
		if r.sendPromptWritesFile {
			r.fileExists = true
		}
		return "", nil
	case strings.HasPrefix(cmd, "if [ -f "):
		if r.fileExists {
			return "1", nil
		}
		return "", nil
	default:
		return "", nil
	}
}
func (r *restartRemote) WriteFile(context.Context, string, []byte) error { return nil }
func (r *restartRemote) ValidateDir(context.Context, string) error       { return nil }
func (r *restartRemote) ScanTmuxSessions(context.Context) ([]string, error) {
	return nil, nil
}
func (r *restartRemote) ScanGitRepos(context.Context) ([]string, error) { return nil, nil }
func (r *restartRemote) ScanWorktrees(context.Context, string) ([]string, error) {
	return nil, nil
}
func (r *restartRemote) GetGitRoot(context.Context, string) (string, error) { return "", nil }
func (r *restartRemote) GetTmuxSessionCWD(context.Context, string) (string, error) {
	return "", nil
}
func (r *restartRemote) GetTmuxSessionEnv(context.Context, string, string) (string, error) {
	return "", nil
}
func (r *restartRemote) CaptureTmuxPane(context.Context, string) (string, error) { return "", nil }
func (r *restartRemote) AttachTmuxSession(string) *exec.Cmd                      { return nil }
func (r *restartRemote) StreamTmuxSession(context.Context, string) (io.ReadWriteCloser, error) {
	return nil, nil
}
func (r *restartRemote) StartTmuxSession(context.Context, string) error { return nil }
func (r *restartRemote) ProvisionRemote(context.Context, []domain.ProvisionStep, chan<- domain.ProvisionProgress) error {
	return nil
}
func (r *restartRemote) Close() error { return nil }

func TestCaptureRestartSessionSummary_WritesHandoffAndInterruptsAgent(t *testing.T) {
	remote := &restartRemote{paneCommand: "node", sendPromptWritesFile: true}

	oldInterval := restartSummaryPollInterval
	restartSummaryPollInterval = time.Millisecond
	defer func() { restartSummaryPollInterval = oldInterval }()

	ok, err := CaptureRestartSessionSummary(context.Background(), remote, "session-1", "/work/.aiman_session_summary.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected summary capture to succeed")
	}

	joined := strings.Join(remote.commands, "\n")
	if !strings.Contains(joined, "SESSION_SUMMARY_SAVED") {
		t.Fatalf("expected summary prompt to be injected, got commands:\n%s", joined)
	}
	if !strings.Contains(joined, "C-c") {
		t.Fatalf("expected Ctrl+C after summary capture, got commands:\n%s", joined)
	}
}

func TestCaptureRestartSessionSummary_SkipsShellPane(t *testing.T) {
	remote := &restartRemote{paneCommand: "bash", sendPromptWritesFile: true}

	ok, err := CaptureRestartSessionSummary(context.Background(), remote, "session-1", "/work/.aiman_session_summary.md")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected shell pane to skip restart summary capture")
	}

	joined := strings.Join(remote.commands, "\n")
	if strings.Contains(joined, "SESSION_SUMMARY_SAVED") {
		t.Fatalf("did not expect summary prompt for shell pane, got commands:\n%s", joined)
	}
	if strings.Contains(joined, "C-c") {
		t.Fatalf("did not expect Ctrl+C for shell pane, got commands:\n%s", joined)
	}
}

func TestCaptureRestartSessionSummary_TimeoutFallsBackWithoutError(t *testing.T) {
	remote := &restartRemote{paneCommand: "node"}

	oldInterval := restartSummaryPollInterval
	restartSummaryPollInterval = time.Millisecond
	defer func() { restartSummaryPollInterval = oldInterval }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	ok, err := CaptureRestartSessionSummary(ctx, remote, "session-1", "/work/.aiman_session_summary.md")
	if err != nil {
		t.Fatalf("expected timeout fallback without error, got %v", err)
	}
	if ok {
		t.Fatal("expected timeout path to continue without a summary")
	}
}
