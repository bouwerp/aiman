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
