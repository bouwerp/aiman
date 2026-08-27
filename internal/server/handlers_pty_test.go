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
		"id": "s1", "name": "test", "command": "bash -c 'echo e2e_marker; exec bash -i'",
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
	create := createPTY(t, sock, map[string]any{"id": "att", "command": "bash -i"})
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
	create := createPTY(t, sock, map[string]any{"id": "rsz", "command": "sleep 60"})
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

// A second attach at the same size used to skip SIGWINCH (TIOCSWINSZ is a
// no-op when the winsize is unchanged). The client had already been cleared,
// so Grok never repainted its chrome.
func TestPTYAttachSameSizeSendsWINCH(t *testing.T) {
	sock := startPTYServer(t)
	cmd := `python3 -c "import signal,sys,time; signal.signal(signal.SIGWINCH, lambda *_: (sys.stdout.write('WINCHED\n'), sys.stdout.flush())); time.sleep(60)"`
	create := createPTY(t, sock, map[string]any{
		"id": "winch", "command": cmd, "cols": 80, "rows": 24,
	})
	if create.Error != nil {
		t.Fatalf("create: %v", create.Error)
	}
	time.Sleep(300 * time.Millisecond)

	conn, err := AttachDial(sock, "winch", 80, 24)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer conn.Close()
	stdinR, stdinW := io.Pipe()
	defer stdinW.Close()
	var mu sync.Mutex
	var out bytes.Buffer
	go func() { _ = conn.Relay(stdinR, &lockedWriter{mu: &mu, w: &out}) }()
	time.Sleep(200 * time.Millisecond)
	_ = conn.Resize(78, 23)
	time.Sleep(200 * time.Millisecond)
	_ = conn.Resize(80, 24)

	var got string
	eventually(t, 10*time.Second, func() bool {
		mu.Lock()
		got = out.String()
		mu.Unlock()
		return strings.Contains(got, "WINCHED")
	})
	if !strings.Contains(got, "WINCHED") {
		t.Fatalf("reattach at the same size must SIGWINCH the agent, got %q", got)
	}
}

// Two TIOCSWINSZ in one syscall burst coalesce into a single SIGWINCH, and
// TIOCGWINSZ then reports the original size, so Grok/Ink skip a full layout.
// The holder must let the child observe the nudged size before restoring.
func TestPTYAttachSameSizeReportsNudgedThenRestoredSize(t *testing.T) {
	sock := startPTYServer(t)
	cmd := `python3 -c "import fcntl,signal,struct,sys,termios,time
def sz():
 s=fcntl.ioctl(1,termios.TIOCGWINSZ,struct.pack('HHHH',0,0,0,0)); r,c,_,_=struct.unpack('HHHH',s); return c,r
def h(*_):
 c,r=sz(); sys.stdout.write('WINCH:%dx%d\n'%(c,r)); sys.stdout.flush()
signal.signal(signal.SIGWINCH,h); time.sleep(60)"`
	create := createPTY(t, sock, map[string]any{
		"id": "winch-sz", "command": cmd, "cols": 80, "rows": 24,
	})
	if create.Error != nil {
		t.Fatalf("create: %v", create.Error)
	}
	time.Sleep(300 * time.Millisecond)

	conn, err := AttachDial(sock, "winch-sz", 80, 24)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer conn.Close()
	stdinR, stdinW := io.Pipe()
	defer stdinW.Close()
	var mu sync.Mutex
	var out bytes.Buffer
	go func() { _ = conn.Relay(stdinR, &lockedWriter{mu: &mu, w: &out}) }()
	time.Sleep(200 * time.Millisecond)
	_ = conn.Resize(80, 24)

	var got string
	eventually(t, 10*time.Second, func() bool {
		mu.Lock()
		got = out.String()
		mu.Unlock()
		return strings.Contains(got, "WINCH:79x24") && strings.Contains(got, "WINCH:80x24")
	})
	if !strings.Contains(got, "WINCH:79x24") || !strings.Contains(got, "WINCH:80x24") {
		t.Fatalf("same-size attach must WINCH at the nudge then the real size, got %q", got)
	}
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

func TestPTYResizeDeclinedWhenAttached(t *testing.T) {
	sock := startPTYServer(t)
	create := createPTY(t, sock, map[string]any{"id": "att-fit", "command": "sleep 30", "cols": 200, "rows": 50})
	if create.Error != nil {
		t.Fatalf("create: %v", create.Error)
	}

	conn, err := AttachDial(sock, "att-fit", 200, 50)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer conn.Close()

	resp, err := Call(sock, "pty.resize", map[string]any{"id": "att-fit", "cols": 80, "rows": 24})
	if err != nil || resp.Error != nil {
		t.Fatalf("resize: %v / %v", err, resp.Error)
	}
	got := resultJSON(t, resp)
	if !strings.Contains(got, `"applied":false`) || !strings.Contains(got, `"attached"`) {
		t.Fatalf("expected a declined resize, got %s", got)
	}

	info, gerr := Call(sock, "pty.get", map[string]any{"id": "att-fit"})
	if gerr != nil || info.Error != nil {
		t.Fatalf("get: %v / %v", gerr, info.Error)
	}
	if strings.Contains(resultJSON(t, info), `"80x24"`) {
		t.Fatalf("preview fit must not shrink an attached session, got %s", resultJSON(t, info))
	}
}

// Full-screen agents (Grok, Claude, agy) paint with CUP. Replaying the raw
// spool on attach dumps every historical frame onto the local terminal.
func TestPTYAttachResetsWithoutReplayingRawCUPHistory(t *testing.T) {
	sock := startPTYServer(t)
	cmd := "printf '\\033[1;1HRAW_OVERDRAWN\\033[1;1HVISIBLE_NOW'; sleep 60"
	create := createPTY(t, sock, map[string]any{
		"id": "cup", "command": cmd, "cols": 80, "rows": 24,
	})
	if create.Error != nil {
		t.Fatalf("create: %v", create.Error)
	}
	eventually(t, 10*time.Second, func() bool {
		return strings.Contains(captureText(t, sock, "cup"), "VISIBLE_NOW")
	})

	got := attachOutput(t, sock, "cup")
	if strings.Contains(got, "RAW_OVERDRAWN") {
		t.Fatalf("attach dumped overwritten CUP history onto the terminal: %q", got)
	}
	if !strings.Contains(got, "VISIBLE_NOW") {
		t.Fatalf("attach must show the current screen immediately, got %q", got)
	}
	if !strings.Contains(got, "\x1b[2J") && !strings.Contains(got, "\x1b[?1049h") {
		t.Fatalf("attach must reset the terminal before painting, got %q", got)
	}
}

// CaptureScreen joins rows with LF. Attach puts the tty in raw mode, where LF
// is "cursor down, same column" — Ink TUIs (agy) come out sheared. Attach must
// not dump that snapshot; SIGWINCH makes the agent redraw with real CUP.
func TestPTYAttachDoesNotDumpLFRenderedSnapshot(t *testing.T) {
	sock := startPTYServer(t)
	cmd := "printf 'ROW_A\\nROW_B\\nROW_C'; sleep 60"
	create := createPTY(t, sock, map[string]any{
		"id": "lf", "command": cmd, "cols": 80, "rows": 24,
	})
	if create.Error != nil {
		t.Fatalf("create: %v", create.Error)
	}
	eventually(t, 10*time.Second, func() bool {
		text := captureText(t, sock, "lf")
		return strings.Contains(text, "ROW_A") && strings.Contains(text, "ROW_B")
	})

	got := attachOutput(t, sock, "lf")
	if !strings.Contains(got, "ROW_A") || !strings.Contains(got, "ROW_B") {
		t.Fatalf("attach must show the current screen immediately, got %q", got)
	}
	if strings.Contains(got, "ROW_A\nROW_B") {
		t.Fatalf("attach dumped an LF-joined snapshot (shears in raw mode): %q", got)
	}
	if !strings.Contains(got, "\x1b[2J") && !strings.Contains(got, "\x1b[?1049h") {
		t.Fatalf("attach must reset the terminal, got %q", got)
	}
}

func TestEncodeAttachScreenEmptySkipsClear(t *testing.T) {
	got := string(encodeAttachScreen("  \n"))
	if strings.Contains(got, "\x1b[2J") {
		t.Fatalf("empty dump must not clear a local grow animation: %q", got)
	}
}

func TestEncodeAttachScreenUsesCUPNotBareLF(t *testing.T) {
	got := string(encodeAttachScreen("ROW_A\nROW_B"))
	if !strings.Contains(got, "ROW_A") || !strings.Contains(got, "ROW_B") {
		t.Fatalf("dump must include both rows: %q", got)
	}
	if strings.Contains(got, "ROW_A\nROW_B") {
		t.Fatalf("raw-mode dump must not join rows with bare LF: %q", got)
	}
	if !strings.Contains(got, "\x1b[1;1H") || !strings.Contains(got, "\x1b[2;1H") {
		t.Fatalf("each row must be CUP-addressed: %q", got)
	}
}

func attachOutput(t *testing.T, sock, id string) string {
	t.Helper()
	conn, err := AttachDial(sock, id, 80, 24)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	stdinR, stdinW := io.Pipe()
	t.Cleanup(func() { _ = stdinW.Close() })
	var mu sync.Mutex
	var out bytes.Buffer
	go func() { _ = conn.Relay(stdinR, &lockedWriter{mu: &mu, w: &out}) }()

	var got string
	eventually(t, 10*time.Second, func() bool {
		mu.Lock()
		got = out.String()
		mu.Unlock()
		return strings.Contains(got, "\x1b[2J") || strings.Contains(got, "\x1b[?1049h")
	})
	return got
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

func TestPTYRunSubmitsCommandWithEnter(t *testing.T) {
	sock := startPTYServer(t)
	create := createPTY(t, sock, map[string]any{"id": "run1", "command": "bash -i"})
	if create.Error != nil {
		t.Fatalf("create: %v", create.Error)
	}
	resp, err := Call(sock, "pty.run", map[string]any{"id": "run1", "command": "echo run_marker_42"})
	if err != nil || resp.Error != nil {
		t.Fatalf("run: %v / %+v", err, resp.Error)
	}
	eventually(t, 8*time.Second, func() bool {
		return strings.Contains(captureText(t, sock, "run1"), "run_marker_42")
	})
}

func TestPTYWaitOutputMatchesExistingText(t *testing.T) {
	sock := startPTYServer(t)
	create := createPTY(t, sock, map[string]any{
		"id": "w1", "command": "bash -c 'echo wait_already_here; exec bash -i'",
	})
	if create.Error != nil {
		t.Fatalf("create: %v", create.Error)
	}
	eventually(t, 8*time.Second, func() bool {
		return strings.Contains(captureText(t, sock, "w1"), "wait_already_here")
	})
	resp, err := Call(sock, "pty.wait-output", map[string]any{
		"id": "w1", "match": "wait_already_here", "timeout_ms": 2000,
	})
	if err != nil || resp.Error != nil {
		t.Fatalf("wait-output: %v / %+v", err, resp.Error)
	}
	m, _ := resp.Result.(map[string]any)
	if m["matched"] != true {
		t.Fatalf("expected matched, got %+v", m)
	}
}

func TestPTYWaitOutputStripsANSI(t *testing.T) {
	sock := startPTYServer(t)
	create := createPTY(t, sock, map[string]any{
		"id": "wansi", "command": "bash -c 'printf \"\\033[32mGREENPASS\\033[0m\\n\"; exec bash -i'",
	})
	if create.Error != nil {
		t.Fatalf("create: %v", create.Error)
	}
	resp, err := Call(sock, "pty.wait-output", map[string]any{
		"id": "wansi", "match": "GREENPASS", "timeout_ms": 8000,
	})
	if err != nil || resp.Error != nil {
		t.Fatalf("wait-output: %v / %+v", err, resp.Error)
	}
}

func TestPTYWaitOutputRegexAndTimeout(t *testing.T) {
	sock := startPTYServer(t)
	create := createPTY(t, sock, map[string]any{"id": "w2", "command": "bash -c 'echo abc123xyz; exec bash -i'"})
	if create.Error != nil {
		t.Fatalf("create: %v", create.Error)
	}
	ok, err := Call(sock, "pty.wait-output", map[string]any{
		"id": "w2", "regex": "abc[0-9]+xyz", "timeout_ms": 8000,
	})
	if err != nil || ok.Error != nil {
		t.Fatalf("regex wait: %v / %+v", err, ok.Error)
	}
	miss, err := Call(sock, "pty.wait-output", map[string]any{
		"id": "w2", "match": "this_string_is_not_in_the_pane", "timeout_ms": 300,
	})
	if err != nil {
		t.Fatalf("timeout call: %v", err)
	}
	if miss.Error == nil || miss.Error.Code != CodeTimeout {
		t.Fatalf("expected timeout, got %+v", miss.Error)
	}
}
