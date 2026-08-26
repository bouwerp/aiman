package ssh

import (
	"strings"
	"testing"
)

func TestTmuxAttachRemoteCommand_EnablesMouseBeforeAttach(t *testing.T) {
	cmd := tmuxAttachRemoteCommand("feature-x")
	if !strings.Contains(cmd, `tmux set-option -t "feature-x" mouse on`) {
		t.Fatalf("expected session mouse enable, got %q", cmd)
	}
	if !strings.Contains(cmd, `exec tmux attach -t "feature-x"`) {
		t.Fatalf("expected tmux attach command, got %q", cmd)
	}
}

func TestAttachTmuxSession_UsesMouseEnablingWrapper(t *testing.T) {
	mgr := NewManager(Config{Host: "example.com", User: "code"})
	cmd := mgr.AttachTmuxSession("feature-x")

	args := strings.Join(cmd.Args, " ")
	if !strings.Contains(args, `tmux set-option -t "feature-x" mouse on`) {
		t.Fatalf("expected ssh command to enable tmux mouse support, got %q", args)
	}
	if !strings.Contains(args, `exec tmux attach -t "feature-x"`) {
		t.Fatalf("expected ssh command to attach tmux session, got %q", args)
	}
}

// A missing tmux server is "no sessions"; anything else from `tmux ls` is a
// real failure that must propagate so a failed scan is never read as a host
// with zero sessions.
func TestIsTmuxNoServerError(t *testing.T) {
	noServer := []string{
		"remote command failed on code@regent0: exit status 1\nOutput: no server running on /tmp/tmux-1000/default",
		"remote command failed on code@regent0: exit status 1\nOutput: error connecting to /tmp/tmux-1000/default (No such file or directory)",
	}
	for _, s := range noServer {
		if !isTmuxNoServerError(s) {
			t.Errorf("expected no-server classification for %q", s)
		}
	}

	realFailures := []string{
		"remote command failed on code@regent0: exit status 255\nOutput: ssh: connect to host regent0 port 22: Operation timed out",
		"remote command failed on code@regent0: signal: killed\nOutput: ",
	}
	for _, s := range realFailures {
		if isTmuxNoServerError(s) {
			t.Errorf("expected real-failure classification for %q", s)
		}
	}
}

// Fitting a session to the dashboard's preview panel switches its tmux window
// to manual sizing. Attaching has to hand that control back, or the window would
// stay at the panel's width for a full-screen client and tmux would never fit it
// to the terminal being attached.
func TestTmuxAttachRemoteCommand_RestoresAutomaticSizing(t *testing.T) {
	cmd := tmuxAttachRemoteCommand("feature-x")
	if !strings.Contains(cmd, `tmux set-option -t "feature-x" window-size latest`) {
		t.Fatalf("attach must restore automatic window sizing, got %q", cmd)
	}
	// It has to happen before the attach, since exec replaces the shell.
	if strings.Index(cmd, "window-size latest") > strings.Index(cmd, "exec tmux attach") {
		t.Fatalf("window-size must be restored before exec, got %q", cmd)
	}
}
