package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// recordingRemote captures the commands issued and replays canned output.
type recordingRemote struct {
	cmds []string
	out  string
	err  error
}

func (r *recordingRemote) Execute(_ context.Context, cmd string) (string, error) {
	r.cmds = append(r.cmds, cmd)
	return r.out, r.err
}

func (r *recordingRemote) WriteFile(_ context.Context, _ string, _ []byte) error { return nil }

func (r *recordingRemote) joined() string { return strings.Join(r.cmds, "\n") }

func TestResizeSessionTerminalPTYUsesTheRuntime(t *testing.T) {
	r := &recordingRemote{out: `{"type":"pty_resize"}`}
	s := domain.Session{ID: "abc", Backend: domain.BackendPTY}

	applied, err := ResizeSessionTerminal(context.Background(), r, s, 154, 40)
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	if !applied {
		t.Error("a PTY resize has no attached-client guard, so it always applies")
	}
	if !strings.Contains(r.joined(), `aiman pty resize "abc" --cols 154 --rows 40`) {
		t.Errorf("unexpected command: %s", r.joined())
	}
}

// tmux fits a window to its smallest attached client, so a resize against an
// automatically sized window is silently undone: manual sizing has to be set
// every time, since an attach resets it.
func TestResizeSessionTerminalTmuxSetsManualSizing(t *testing.T) {
	r := &recordingRemote{out: "AIMAN_FIT=applied\n"}
	s := domain.Session{ID: "abc", TmuxSession: "demo"}

	applied, err := ResizeSessionTerminal(context.Background(), r, s, 154, 40)
	if err != nil {
		t.Fatalf("resize: %v", err)
	}
	if !applied {
		t.Error("expected the resize to be reported as applied")
	}
	cmd := r.joined()
	for _, want := range []string{
		"window-size manual",
		"resize-window",
		"-x 154",
		"-y 40",
		"#{session_attached}",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command missing %q: %s", want, cmd)
		}
	}
	// One round trip: the guard and the resize cannot be split without racing.
	if len(r.cmds) != 1 {
		t.Errorf("expected a single remote command, got %d", len(r.cmds))
	}
}

// Someone watching an attached session must not have the window resized under
// them. That is a decision, not a failure, so it is reported as "not applied".
func TestResizeSessionTerminalLeavesAttachedSessionsAlone(t *testing.T) {
	r := &recordingRemote{out: "AIMAN_FIT=attached\n"}
	s := domain.Session{ID: "abc", TmuxSession: "demo"}

	applied, err := ResizeSessionTerminal(context.Background(), r, s, 154, 40)
	if err != nil {
		t.Fatalf("an attached session is not an error: %v", err)
	}
	if applied {
		t.Error("must not report a resize that was declined")
	}
}

func TestResizeSessionTerminalRejectsUnusableSizes(t *testing.T) {
	r := &recordingRemote{}
	s := domain.Session{ID: "abc", TmuxSession: "demo"}
	for _, sz := range [][2]int{{0, 40}, {154, 0}, {-1, -1}} {
		if _, err := ResizeSessionTerminal(context.Background(), r, s, sz[0], sz[1]); err == nil {
			t.Errorf("expected an error for %dx%d", sz[0], sz[1])
		}
	}
	if len(r.cmds) != 0 {
		t.Errorf("nothing should have been sent remotely: %v", r.cmds)
	}
}

func TestResizeSessionTerminalNeedsATmuxName(t *testing.T) {
	r := &recordingRemote{}
	if _, err := ResizeSessionTerminal(context.Background(), r, domain.Session{ID: "abc"}, 154, 40); err == nil {
		t.Error("a tmux session with no name cannot be resized")
	}
}

func TestResizeSessionTerminalPropagatesTransportErrors(t *testing.T) {
	r := &recordingRemote{err: errors.New("ssh died")}
	s := domain.Session{ID: "abc", TmuxSession: "demo"}
	if _, err := ResizeSessionTerminal(context.Background(), r, s, 154, 40); err == nil {
		t.Error("a transport failure must surface")
	}
}

func TestClampTerminalSizeAppliesFloors(t *testing.T) {
	cases := []struct{ inC, inR, wantC, wantR int }{
		{154, 40, 154, 40},                        // already usable
		{40, 8, MinTerminalCols, MinTerminalRows}, // both floored
		{200, 8, 200, MinTerminalRows},            // rows only
		{40, 60, MinTerminalCols, 60},             // cols only
	}
	for _, c := range cases {
		gotC, gotR, ok := ClampTerminalSize(c.inC, c.inR)
		if !ok {
			t.Fatalf("ClampTerminalSize(%d,%d) not ok", c.inC, c.inR)
		}
		if gotC != c.wantC || gotR != c.wantR {
			t.Errorf("ClampTerminalSize(%d,%d) = %dx%d, want %dx%d",
				c.inC, c.inR, gotC, gotR, c.wantC, c.wantR)
		}
	}
	if _, _, ok := ClampTerminalSize(0, 0); ok {
		t.Error("a zero size is not a request to clamp")
	}
}

func TestParseFitOutcome(t *testing.T) {
	if applied, err := ParseFitOutcome("AIMAN_FIT=applied"); !applied || err != nil {
		t.Errorf("applied: got %v / %v", applied, err)
	}
	if applied, err := ParseFitOutcome("AIMAN_FIT=attached"); applied || err != nil {
		t.Errorf("attached: got %v / %v", applied, err)
	}
	if _, err := ParseFitOutcome("AIMAN_FIT=failed"); err == nil {
		t.Error("a declined resize should be an error")
	}
	// Unrecognised output must not be read as success.
	if applied, err := ParseFitOutcome("no such session"); applied || err == nil {
		t.Errorf("garbage output: got %v / %v", applied, err)
	}
}
