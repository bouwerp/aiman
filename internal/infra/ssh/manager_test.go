package ssh

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	commandLookPath = func(string) (string, error) {
		return "", errors.New("not found")
	}
	os.Exit(m.Run())
}

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
func TestBulkSSHArgsCompressesWithoutX11(t *testing.T) {
	args := strings.Join(bulkSSHArgs("/tmp/sock", true), " ")
	if !strings.Contains(args, "Compression=yes") || !strings.Contains(args, "ServerAliveInterval=30") {
		t.Fatalf("bulk ssh should compress and keep a 30s alive interval: %s", args)
	}
	if strings.Contains(args, "-X") {
		t.Fatalf("bulk ssh must not forward X11: %s", args)
	}
}

func TestAttachTmuxSessionKeepsAnUncompressedTTYMaster(t *testing.T) {
	mgr := NewManager(Config{Host: "example.com", User: "code"})
	args := strings.Join(mgr.AttachTmuxSession("feature-x").Args, " ")
	if strings.Contains(args, "Compression=yes") || !strings.Contains(args, "-tty") || !strings.Contains(args, " -X ") {
		t.Fatalf("tmux attach should be uncompressed X11 on its own master: %s", args)
	}
}

func TestAttachPTYSessionSkipsX11(t *testing.T) {
	mgr := NewManager(Config{Host: "example.com", User: "code"})
	args := strings.Join(mgr.AttachPTYSession("sess").Args, " ")
	if strings.Contains(args, " -X ") || strings.Contains(args, "Compression=yes") {
		t.Fatalf("pty attach should stay uncompressed and without X11: %s", args)
	}
}

func TestAttachTmuxSessionUsesMoshWhenPresent(t *testing.T) {
	prev := commandLookPath
	commandLookPath = func(name string) (string, error) {
		if name == "mosh" {
			return "/usr/bin/mosh", nil
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() { commandLookPath = prev })

	mgr := NewManager(Config{Host: "example.com", User: "code"})
	args := strings.Join(mgr.AttachTmuxSession("feature-x").Args, " ")
	if !strings.Contains(args, "/usr/bin/mosh code@example.com -- sh -c") || !strings.Contains(args, "exec tmux attach") {
		t.Fatalf("mosh attach: %s", args)
	}
}

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
