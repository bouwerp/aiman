package usecase

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
)

// restartRemote fakes the PTY runtime and serve RPCs a restart handoff uses.
// pane is what `aiman pty capture` shows; prompts holds every file written.
type restartRemote struct {
	commands             []string
	prompts              []string
	pane                 string
	fileExists           bool
	sendPromptWritesFile bool
	failOn               string // command substring that triggers a hard error
	ptyRunning           bool
	rpcOut               map[string]string // rpc method → stdout for that call
}

func (r *restartRemote) Connect(context.Context) error { return nil }
func (r *restartRemote) GetRoot() string               { return "" }
func (r *restartRemote) Execute(_ context.Context, cmd string) (string, error) {
	r.commands = append(r.commands, cmd)
	if r.failOn != "" && strings.Contains(cmd, r.failOn) {
		return "", fmt.Errorf("simulated remote failure for %q", r.failOn)
	}
	switch {
	case strings.HasPrefix(cmd, "rm -f "):
		r.fileExists = false
		return "", nil
	case strings.Contains(cmd, "aiman pty capture"):
		return r.pane, nil
	case strings.Contains(cmd, "aiman pty input") && strings.Contains(cmd, "--file"):
		if r.sendPromptWritesFile && r.summaryPrompted() {
			r.fileExists = true
		}
		return "", nil
	case strings.HasPrefix(cmd, "if [ -f "):
		if r.fileExists {
			return "1", nil
		}
		return "", nil
	case strings.Contains(cmd, "aiman rpc handover.write"):
		return r.rpcOut["handover.write"], nil
	case strings.Contains(cmd, "aiman rpc handover.point"):
		return r.rpcOut["handover.point"], nil
	case strings.Contains(cmd, "aiman pty get"):
		if r.ptyRunning {
			return `{"session":{"status":"running"}}`, nil
		}
		return "", nil
	default:
		return "", nil
	}
}
func (r *restartRemote) WriteFile(_ context.Context, _ string, content []byte) error {
	r.prompts = append(r.prompts, string(content))
	return nil
}

// summaryPrompted reports whether the agent was asked for a restart summary.
func (r *restartRemote) summaryPrompted() bool {
	for _, p := range r.prompts {
		if strings.Contains(p, "SESSION_SUMMARY_SAVED") {
			return true
		}
	}
	return false
}
func (r *restartRemote) ValidateDir(context.Context, string) error { return nil }
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
