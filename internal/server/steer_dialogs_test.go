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
		return err == nil && strings.Contains(screen, "elease notes")
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
