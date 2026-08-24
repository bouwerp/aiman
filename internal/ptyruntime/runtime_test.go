package ptyruntime

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestManagerCreateEchoAndCapture(t *testing.T) {
	m := NewManager()
	info, err := m.Create(Spec{
		ID:      "s1",
		Name:    "test",
		Command: "echo aiman_pty_ok",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if info.Status != StatusRunning || info.PID == 0 {
		t.Fatalf("unexpected info: %+v", info)
	}

	waitFor(t, func() bool {
		out, err := m.Capture("s1", 0)
		return err == nil && bytes.Contains(out, []byte("aiman_pty_ok"))
	})

	if _, err := m.Create(Spec{ID: "s1", Command: "true"}); err == nil {
		t.Fatal("duplicate id must fail")
	}
}

func TestManagerWriteReachesShell(t *testing.T) {
	m := NewManager()
	if _, err := m.Create(Spec{ID: "sh", Command: "true"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	// The bootstrap drops to `exec bash -i`; feed it a marker command.
	if err := m.Write("sh", []byte("echo wrote_$((40+2))\r")); err != nil {
		t.Fatalf("write: %v", err)
	}
	waitFor(t, func() bool {
		out, _ := m.Capture("sh", 0)
		return bytes.Contains(out, []byte("wrote_42"))
	})
}

func TestManagerKillTerminatesProcess(t *testing.T) {
	m := NewManager()
	if _, err := m.Create(Spec{ID: "k", Command: "sleep 300"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	start := time.Now()
	if err := m.Kill("k"); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("kill took %s; grace path stuck?", elapsed)
	}
	info, err := m.Get("k")
	if err != nil {
		t.Fatalf("get after kill: %v", err)
	}
	if info.Status != StatusExited {
		t.Fatalf("expected exited, got %s (%s)", info.Status, info.ExitErr)
	}
	if err := m.Kill("k"); err == nil {
		t.Fatal("kill on exited session must fail")
	}
	if err := m.Forget("k"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if _, err := m.Get("k"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after forget, got %v", err)
	}
}

func TestManagerResizeUpdatesSize(t *testing.T) {
	m := NewManager()
	if _, err := m.Create(Spec{ID: "r", Command: "sleep 60"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	defer func() { _ = m.Kill("r") }()
	if err := m.Resize("r", 120, 40); err != nil {
		t.Fatalf("resize: %v", err)
	}
	info, _ := m.Get("r")
	if info.Size != "120x40" {
		t.Fatalf("size not updated: %q", info.Size)
	}
	if err := m.Resize("r", -1, 0); err == nil {
		t.Fatal("negative size must fail")
	}
}

func TestRingBufferBoundsAndTail(t *testing.T) {
	r := newRingBuffer(16)
	r.append([]byte("0123456789"))
	r.append([]byte("abcdefghij"))
	if got := r.len(); got != 16 {
		t.Fatalf("len = %d, want capped 16", got)
	}
	if got := string(r.tail(4)); got != "ghij" {
		t.Fatalf("tail(4) = %q", got)
	}
	full := r.tail(0)
	if !strings.HasSuffix(string(full), "abcdefghij") {
		t.Fatalf("full tail = %q", full)
	}
	// Oversized append keeps only the newest bytes.
	r.append(make([]byte, 32))
	for i := range r.tail(0) {
		if r.tail(0)[i] != 0 {
			t.Fatal("oversized append should leave only zeros from the new data")
		}
	}
}

func TestEnvMapOverridesAndPreservesOrder(t *testing.T) {
	base := []string{"HOME=/home/u", "PATH=/bin", "AIMAN_ENV=0"}
	got := envMap(base, map[string]string{"AIMAN_ENV": "1", "NEW_VAR": "x"})
	joined := strings.Join(got, ";")
	if !strings.Contains(joined, "AIMAN_ENV=1") || strings.Contains(joined, "AIMAN_ENV=0") {
		t.Fatalf("override failed: %s", joined)
	}
	if !strings.Contains(joined, "NEW_VAR=x") {
		t.Fatalf("missing new var: %s", joined)
	}
	// Empty values are dropped so secrets cannot be blanked accidentally.
	got = envMap(base, map[string]string{"NEW_VAR": ""})
	if strings.Contains(strings.Join(got, ";"), "NEW_VAR") {
		t.Fatalf("empty value must be dropped: %v", got)
	}
}

func TestSubscribeReplaysThenStreams(t *testing.T) {
	m := NewManager()
	if _, err := m.Create(Spec{ID: "sub", Command: "echo first_output"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	waitFor(t, func() bool {
		out, _ := m.Capture("sub", 0)
		return bytes.Contains(out, []byte("first_output"))
	})

	buffered := make(chan []byte, 64)
	live, unsub, err := m.sessions["sub"].subscribe(buffered)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsub()

	// Replay arrives asynchronously.
	replayed := ""
	waitFor(t, func() bool {
		select {
		case c := <-buffered:
			replayed += string(c)
		case <-live:
		default:
		}
		return strings.Contains(replayed, "first_output")
	})

	// Live writes stream to the subscriber.
	if err := m.Write("sub", []byte("echo live_marker\r")); err != nil {
		t.Fatalf("write: %v", err)
	}
	sawLive := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
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
		if sawLive {
			break
		}
	}
	if !sawLive {
		t.Fatal("live write never reached subscriber")
	}
}

const waitTimeout = 5 * time.Second

// waitFor polls cond until it holds or the standard timeout elapses.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", waitTimeout)
}
