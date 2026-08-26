package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/ptyruntime"
)

// holderBin is a real `aiman pty hold` command, built once for the suite.
//
// It must never default to os.Executable(): under `go test` that is the test
// binary, and a Go test binary ignores unknown positional args, so a holder
// spawned that way silently re-runs this whole suite — which creates more
// sessions, which fork more suites. That is not a leak but exponential
// self-replication; it once reached ~2000 resident processes and OOM-killed
// the dev box. ptyruntime.holderCmd now panics under test rather than allow
// it, and this is the correct injection.
var holderBin []string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "aiman-server-test-*")
	if err != nil {
		panic(err)
	}
	bin := filepath.Join(tmp, "aiman")
	build := exec.Command("go", "build", "-o", bin, "github.com/bouwerp/aiman/cmd/aiman")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		_ = os.RemoveAll(tmp)
		panic("building aiman for pty tests: " + err.Error())
	}
	holderBin = []string{bin, "pty", "hold"}
	code := m.Run()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}

// startPTYServer runs a serve instance with a live PTY manager and returns
// its socket path. The manager is rooted in a temp dir (never the real
// ~/.aiman) and holds via the built binary, never this test binary.
func startPTYServer(t *testing.T) string {
	t.Helper()
	dir := shortTempDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	mgr := ptyruntime.NewManagerWithRoot(shortTempDir(t), append([]string(nil), holderBin...))
	srv := New(ln, nil, nil, nil, nil, mgr, "test")
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() { defer close(served); _ = srv.Serve(ctx) }()
	t.Cleanup(func() {
		// Serve owns the listener and closes it itself once ctx is done. Closing
		// it here as well was a double close, which -race reports as a data race
		// on the underlying file descriptor. Cancel, then wait for Serve to
		// return so teardown is ordered.
		cancel()
		<-served
		mgr.CloseAll()
	})
	return SocketPath(dir)
}

// createPTY creates a session and registers its kill as test cleanup, so no
// individual test can leak a holder by forgetting to tear it down.
func createPTY(t *testing.T, sock string, params map[string]any) Response {
	t.Helper()
	resp, err := Call(sock, "pty.create", params)
	if err != nil {
		t.Fatalf("pty.create: %v", err)
	}
	if id, _ := params["id"].(string); id != "" {
		t.Cleanup(func() {
			_, _ = Call(sock, "pty.kill", map[string]any{"id": id})
		})
	}
	return resp
}

func resultJSON(t *testing.T, resp Response) string {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected error: %v", resp.Error)
	}
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return string(b)
}

func captureText(t *testing.T, sock, id string) string {
	t.Helper()
	resp, err := Call(sock, "pty.capture", map[string]any{"id": id})
	if err != nil {
		t.Fatalf("capture call: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("capture: %v", resp.Error)
	}
	m, _ := resp.Result.(map[string]any)
	text, _ := m["text"].(string)
	return text
}

func eventually(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestPTYCreateCaptureKillOverSocket(t *testing.T) {
	sock := startPTYServer(t)

	create := createPTY(t, sock, map[string]any{
		"id": "s1", "name": "test", "command": "echo e2e_marker",
	})
	if create.Error != nil {
		t.Fatalf("create: %v", create.Error)
	}

	eventually(t, 5*time.Second, func() bool {
		return strings.Contains(captureText(t, sock, "s1"), "e2e_marker")
	})

	list, err := Call(sock, "pty.list", map[string]any{})
	if err != nil {
		t.Fatalf("list call: %v", err)
	}
	if !strings.Contains(resultJSON(t, list), `"status":"running"`) {
		t.Fatalf("expected running session in list: %s", resultJSON(t, list))
	}

	in, err := Call(sock, "pty.input", map[string]any{"id": "s1", "data": "echo typed_$((30+12))\r"})
	if err != nil || in.Error != nil {
		t.Fatalf("input: %v / %v", err, in.Error)
	}
	eventually(t, 5*time.Second, func() bool {
		return strings.Contains(captureText(t, sock, "s1"), "typed_42")
	})

	if kill, kerr := Call(sock, "pty.kill", map[string]any{"id": "s1"}); kerr != nil || kill.Error != nil {
		t.Fatalf("kill: %v / %v", kerr, kill.Error)
	}
	eventually(t, 5*time.Second, func() bool {
		info, gerr := Call(sock, "pty.get", map[string]any{"id": "s1"})
		return gerr == nil && info.Error == nil && strings.Contains(resultJSON(t, info), `"exited"`)
	})

	if fg, ferr := Call(sock, "pty.forget", map[string]any{"id": "s1"}); ferr != nil || fg.Error != nil {
		t.Fatalf("forget: %v / %v", ferr, fg.Error)
	}
	gone, _ := Call(sock, "pty.get", map[string]any{"id": "s1"})
	if gone.Error == nil || gone.Error.Code != CodeNotFound {
		t.Fatalf("expected not_found after forget, got %+v", gone.Error)
	}
}

func TestPTYAttachRelaysBothDirections(t *testing.T) {
	sock := startPTYServer(t)
	create := createPTY(t, sock, map[string]any{"id": "att", "command": "true"})
	if create.Error != nil {
		t.Fatalf("create: %v", create.Error)
	}

	conn, err := AttachDial(sock, "att", 100, 30)
	if err != nil {
		t.Fatalf("attach dial: %v", err)
	}
	defer conn.Close()

	stdinR, stdinW := io.Pipe()
	var outMu sync.Mutex
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- conn.Relay(stdinR, &lockedWriter{mu: &outMu, w: &out}) }()

	outText := func() string {
		outMu.Lock()
		defer outMu.Unlock()
		return out.String()
	}

	if _, werr := stdinW.Write([]byte("echo attached_$((50+7))\r")); werr != nil && werr != io.ErrClosedPipe {
		t.Logf("stdin write: %v", werr)
	}
	eventually(t, 10*time.Second, func() bool {
		return strings.Contains(outText(), "attached_57")
	})

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("relay ended early: %v", err)
		}
	default:
	}

	// Session must still be running after the relay is torn down.
	info, gerr := Call(sock, "pty.get", map[string]any{"id": "att"})
	if gerr != nil || info.Error != nil {
		t.Fatalf("get after relay: %v / %v", gerr, info.Error)
	}
	if !strings.Contains(resultJSON(t, info), `"running"`) {
		t.Fatalf("session must survive relay teardown, got: %s", resultJSON(t, info))
	}
}

// lockedWriter appends to a buffer under a mutex; the relay writes from its
// own goroutine while assertions poll the same buffer.
type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func TestPTYAttachLiveResize(t *testing.T) {
	sock := startPTYServer(t)
	create := createPTY(t, sock, map[string]any{"id": "rsz", "command": "true"})
	if create.Error != nil {
		t.Fatalf("create: %v", create.Error)
	}

	conn, err := AttachDial(sock, "rsz", 80, 24)
	if err != nil {
		t.Fatalf("attach dial: %v", err)
	}
	defer conn.Close()

	if rerr := conn.Resize(123, 45); rerr != nil {
		t.Fatalf("resize: %v", rerr)
	}
	eventually(t, 5*time.Second, func() bool {
		info, gerr := Call(sock, "pty.get", map[string]any{"id": "rsz"})
		return gerr == nil && info.Error == nil && strings.Contains(resultJSON(t, info), `"123x45"`)
	})
}

func TestPTYAttachUnknownSessionFailsCleanly(t *testing.T) {
	sock := startPTYServer(t)
	if _, err := AttachDial(sock, "missing", 80, 24); err == nil {
		t.Fatal("attach to unknown session must fail")
	}
}

// Resizing used to be reachable only from inside pty.attach, which left no way
// to fit a session for a viewer that is not attached — the dashboard's preview
// panel is far narrower than the terminal that last sized the session.
func TestPTYResizeOutsideAnAttachStream(t *testing.T) {
	sock := startPTYServer(t)
	create := createPTY(t, sock, map[string]any{"id": "rsz2", "command": "sleep 30", "cols": 200, "rows": 50})
	if create.Error != nil {
		t.Fatalf("create: %v", create.Error)
	}

	resp, err := Call(sock, "pty.resize", map[string]any{"id": "rsz2", "cols": 100, "rows": 30})
	if err != nil || resp.Error != nil {
		t.Fatalf("resize: %v / %v", err, resp.Error)
	}

	// The holder records the size it applied, so get reports it back.
	eventually(t, 10*time.Second, func() bool {
		got, gerr := Call(sock, "pty.get", map[string]any{"id": "rsz2"})
		if gerr != nil || got.Error != nil {
			return false
		}
		return strings.Contains(resultJSON(t, got), `"100x30"`)
	})
}

func TestPTYResizeRejectsUnusableSizes(t *testing.T) {
	sock := startPTYServer(t)
	create := createPTY(t, sock, map[string]any{"id": "rsz3", "command": "sleep 30"})
	if create.Error != nil {
		t.Fatalf("create: %v", create.Error)
	}
	for _, params := range []map[string]any{
		{"id": "rsz3", "cols": 0, "rows": 30},
		{"id": "rsz3", "cols": 100, "rows": 0},
		{"id": "rsz3"},
	} {
		resp, err := Call(sock, "pty.resize", params)
		if err != nil {
			t.Fatalf("call: %v", err)
		}
		if resp.Error == nil {
			t.Errorf("expected an error for %v", params)
		}
	}
}
