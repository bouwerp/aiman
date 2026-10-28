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
		t.Error("an unattached PTY resize should apply")
	}
	if !strings.Contains(r.joined(), `aiman pty resize "abc" --cols 154 --rows 40`) {
		t.Errorf("unexpected command: %s", r.joined())
	}
}

// Someone watching an attached session must not have the window resized under
// them. That is a decision, not a failure, so it is reported as "not applied".
func TestResizeSessionTerminalPTYLeavesAttachedAlone(t *testing.T) {
	r := &recordingRemote{out: `{"type":"pty_resize","applied":false,"reason":"attached"}`}
	s := domain.Session{ID: "abc", Backend: domain.BackendPTY}

	applied, err := ResizeSessionTerminal(context.Background(), r, s, 80, 24)
	if err != nil {
		t.Fatalf("an attached session is not an error: %v", err)
	}
	if applied {
		t.Error("must not report a resize that was declined because a client is attached")
	}
}

func TestParsePTYResizeApplied(t *testing.T) {
	if !parsePTYResizeApplied(`{"type":"pty_resize","applied":true}`) {
		t.Error("applied true")
	}
	if parsePTYResizeApplied(`{"type":"pty_resize","applied":false,"reason":"attached"}`) {
		t.Error("applied false")
	}
	if !parsePTYResizeApplied(`{"type":"pty_resize"}`) {
		t.Error("older remotes without the flag still apply")
	}
	if !parsePTYResizeApplied("not json") {
		t.Error("unparseable output must not be treated as declined")
	}
}

func TestResizeSessionTerminalRejectsUnusableSizes(t *testing.T) {
	r := &recordingRemote{}
	s := domain.Session{ID: "abc", TerminalName: "demo"}
	for _, sz := range [][2]int{{0, 40}, {154, 0}, {-1, -1}} {
		if _, err := ResizeSessionTerminal(context.Background(), r, s, sz[0], sz[1]); err == nil {
			t.Errorf("expected an error for %dx%d", sz[0], sz[1])
		}
	}
	if len(r.cmds) != 0 {
		t.Errorf("nothing should have been sent remotely: %v", r.cmds)
	}
}

func TestResizeSessionTerminalPropagatesTransportErrors(t *testing.T) {
	r := &recordingRemote{err: errors.New("ssh died")}
	s := domain.Session{ID: "abc", TerminalName: "demo"}
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
