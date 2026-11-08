package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// After a failed attach the dashboard still waits on the attach master's
// close and logs a close failure.
func TestAttachDoneReportsMasterClose(t *testing.T) {
	model := newTestModelWithSession(t, domain.Session{ID: "s1", RemoteHost: "devbox", TerminalName: "PB-1"})
	ch := make(chan error, 1)
	ch <- errors.New("broken pipe")

	_, cmd := model.applyAttachDone(attachDoneMsg{err: errors.New("exit status 1"), masterClosed: ch}, nil)
	if cmd == nil {
		t.Fatal("want a command waiting on the master close")
	}
	got, ok := cmd().(attachMasterClosedMsg)
	if !ok {
		t.Fatal("want an attachMasterClosedMsg")
	}
	model.Update(got)
	if !strings.Contains(strings.Join(model.consoleLog, "\n"), "close attach master: broken pipe") {
		t.Errorf("close failure not logged: %q", model.consoleLog)
	}
}

// No master to close means no extra command.
func TestWaitAttachMasterClosedNil(t *testing.T) {
	if waitAttachMasterClosed(nil) != nil {
		t.Fatal("want nil command when nothing was closed")
	}
}
