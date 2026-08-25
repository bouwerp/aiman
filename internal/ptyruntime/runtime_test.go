package ptyruntime

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/ptyhold"
)

var holderBin []string // built by TestMain: path to a real aiman binary

// TestMain builds the real aiman binary once so holders run exactly as they
// do in production — the whole point of these tests is surviving process
// boundaries, which in-process fakes cannot prove.
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "aiman-pty-test-*")
	if err != nil {
		panic(err)
	}
	bin := filepath.Join(tmp, "aiman")
	build := exec.Command("go", "build", "-o", bin, "github.com/bouwerp/aiman/cmd/aiman")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		panic("building aiman for pty tests: " + err.Error())
	}
	holderBin = []string{bin, "pty", "hold"}
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	return NewManagerWithRoot(root, append([]string(nil), holderBin...))
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestHolderCreateEchoAndCapture(t *testing.T) {
	m := newTestManager(t)
	info, err := m.Create(Spec{ID: "s1", Name: "test", Command: "echo aiman_pty_ok"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if info.Status != StatusRunning || info.PID == 0 {
		t.Fatalf("unexpected info: %+v", info)
	}
	waitFor(t, func() bool {
		out, _ := m.Capture("s1", 0)
		return bytes.Contains(out, []byte("aiman_pty_ok"))
	})

	if _, err := m.Create(Spec{ID: "s1", Command: "true"}); err == nil {
		t.Fatal("duplicate id must fail")
	}
}

func TestHolderWriteReachesShell(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Create(Spec{ID: "sh", Command: "true"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := m.Write("sh", []byte("echo wrote_$((40+2))\r")); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor(t, func() bool {
		out, _ := m.Capture("sh", 0)
		return bytes.Contains(out, []byte("wrote_42"))
	})
}

func TestHolderSurvivesManagerRestart(t *testing.T) {
	root := t.TempDir()
	holderCmd := append([]string(nil), holderBin...)

	a := NewManagerWithRoot(root, holderCmd)
	if _, err := a.Create(Spec{ID: "surv", Command: "echo before_restart"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	waitFor(t, func() bool {
		out, _ := a.Capture("surv", 0)
		return bytes.Contains(out, []byte("before_restart"))
	})
	a.CloseAll() // simulate serve shutdown: connections dropped, holders untouched

	// "serve restart": a brand-new manager over the same root must find the
	// session still running, with its scrollback intact.
	b := NewManagerWithRoot(root, holderCmd)
	info, err := b.Get("surv")
	if err != nil {
		t.Fatalf("get after restart: %v", err)
	}
	if info.Status != StatusRunning {
		t.Fatalf("session did not survive restart: %+v", info)
	}
	out, err := b.Capture("surv", 0)
	if err != nil || !bytes.Contains(out, []byte("before_restart")) {
		t.Fatalf("scrollback lost across restart: %q / %v", out, err)
	}
	// And it is still interactive under the new manager.
	if err := b.Write("surv", []byte("echo after_$((70+7))\r")); err != nil {
		t.Fatalf("write after restart: %v", err)
	}
	waitFor(t, func() bool {
		out2, _ := b.Capture("surv", 0)
		return bytes.Contains(out2, []byte("after_77"))
	})
}

func TestHolderListFindsSessionsFromFreshManager(t *testing.T) {
	root := t.TempDir()
	holderCmd := append([]string(nil), holderBin...)
	a := NewManagerWithRoot(root, holderCmd)
	if _, err := a.Create(Spec{ID: "l1", Name: "one", Command: "sleep 300"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	b := NewManagerWithRoot(root, holderCmd)
	list := b.List()
	found := false
	for _, info := range list {
		if info.ID == "l1" && info.Name == "one" && info.Status == StatusRunning {
			found = true
		}
	}
	if !found {
		t.Fatalf("fresh manager must adopt existing sessions: %+v", list)
	}
	if err := b.Kill("l1"); err != nil {
		t.Fatalf("kill from adopting manager: %v", err)
	}
	waitFor(t, func() bool {
		info, gerr := b.Get("l1")
		return gerr == nil && info.Status == StatusExited
	})
	if err := b.Forget("l1"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if _, gerr := b.Get("l1"); gerr != ErrNotFound {
		t.Fatalf("expected ErrNotFound after forget, got %v", gerr)
	}
}

func TestHolderKillTerminatesProcess(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Create(Spec{ID: "k", Command: "sleep 300"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	start := time.Now()
	if err := m.Kill("k"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if elapsed := time.Since(start); elapsed > killTimeout {
		t.Fatalf("kill took %s; grace path stuck?", elapsed)
	}
	info, _ := m.Get("k")
	if info.Status != StatusExited {
		t.Fatalf("expected exited, got %s (%s)", info.Status, info.ExitErr)
	}
	if err := m.Kill("k"); err == nil {
		t.Fatal("kill on exited session must fail")
	}
}

func TestHolderResizeRoundTrip(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Create(Spec{ID: "r", Command: "sleep 60"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = m.Kill("r") }()
	if err := m.Resize("r", 120, 40); err != nil {
		t.Fatalf("resize: %v", err)
	}
	// The contract has no size readback (files only); verify the marker was
	// consumed by the holder's control loop.
	waitFor(t, func() bool {
		_, err := os.Stat(ptyhold.Dir(m.root, "r") + string(os.PathSeparator) + "resize")
		return err != nil // file gone => holder applied it
	})
}

func TestSubscribeReplaysThenStreams(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Create(Spec{ID: "sub", Command: "echo first_output"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	waitFor(t, func() bool {
		out, _ := m.Capture("sub", 0)
		return bytes.Contains(out, []byte("first_output"))
	})

	replay, live, unsub, err := m.Subscribe("sub")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsub()
	if !bytes.Contains(replay, []byte("first_output")) {
		t.Fatalf("replay missing prior output: %q", replay)
	}

	if err := m.Write("sub", []byte("echo live_marker\r")); err != nil {
		t.Fatalf("write: %v", err)
	}
	sawLive := false
	deadline := time.Now().Add(10 * time.Second)
	for !sawLive && time.Now().Before(deadline) {
		select {
		case c, ok := <-live:
			if !ok {
				return
			}
			if bytes.Contains(c, []byte("live_marker")) {
				sawLive = true
			}
		default:
		}
	}
	if !sawLive {
		t.Fatal("live write never reached subscriber")
	}
}

func TestSpoolRotationKeepsHistoryReadable(t *testing.T) {
	t.Setenv("AIMAN_PTY_SPOOL_MAX", "65536") // small segments so rotation happens quickly
	m := newTestManager(t)
	if _, err := m.Create(Spec{ID: "big", Command: "true"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// Write more than one spool segment through the shell.
	chunk := strings.Repeat("x", 1024) + "\r"
	go func() {
		for i := 0; i < 300; i++ { // ~600 KiB echoed back, several segments
			_ = m.Write("big", []byte(chunk))
		}
	}()
	waitFor(t, func() bool {
		_, err := os.Stat(filepath.Join(ptyhold.Dir(m.root, "big"), ptyhold.SpoolOld))
		return err == nil
	})
	out := ptyhold.ReadSpool(m.root, "big", 1<<20)
	if len(out) == 0 || len(out) > 1<<20 {
		t.Fatalf("tail read returned %d bytes, want 0 < n <= cap", len(out))
	}
	if !bytes.Contains(ptyhold.ReadSpool(m.root, "big", 0), []byte(strings.Repeat("x", 1024))) {
		t.Fatal("history content lost across rotation")
	}
	if err := m.Kill("big"); err != nil {
		t.Logf("kill big (best effort): %v", err)
	}
}
