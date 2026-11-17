package harnesstest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/edge"
	"github.com/bouwerp/aiman/internal/harness/tools"
)

// TSBreakLine is the line of tick's body in TSSource. The compiled line is
// 3: tsc drops the interface, so only the source map gets this one right.
const TSBreakLine = 9

// TSSource is the TypeScript debuggee; TSCompiled and TSMap are what
// `tsc --sourceMap --target es2022 --module commonjs` (TypeScript 5.9.3)
// writes for it to dist/.
const TSSource = `// A tiny TypeScript app for the harness debugger test.

interface Counter {
  n: number;
  label: string;
}

function tick(c: Counter): Counter {
  const next: Counter = { n: c.n + 1, label: c.label };
  return next;
}

let state: Counter = { n: 0, label: "ticks" };
setInterval(() => {
  state = tick(state);
  if (state.n % 50 === 0) console.log(state.label, state.n);
}, 100);
`

// TSCompiled is dist/app.js.
const TSCompiled = `// A tiny TypeScript app for the harness debugger test.
function tick(c) {
    const next = { n: c.n + 1, label: c.label };
    return next;
}
let state = { n: 0, label: "ticks" };
setInterval(() => {
    state = tick(state);
    if (state.n % 50 === 0)
        console.log(state.label, state.n);
}, 100);
//# sourceMappingURL=app.js.map`

// TSMap is dist/app.js.map.
const TSMap = `{"version":3,"file":"app.js","sourceRoot":"","sources":["../src/app.ts"],"names":[],"mappings":"AAAA,uDAAuD;AAOvD,SAAS,IAAI,CAAC,CAAU;IACtB,MAAM,IAAI,GAAY,EAAE,CAAC,EAAE,CAAC,CAAC,CAAC,GAAG,CAAC,EAAE,KAAK,EAAE,CAAC,CAAC,KAAK,EAAE,CAAC;IACrD,OAAO,IAAI,CAAC;AACd,CAAC;AAED,IAAI,KAAK,GAAY,EAAE,CAAC,EAAE,CAAC,EAAE,KAAK,EAAE,OAAO,EAAE,CAAC;AAC9C,WAAW,CAAC,GAAG,EAAE;IACf,KAAK,GAAG,IAAI,CAAC,KAAK,CAAC,CAAC;IACpB,IAAI,KAAK,CAAC,CAAC,GAAG,EAAE,KAAK,CAAC;QAAE,OAAO,CAAC,GAAG,CAAC,KAAK,CAAC,KAAK,EAAE,KAAK,CAAC,CAAC,CAAC,CAAC;AAC5D,CAAC,EAAE,GAAG,CAAC,CAAC"}`

const nodeDebugFile = `version: 1
app:
  run: node dist/app.js
  debug: {lang: node, entry: dist/app.js}
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

var (
	nodeToolsOnce       sync.Once
	nodeDir, jsDebugDir string
	nodeToolsErr        error
)

// nodeTools are the pinned Node and js-debug, fetched and checked through
// the same tools store serve uses and cached in the user cache.
func nodeTools(t *testing.T) (string, string) {
	t.Helper()
	nodeToolsOnce.Do(func() {
		dir, err := cacheDir()
		if err != nil {
			nodeToolsErr = err
			return
		}
		store := &tools.Store{Dir: filepath.Join(dir, "tools")}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		n, err := tools.Node(runtime.GOARCH, tools.Pin{})
		if err != nil {
			nodeToolsErr = err
			return
		}
		j, err := tools.JSDebug(tools.Pin{})
		if err != nil {
			nodeToolsErr = err
			return
		}
		if nodeDir, nodeToolsErr = store.Ensure(ctx, n); nodeToolsErr != nil {
			return
		}
		jsDebugDir, nodeToolsErr = store.Ensure(ctx, j)
	})
	if nodeToolsErr != nil {
		t.Fatal(nodeToolsErr)
	}
	return nodeDir, jsDebugDir
}

// UpNodeDebugRun starts a Node debug run of the compiled TypeScript app in
// aiman's adapter image, which has no node of its own, so the app runs on
// the mounted Node; js-debug runs in the adapter container.
func UpNodeDebugRun(t *testing.T) *DebugRun {
	t.Helper()
	NeedDocker(t)
	for _, img := range []string{BusyboxImage, PostgresImage, harness.AdapterImage} {
		ensureImage(t, img)
	}
	nodePath, jsPath := nodeTools(t)
	aiman := edgeBinary(t)
	dir := t.TempDir()
	for _, d := range []string{"src", "src/src", "src/dist", "out", "control"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for rel, body := range map[string]string{"src/src/app.ts": TSSource, "src/dist/app.js": TSCompiled, "src/dist/app.js.map": TSMap} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	token := randomHex(t, 32)
	if err := os.WriteFile(filepath.Join(dir, "control", harness.ControlTokenName), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := harness.Parse([]byte(nodeDebugFile))
	if err != nil {
		t.Fatal(err)
	}
	ds, err := harness.NodeDebugSpec(nodePath, jsPath, f)
	if err != nil {
		t.Fatal(err)
	}
	inner, outer, dbg := randomSubnets(t)
	launch, err := (&harness.NodeDebug{}).Launch(f, harness.DebugNetOf(dbg), nil)
	if err != nil {
		t.Fatal(err)
	}
	spec := harness.RunSpec{
		RunID: "n" + randomHex(t, 5), Scenario: "dbg",
		Src: filepath.Join(dir, "src"), Out: filepath.Join(dir, "out"), ControlDir: filepath.Join(dir, "control"),
		AimanBin: aiman, UID: os.Getuid(), GID: os.Getgid(),
		AppImage: harness.AdapterImage, EdgeImage: BusyboxImage, AdapterImage: harness.AdapterImage,
		InnerSubnet: inner, OuterSubnet: outer, ReadPassword: "ro-" + randomHex(t, 4),
		Debug: ds, DebugSubnet: dbg,
	}
	p, err := harness.Compose(f, spec)
	if err != nil {
		t.Fatal(err)
	}
	net := harness.DebugNetOf(dbg)
	r := startRun(t, dir, p, spec, launch, net.App.String())
	r.AdapterDebugIP = net.Adapter.String()
	return r
}

// DialLease connects to a DAP port on 127.0.0.1 and sends the lease line.
func DialLease(t *testing.T, port int, token string) *TCPTransport {
	t.Helper()
	c := DialDAP(t, port)
	if _, err := fmt.Fprintf(c.C, "%s%s\n", edge.LeasePrefix, token); err != nil {
		t.Fatal(err)
	}
	return c
}

// Start sends one request and returns its seq; Await reads its response.
func (s *Session) Start(command string, args any) int {
	s.t.Helper()
	seq := s.seq
	s.seq++
	if err := s.tr.Send(map[string]any{"seq": seq, "type": "request", "command": command, "arguments": args}); err != nil {
		s.t.Fatalf("%s: %v", command, err)
	}
	return seq
}

// HitTSBreakpoint runs the js-debug flow: the root session attaches, js-debug
// asks for a child session with startDebugging, a second connection under
// the same lease opens it, and the breakpoint in the .ts source stops tick.
// dial opens each connection (with the lease line, or through the console's
// editor port), args is the root's attach request and tsPath the .ts
// source as the client names it. It returns the top frame's source path.
func HitTSBreakpoint(t *testing.T, dial func() Transport, request string, args map[string]any, tsPath string) (string, *Session) {
	t.Helper()
	return HitJSBreakpoint(t, dial, request, args, tsPath, TSBreakLine)
}

// HitJSBreakpoint is HitTSBreakpoint with a breakpoint on line of tsPath.
func HitJSBreakpoint(t *testing.T, dial func() Transport, request string, args map[string]any, tsPath string, line int) (string, *Session) {
	t.Helper()
	init := map[string]any{
		"clientID": "test", "clientName": "harness test", "adapterID": "pwa-node",
		"pathFormat": "path", "linesStartAt1": true, "columnsStartAt1": true, "supportsStartDebuggingRequest": true,
	}
	root := NewSession(t, dial())
	root.Must("initialize", init)
	root.Start(request, args)
	root.Event("initialized")
	root.Start("configurationDone", map[string]any{})
	sd := root.ReverseRequest("startDebugging")
	sdArgs, _ := sd["arguments"].(map[string]any)
	cfg, _ := sdArgs["configuration"].(map[string]any)
	if cfg["__pendingTargetId"] == nil {
		t.Fatalf("startDebugging without a pending target: %v", sd)
	}

	child := NewSession(t, dial())
	child.Must("initialize", init)
	child.Start(fmt.Sprint(sdArgs["request"]), cfg)
	child.Event("initialized")
	child.Must("setBreakpoints", map[string]any{
		"source":      map[string]any{"path": tsPath},
		"breakpoints": []map[string]any{{"line": line}},
	})
	child.Must("configurationDone", map[string]any{})
	stopped := child.Event("stopped")
	body, _ := stopped["body"].(map[string]any)
	st := child.Must("stackTrace", map[string]any{"threadId": body["threadId"], "levels": 5})
	frames, _ := st["body"].(map[string]any)["stackFrames"].([]any)
	if len(frames) == 0 {
		t.Fatalf("no frames: %v", st)
	}
	top := frames[0].(map[string]any)
	if got, _ := top["line"].(float64); int(got) != line {
		t.Fatalf("top frame %v, want line %d of the .ts source (stopped %v)", top, line, body)
	}
	src, _ := top["source"].(map[string]any)
	return fmt.Sprint(src["path"]), child
}
