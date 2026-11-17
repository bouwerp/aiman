// Package harnesstest starts a real debug run for the Docker tests of the
// harness and the console: a compose project from harness.Compose with the
// Go adapter, the pinned Delve built on this host, a tiny Go app and a
// postgres service. It is test support; nothing in aiman imports it.
package harnesstest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/edge"
)

// Images the debug run uses. Only these tests pull them.
const (
	BusyboxImage  = "busybox:1.36"
	PostgresImage = "postgres:16"
)

// BreakLine is the line of tick's body in AppSource.
const BreakLine = 9

// AppSource is the debuggee: it calls tick ten times a second.
const AppSource = `package main

import (
	"fmt"
	"time"
)

func tick(counter int) int {
	next := counter + 1
	return next
}

func main() {
	c := 0
	for {
		c = tick(c)
		if c%50 == 0 {
			fmt.Println("tick", c)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
`

const debugFile = `version: 1
app:
  run: /out/app
  debug: {lang: go}
services:
  db:
    image: postgres:16
    env: {POSTGRES_PASSWORD: test}
    ready: pg_isready -h 127.0.0.1 -U postgres
    root: true
    caps: [CHOWN, SETUID, SETGID, DAC_OVERRIDE, FOWNER]
    limits: {memory: 512m}
scenarios:
  - id: dbg
    services: [db]
`

// NeedDocker skips the test unless AIMAN_TEST_DOCKER=1.
func NeedDocker(t *testing.T) {
	t.Helper()
	if os.Getenv("AIMAN_TEST_DOCKER") != "1" {
		t.Skip("set AIMAN_TEST_DOCKER=1 to run Docker tests")
	}
}

// DebugRun is a running debug project.
type DebugRun struct {
	t       *testing.T
	Project *harness.Project
	file    string
	Spec    harness.RunSpec
	// HostPort is the dap relay on host 127.0.0.1.
	HostPort int
	// Launch is the DAP launch request a client sends.
	Launch harness.DebugLaunch
	// AppDebugIP is the app's address on the debug network.
	AppDebugIP string
}

func (r *DebugRun) compose(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"compose", "-f", r.file, "-p", r.Project.Name}, args...)
	cmd := exec.CommandContext(ctx, "docker", full...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.String(), err
}

// Exec runs script with sh in a service.
func (r *DebugRun) Exec(service, script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return r.compose(ctx, "exec", "-T", service, "sh", "-c", script)
}

// Logs returns the project's logs, for a failure message.
func (r *DebugRun) Logs() string {
	out, _ := r.compose(context.Background(), "logs", "--no-color")
	return out
}

// UpDebugRun builds and starts a debug run, removed at cleanup.
func UpDebugRun(t *testing.T) *DebugRun {
	t.Helper()
	NeedDocker(t)
	for _, img := range []string{BusyboxImage, PostgresImage} {
		ensureImage(t, img)
	}
	dlv := dlvBinary(t)
	aiman := edgeBinary(t)
	dir := t.TempDir()
	for _, d := range []string{"src", "out", "control"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	buildApp(t, filepath.Join(dir, "src"), filepath.Join(dir, "out", "app"))
	token := randomHex(t, 32)
	if err := os.WriteFile(filepath.Join(dir, "control", harness.ControlTokenName), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := harness.Parse([]byte(debugFile))
	if err != nil {
		t.Fatal(err)
	}
	launch, err := (&harness.GoDebug{}).Launch(f, harness.DebugNet{})
	if err != nil {
		t.Fatal(err)
	}
	inner, outer, dbg := randomSubnets(t)
	spec := harness.RunSpec{
		RunID: "d" + randomHex(t, 5), Scenario: "dbg",
		Src: filepath.Join(dir, "src"), Out: filepath.Join(dir, "out"), ControlDir: filepath.Join(dir, "control"),
		AimanBin: aiman, UID: os.Getuid(), GID: os.Getgid(),
		AppImage: BusyboxImage, EdgeImage: BusyboxImage,
		InnerSubnet: inner, OuterSubnet: outer, ReadPassword: "ro-" + randomHex(t, 4),
		Debug: harness.GoDebugSpec(dlv), DebugSubnet: dbg,
	}
	p, err := harness.Compose(f, spec)
	if err != nil {
		t.Fatal(err)
	}
	app, _, _ := harness.DebugAddrs(dbg)
	r := &DebugRun{t: t, Project: p, file: filepath.Join(dir, "compose.yaml"), Spec: spec, Launch: launch, AppDebugIP: app.String()}
	if err := os.WriteFile(r.file, p.YAML, 0o600); err != nil {
		t.Fatal(err)
	}
	for rel, b := range p.Files {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o444); err != nil { //nolint:gosec // G306: postgres reads the init script as its own user
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if out, err := r.compose(ctx, "down", "-v", "--remove-orphans", "-t", "2"); err != nil {
			t.Logf("compose down: %v: %s", err, out)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if out, err := r.compose(ctx, "up", "-d", "--wait", "--wait-timeout", "120"); err != nil {
		t.Fatalf("compose up: %v: %s\n%s\n%s", err, out, r.Logs(), p.YAML)
	}
	r.HostPort = r.relayPort(t)
	return r
}

func (r *DebugRun) relayPort(t *testing.T) int {
	t.Helper()
	var dap harness.Relay
	for _, rl := range r.Project.Relays {
		if rl.Kind == harness.RelayKindDAP {
			dap = rl
		}
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(r.Spec.ControlDir, harness.ControlSocketName)); err == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := r.compose(ctx, "port", harness.ServiceEdge, strconv.Itoa(dap.Port))
	if err != nil {
		t.Fatalf("compose port: %v: %s", err, out)
	}
	ap, err := netip.ParseAddrPort(strings.TrimSpace(out))
	if err != nil || ap.Addr().String() != "127.0.0.1" {
		t.Fatalf("dap relay published on %q, want 127.0.0.1: %v", out, err)
	}
	return int(ap.Port())
}

// buildApp writes AppSource and builds it static with optimisation off and
// the source directory renamed to /src, as app.build does in the container.
func buildApp(t *testing.T, src, out string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte(AppSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module app\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "-buildvcs=false", "-gcflags=all=-N -l -trimpath="+src+"=>/src", "-o", out, ".") //nolint:gosec // G204: the test's own build
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOFLAGS=", "GOWORK=off")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the app: %v: %s", err, b)
	}
}

var (
	edgeOnce sync.Once
	edgePath string
	edgeErr  error
	dlvOnce  sync.Once
	dlvPath  string
	dlvErr   error
)

func cacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "aiman-harness-test")
	return dir, os.MkdirAll(dir, 0o700)
}

// edgeBinary builds cmd/aiman without cgo for the busybox edge. It goes to
// one fixed path in the user cache, renamed into place, so runs leave no
// copies behind.
func edgeBinary(t *testing.T) string {
	t.Helper()
	edgeOnce.Do(func() {
		dir, err := cacheDir()
		if err != nil {
			edgeErr = err
			return
		}
		root, err := exec.Command("go", "env", "GOMOD").Output()
		if err != nil {
			edgeErr = err
			return
		}
		tmp, err := os.CreateTemp(dir, "aiman-build-")
		if err != nil {
			edgeErr = err
			return
		}
		_ = tmp.Close()
		cmd := exec.Command("go", "build", "-o", tmp.Name(), "./cmd/aiman") //nolint:gosec // G204: the test's own build
		cmd.Dir = filepath.Dir(strings.TrimSpace(string(root)))
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			_ = os.Remove(tmp.Name())
			edgeErr = fmt.Errorf("build aiman: %w: %s", err, out)
			return
		}
		edgePath = filepath.Join(dir, "aiman-edge")
		edgeErr = os.Rename(tmp.Name(), edgePath)
	})
	if edgeErr != nil {
		t.Fatal(edgeErr)
	}
	return edgePath
}

// dlvBinary is the pinned Delve, built on this host through the same
// DlvTools serve uses and cached in the user cache.
func dlvBinary(t *testing.T) string {
	t.Helper()
	dlvOnce.Do(func() {
		dir, err := cacheDir()
		if err != nil {
			dlvErr = err
			return
		}
		tools := &harness.DlvTools{Dir: filepath.Join(dir, "tools"), Pin: harness.DefaultDlv}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		dlvPath, dlvErr = tools.Ensure(ctx)
	})
	if dlvErr != nil {
		t.Fatal(dlvErr)
	}
	return dlvPath
}

var pulls sync.Map

func ensureImage(t *testing.T, ref string) {
	t.Helper()
	once, _ := pulls.LoadOrStore(ref, &sync.Once{})
	once.(*sync.Once).Do(func() {
		if exec.Command("docker", "image", "inspect", ref).Run() == nil { //nolint:gosec // G204: a fixed test image
			return
		}
		if out, err := exec.Command("docker", "pull", ref).CombinedOutput(); err != nil { //nolint:gosec // G204: a fixed test image
			t.Fatalf("pull %s: %v: %s", ref, err, out)
		}
	})
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// randomSubnets picks inner and outer /25s and a debug /29 that other test
// runs are unlikely to hold.
func randomSubnets(t *testing.T) (inner, outer, debug netip.Prefix) {
	t.Helper()
	var r [2]byte
	if _, err := rand.Read(r[:]); err != nil {
		t.Fatal(err)
	}
	b, c := 140+r[0]%100, r[1]%254
	return netip.PrefixFrom(netip.AddrFrom4([4]byte{10, b, c, 0}), 25),
		netip.PrefixFrom(netip.AddrFrom4([4]byte{10, b, c, 128}), 25),
		netip.PrefixFrom(netip.AddrFrom4([4]byte{10, 240 + r[0]%10, c, 8}), 29)
}

// Transport carries DAP messages as JSON objects, over TCP or the console's
// WebSocket.
type Transport interface {
	Send(msg map[string]any) error
	Recv() (map[string]any, error)
}

// TCPTransport is a framed DAP connection.
type TCPTransport struct {
	C  net.Conn
	br *bufio.Reader
}

// DialDAP connects to a DAP port on 127.0.0.1.
func DialDAP(t *testing.T, port int) *TCPTransport {
	t.Helper()
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return &TCPTransport{C: c, br: bufio.NewReader(c)}
}

// Send writes one message.
func (c *TCPTransport) Send(msg map[string]any) error { return edge.WriteDAPMessage(c.C, msg) }

// Recv reads one message.
func (c *TCPTransport) Recv() (map[string]any, error) {
	_ = c.C.SetReadDeadline(time.Now().Add(60 * time.Second))
	body, err := edge.ReadDAPMessage(c.br, edge.MaxDAPMessage)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(body, &m)
}

// Session is a minimal DAP client over a Transport.
type Session struct {
	t      *testing.T
	tr     Transport
	seq    int
	events []map[string]any
}

// NewSession wraps tr.
func NewSession(t *testing.T, tr Transport) *Session { return &Session{t: t, tr: tr, seq: 1} }

// Request sends one request and returns its response, keeping the events
// that arrive before it.
func (s *Session) Request(command string, args any) (map[string]any, error) {
	seq := s.seq
	s.seq++
	if err := s.tr.Send(map[string]any{"seq": seq, "type": "request", "command": command, "arguments": args}); err != nil {
		return nil, err
	}
	for {
		m, err := s.tr.Recv()
		if err != nil {
			return nil, err
		}
		switch m["type"] {
		case "event":
			s.events = append(s.events, m)
		case "response":
			if rs, _ := m["request_seq"].(float64); int(rs) == seq {
				return m, nil
			}
		}
	}
}

// Must is Request that fails the test on an error or an unsuccessful reply.
func (s *Session) Must(command string, args any) map[string]any {
	s.t.Helper()
	m, err := s.Request(command, args)
	if err != nil {
		s.t.Fatalf("%s: %v", command, err)
	}
	if m["success"] != true {
		s.t.Fatalf("%s failed: %v", command, m["message"])
	}
	return m
}

// Event waits for the named event, taking it from those already seen first.
func (s *Session) Event(name string) map[string]any {
	s.t.Helper()
	for i, e := range s.events {
		if e["event"] == name {
			s.events = append(s.events[:i], s.events[i+1:]...)
			return e
		}
	}
	for {
		m, err := s.tr.Recv()
		if err != nil {
			s.t.Fatalf("waiting for %s: %v", name, err)
		}
		if m["type"] == "event" && m["event"] == name {
			return m
		}
	}
}

// HitBreakpoint runs the whole client flow against the debug run: launch,
// a breakpoint on tick's body, and the stop there with the counter in scope.
// It returns the top frame's source path.
func HitBreakpoint(t *testing.T, s *Session, launch harness.DebugLaunch) string {
	t.Helper()
	s.Must("initialize", map[string]any{"clientID": "test", "clientName": "harness test", "adapterID": launch.Lang,
		"pathFormat": "path", "linesStartAt1": true, "columnsStartAt1": true})
	s.Must("launch", launch.Arguments)
	s.Event("initialized")
	bp := s.Must("setBreakpoints", map[string]any{
		"source":      map[string]any{"path": launch.SourceRoot + "/main.go"},
		"breakpoints": []map[string]any{{"line": BreakLine}},
	})
	bps, _ := bp["body"].(map[string]any)["breakpoints"].([]any)
	if len(bps) != 1 || bps[0].(map[string]any)["verified"] != true {
		t.Fatalf("breakpoint not verified: %v", bp)
	}
	s.Must("configurationDone", map[string]any{})
	stopped := s.Event("stopped")
	body, _ := stopped["body"].(map[string]any)
	if body["reason"] != "breakpoint" {
		t.Fatalf("stopped for %v, want breakpoint", body)
	}
	tid := body["threadId"]
	st := s.Must("stackTrace", map[string]any{"threadId": tid, "levels": 5})
	frames, _ := st["body"].(map[string]any)["stackFrames"].([]any)
	if len(frames) == 0 {
		t.Fatalf("no frames: %v", st)
	}
	top := frames[0].(map[string]any)
	if line, _ := top["line"].(float64); int(line) != BreakLine || !strings.HasSuffix(fmt.Sprint(top["name"]), "tick") {
		t.Fatalf("top frame %v, want tick at line %d", top, BreakLine)
	}
	src, _ := top["source"].(map[string]any)
	sc := s.Must("scopes", map[string]any{"frameId": top["id"]})
	scopes, _ := sc["body"].(map[string]any)["scopes"].([]any)
	found := false
	for _, x := range scopes {
		ref := x.(map[string]any)["variablesReference"]
		vs := s.Must("variables", map[string]any{"variablesReference": ref})
		vars, _ := vs["body"].(map[string]any)["variables"].([]any)
		for _, v := range vars {
			if v.(map[string]any)["name"] == "counter" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("variable counter not in scope %v", scopes)
	}
	s.Must("continue", map[string]any{"threadId": tid})
	return fmt.Sprint(src["path"])
}
