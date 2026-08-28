package usecase

import (
	"context"
	"strings"
	"testing"
)

// The trust dialog is a select list: characters it does not recognise act as
// menu input, and a prompt typed into it selects "No, quit" — the agent exits 0
// with an empty pane and never touches the worktree. So the dialog has to be
// cleared before any prompt text is typed.
func TestDeliverInitialPromptPTYClearsWorkspaceTrustBeforeTyping(t *testing.T) {
	r := &recordingRemote{}
	DeliverInitialPromptPTY(context.Background(), r, "sess-1", "review PR 439 and fix 2 bugs")
	cmd := r.joined()

	trustAt := strings.Index(cmd, "trust")
	if trustAt < 0 {
		t.Fatalf("no workspace-trust check in the delivery script: %s", cmd)
	}
	typeAt := strings.Index(cmd, "--file")
	if typeAt < 0 {
		t.Fatalf("the prompt is never typed: %s", cmd)
	}
	if trustAt > typeAt {
		t.Errorf("the prompt is typed before the trust dialog is cleared: %s", cmd)
	}
}

// An ad-hoc session carries no prompt, but an unanswered dialog still leaves the
// agent parked on a menu instead of ready for work.
func TestDeliverInitialPromptPTYClearsWorkspaceTrustWithoutAPrompt(t *testing.T) {
	r := &recordingRemote{}
	DeliverInitialPromptPTY(context.Background(), r, "sess-1", "")
	cmd := r.joined()
	if !strings.Contains(cmd, "trust") {
		t.Fatalf("an empty prompt skipped the trust dialog entirely: %s", cmd)
	}
	if strings.Contains(cmd, "--file") {
		t.Errorf("nothing should be typed when there is no prompt: %s", cmd)
	}
}

// The dialog's default is "Yes, continue" in every agent that asks, so Return
// accepts it. Anything else is menu input.
func TestAcceptWorkspaceTrustPTYPressesReturn(t *testing.T) {
	script := acceptWorkspaceTrustPTY("sess-1")
	if !strings.Contains(script, `aiman pty input "sess-1" --key enter`) {
		t.Errorf("trust acceptance must press Return: %s", script)
	}
	if strings.Contains(script, "--data") {
		t.Errorf("trust acceptance must not type text into a menu: %s", script)
	}
}

func TestDeliverInitialPromptPTYIgnoresAnEmptySessionID(t *testing.T) {
	r := &recordingRemote{}
	DeliverInitialPromptPTY(context.Background(), r, "  ", "do the thing")
	if len(r.cmds) != 0 {
		t.Errorf("nothing should be sent without a session id: %v", r.cmds)
	}
}
