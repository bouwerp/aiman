package server

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
)

// screenSession runs a pty that prints a boot-dialog fixture and stays up,
// with a fresh idle hook report on the session.
func screenSession(t *testing.T, id, fixture string) *Server {
	t.Helper()
	return screenSessionShowing(t, id, fixture, "elease notes")
}

func screenSessionShowing(t *testing.T, id, fixture, shown string) *Server {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "usecase", "testdata", "bootdialogs", fixture+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	mgr := ptyruntime.NewManagerWithRoot(shortTempDir(t), append([]string(nil), holderBin...))
	t.Cleanup(mgr.CloseAll)
	if _, err := mgr.Create(ptyruntime.Spec{ID: id, Name: id, Command: fmt.Sprintf("cat %q; exec cat", path)}); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Kill(id) })
	eventually(t, 5*time.Second, func() bool {
		screen, err := mgr.CaptureScreen(id)
		return err == nil && strings.Contains(screen, shown)
	})
	sess := hookedSession(id, domain.AgentStateIdle, agenthook.SourceLifecycle, time.Now())
	sess.Backend = domain.BackendPTY
	srv := steerGateServer(t, sess)
	srv.pty = mgr
	return srv
}

// A hook can say idle while Codex's update menu is up. A note typed there
// selects a row, and the default row installs.
func TestSteerHoldsOnUpdateDialogDespiteIdleHook(t *testing.T) {
	srv := screenSession(t, "sd1", "codex_update")
	ready, held := srv.steerReady(context.Background(), "sd1", time.Now().Add(-time.Minute))
	if ready || held != steerHeldDialog {
		t.Fatalf("steerReady = (%v, %q), want (false, %q)", ready, held, steerHeldDialog)
	}
}

// Codex's "Update available!" banner above a ready composer is not a dialog.
func TestSteerReadyWithUpdateBannerAboveComposer(t *testing.T) {
	srv := screenSession(t, "sd2", "codex_after_esc")
	if ready, held := srv.steerReady(context.Background(), "sd2", time.Now().Add(-time.Minute)); !ready {
		t.Fatalf("steerReady = (false, %q), want ready under a banner", held)
	}
}

// Codex's retired-model notice swallows typed text, so a note waits behind it.
func TestSteerHoldsOnRetiredModelNotice(t *testing.T) {
	srv := screenSessionShowing(t, "sd3", "codex_model_gone", "no longer available")
	ready, held := srv.steerReady(context.Background(), "sd3", time.Now().Add(-time.Minute))
	if ready || held != steerHeldDialog {
		t.Fatalf("steerReady = (%v, %q), want (false, %q)", ready, held, steerHeldDialog)
	}
}

// The real path: bytes through a holder's spool into serve's capture. The
// capture keeps dim for the phone, and notification text stays plain.
func TestCaptureKeepsDimThroughTheHolder(t *testing.T) {
	const id = "sd4"
	mgr := ptyruntime.NewManagerWithRoot(shortTempDir(t), append([]string(nil), holderBin...))
	t.Cleanup(mgr.CloseAll)
	command := `printf '\342\235\257\302\240\033[2mTry it\r\033[1B\033[22m'; exec cat`
	if _, err := mgr.Create(ptyruntime.Spec{ID: id, Name: id, Command: command}); err != nil {
		t.Fatalf("create: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Kill(id) })
	const want = "❯\u00a0\x1b[2mTry it\x1b[22m"
	eventually(t, 5*time.Second, func() bool {
		screen, err := mgr.CaptureScreen(id)
		return err == nil && strings.Contains(screen, want)
	})
	sess := hookedSession(id, domain.AgentStateIdle, agenthook.SourceLifecycle, time.Now())
	sess.Backend = domain.BackendPTY
	srv := steerGateServer(t, sess)
	srv.pty = mgr
	if got := srv.lastScreenLine(sess); got != "❯\u00a0Try it" {
		t.Fatalf("lastScreenLine = %q", got)
	}
}
