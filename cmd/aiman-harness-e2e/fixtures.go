//go:build e2e

package main

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/harnesstest"
)

// Images the fixtures run beyond harnesstest's.
const (
	goImage   = "golang:1.26"
	nodeImage = "node:24-slim"
	// anvilImage is Foundry v1.8.1 pinned by digest, as the EVM Docker test.
	// bun12Image is oven/bun:1.2-slim (Bun 1.2.23) by digest: a reference
	// that names no minor, so only the debug probe in the app container
	// finds the Bun the adapter cannot debug.
	bun12Image = "oven/bun@sha256:9654aa08d4b7e778b84148921bab8edc1409c8d0a85707b8c801dd7cf1878971"
	anvilImage = "ghcr.io/foundry-rs/foundry@sha256:0c00cb0bda1ab1b91c9a6bf60f4c76c09c1a8870824b6d4718afbabacf6f9a17"
)

// fixture is one repository the driver can write and commit.
type fixture struct {
	// scenario is the default for --up.
	scenario string
	// images are pulled before serve starts, when missing.
	images []string
	write  func(ctx context.Context, repo string) error
}

var fixtures = map[string]fixture{
	"go": {scenario: "smoke", images: []string{goImage}, write: files(map[string]string{
		".aiman/harness.yaml": goFile,
		"go.mod":              "module fixture\n\ngo 1.26\n",
		"main.go":             harnesstest.AppSource,
		"web.go":              goWeb,
	})},
	"node": {scenario: "smoke", images: []string{nodeImage, harness.AdapterImage}, write: files(map[string]string{
		".aiman/harness.yaml": nodeFile,
		"package.json":        `{"name":"fixture","private":true}` + "\n",
		"src/app.ts":          harnesstest.TSSource,
		"dist/app.js":         harnesstest.TSCompiled,
		"dist/app.js.map":     harnesstest.TSMap,
	})},
	"bun": {scenario: "smoke", images: []string{harnesstest.BunImage, harness.AdapterImage}, write: writeBun},
	"python": {scenario: "smoke", images: []string{harnesstest.PythonImage}, write: files(map[string]string{
		".aiman/harness.yaml": pythonFile,
		"app.py":              harnesstest.PythonSource,
	})},
	"vitest":       {scenario: "smoke", images: []string{nodeImage, harness.AdapterImage}, write: writeVitest},
	"vitest-bun12": {scenario: "smoke", images: []string{bun12Image, harness.AdapterImage}, write: writeVitestBun12},
	"db": {scenario: "pg", images: []string{plainAppImage, "postgres:16"}, write: files(map[string]string{
		".aiman/harness.yaml": dbFile,
		"testdata/orders.sql": "create table orders (id int, status text, api_key text);\n" +
			"insert into orders values (42, 'paid', 'k-123'), (43, 'open', 'k-456');\n" +
			"create table customers (id int, name text, password text);\n" +
			"insert into customers values (1, 'Ada', 'hunter2');\n",
	})},
	"evm": {scenario: "chain", images: []string{plainAppImage, anvilImage}, write: files(map[string]string{
		".aiman/harness.yaml": evmFile(),
	})},
	"net": {scenario: "refund", images: []string{goImage}, write: files(map[string]string{
		".aiman/harness.yaml":  netFile,
		"go.mod":               "module fixture\n\ngo 1.26\n",
		"main.go":              netApp,
		"testdata/refund.json": `{"id":"re_1","status":"succeeded"}` + "\n",
	})},
}

func fixtureNames() []string {
	names := make([]string, 0, len(fixtures))
	for n := range fixtures {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// files writes fixed files and commits them.
func files(m map[string]string) func(context.Context, string) error {
	return func(_ context.Context, repo string) error {
		if err := harnesstest.WriteFiles(repo, m); err != nil {
			return err
		}
		return harnesstest.CommitRepo(repo)
	}
}

// goFile is a Go app with a web port and two checks, one failing. In a
// debug run Delve starts the app only while a client holds the debugger, so
// the web link answers once the Debug tab has connected.
const goFile = `version: 1
app:
  build: go build -o /out/app .
  run: /out/app
  ports: [{name: web, container: 8080}]
  debug: {lang: go}
scenarios:
  - id: smoke
    title: Go app answers on its web port
    steps:
      - do: Open the web link
        expect: The page says hello from the harness fixture
      - do: Reload the page
        expect: The hit count goes up by one
      - do: Set a breakpoint in tick (main.go line 9) in the Debug tab
        expect: The app stops in tick
    checks:
      - name: binary
        run: test -x /out/app && echo binary ok
      - name: deliberate-failure
        run: echo "this check fails on purpose" && exit 3
`

const goWeb = `package main

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

var hits atomic.Int64

func init() {
	go func() {
		_ = http.ListenAndServe(":8080", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintf(w, "hello from the harness fixture, hit %d\n", hits.Add(1))
		}))
	}()
}
`

// nodeFile runs the compiled TypeScript app in the Node image; the source
// map points the debugger at src/app.ts (tick's body is line 9).
const nodeFile = `version: 1
app:
  image: ` + nodeImage + `
  run: node dist/app.js
  debug: {lang: node, entry: dist/app.js}
scenarios:
  - id: smoke
    title: Node app ticks
    steps:
      - do: Open Logs for app
        expect: Lines "ticks 50", "ticks 100" appear every five seconds
      - do: Set a breakpoint in src/app.ts line 9
        expect: The app stops in tick with c.n in Variables
    checks:
      - name: syntax
        run: node --check dist/app.js && echo syntax ok
`

const bunFile = `version: 1
app:
  image: ` + harnesstest.BunImage + `
  run: bun src/index.ts
  ports: [{name: web, container: 3000}]
  debug: {lang: bun, entry: src/index.ts}
scenarios:
  - id: smoke
    title: Hono on Bun answers
    steps:
      - do: Open the web link
        expect: The page says "hits" and a number
      - do: Set a breakpoint in src/index.ts line 9
        expect: The app stops in bump
    checks:
      - name: version
        run: bun --version
`

func writeBun(ctx context.Context, repo string) error {
	hono, err := harnesstest.HonoPackage(ctx)
	if err != nil {
		return err
	}
	if err := harnesstest.WriteFiles(repo, map[string]string{
		".aiman/harness.yaml": bunFile,
		"src/index.ts":        harnesstest.BunSource,
	}); err != nil {
		return err
	}
	if err := harnesstest.CopyTree(hono, filepath.Join(repo, "node_modules", "hono")); err != nil {
		return err
	}
	return harnesstest.CommitRepo(repo)
}

const pythonFile = `version: 1
app:
  image: ` + harnesstest.PythonImage + `
  run: python3 app.py
  debug: {lang: python, entry: app.py}
scenarios:
  - id: smoke
    title: Python loop runs
    steps:
      - do: Set a breakpoint in app.py line 6
        expect: The app stops in bump
    checks:
      - name: compile
        run: python3 -m py_compile app.py && echo compiled
`

// vitestFile is harnesstest's Vitest fixture in the Node image, with the
// test both as a plain check and as a debug check.
const vitestFile = `version: 1
app:
  image: ` + nodeImage + `
  run: node dist/app.js
  debug: {lang: node, entry: dist/app.js}
scenarios:
  - id: smoke
    title: Vitest check
    steps:
      - do: Run the sum check
        expect: It exits 0
      - do: Debug check sum and set a breakpoint in tests/sum.test.ts line 6
        expect: The test stops at the expect
    checks:
      - name: sum
        run: node node_modules/vitest/vitest.mjs run tests/sum.test.ts
        debug:
          lang: node
          run: node node_modules/vitest/vitest.mjs run --inspect-brk=$AIMAN_INSPECT --no-file-parallelism --test-timeout=0 tests/sum.test.ts
`

// vitestBun12File runs the Vitest fixture's app on Bun 1.2 with the bun
// debugger: a debug run fails its probe (harness_debug_unsupported) and the
// Debug tab offers check debugging, where the check runs under Node.
const vitestBun12File = `version: 1
app:
  image: ` + bun12Image + `
  run: bun dist/app.js
  debug: {lang: bun, entry: dist/app.js}
scenarios:
  - id: smoke
    title: Bun 1.2 app with a Vitest check
    steps:
      - do: Start with the debugger and open the Debug tab
        expect: It says the debugger cannot attach and offers check debugging
    checks:
      - name: sum
        run: node node_modules/vitest/vitest.mjs run tests/sum.test.ts
        debug:
          lang: node
          run: node node_modules/vitest/vitest.mjs run --inspect-brk=$AIMAN_INSPECT --no-file-parallelism --test-timeout=0 tests/sum.test.ts
`

func writeVitestBun12(_ context.Context, repo string) error {
	pkgs, err := harnesstest.VitestPackages()
	if err != nil {
		return err
	}
	return harnesstest.WriteVitestRepo(repo, vitestBun12File, pkgs)
}

func writeVitest(_ context.Context, repo string) error {
	pkgs, err := harnesstest.VitestPackages()
	if err != nil {
		return err
	}
	return harnesstest.WriteVitestRepo(repo, vitestFile, pkgs)
}

// dbFile is a postgres service with a fixture. SQLite services are left
// out: a harness file cannot seed one (sqlite fixtures are refused) and a
// service takes no command of its own.
const dbFile = `version: 1
app:
  run: exec sleep 86400
services:
  db:
    image: postgres:16
    env: {POSTGRES_PASSWORD: test}
    ready: pg_isready -h 127.0.0.1 -U postgres
    database: {kind: postgres, name: app}
    root: true
    caps: [CHOWN, SETUID, SETGID, DAC_OVERRIDE, FOWNER]
    limits: {memory: 1g}
scenarios:
  - id: pg
    title: Orders in postgres
    services: [db]
    fixtures: [{sql: testdata/orders.sql, into: db}]
    steps:
      - do: Open the Database tab and pick public.orders
        expect: Two rows, api_key shown as [redacted]
`

// anvilDev0 is anvil's first dev account; anvilFirst and anvilSecond the
// addresses its first two creations get.
const (
	anvilDev0   = "0xf39fd6e51aad88f6f4ce6ab8827279cfffb92266"
	anvilFirst  = "5fbdb2315678afecb367f032d93f642f64180aa3"
	anvilSecond = "0xe7f1725e7734ce288f8367e1bb143e90bb3f0512"
)

// evmFile deploys a contract reverting with Error("nope") and one calling
// it, then calls the second, as the EVM Docker test does: three
// transactions, the last reverted through a nested call.
func evmFile() string {
	reverter, caller := evmContracts()
	from := `"from":"` + anvilDev0 + `"`
	var b strings.Builder
	b.WriteString("version: 1\napp:\n  run: exec sleep 86400\nservices:\n  chain:\n    image: " + anvilImage +
		"\n    evm: {kind: anvil}\nscenarios:\n  - id: chain\n    title: Anvil with a reverted call\n    services: [chain]\n    setup:\n")
	for _, tx := range []string{
		`{` + from + `,"data":"` + reverter + `"}`,
		`{` + from + `,"data":"` + caller + `"}`,
		`{` + from + `,"to":"` + anvilSecond + `","gas":"0x100000","data":"0x12345678"}`,
	} {
		// busybox wget ignores NO_PROXY; -Y off keeps the call off the proxy.
		b.WriteString("      - run: |\n          wget -Y off -q -O- --header 'Content-Type: application/json' --post-data '" +
			`{"jsonrpc":"2.0","id":1,"method":"eth_sendTransaction","params":[` + tx + `]}` + "' http://chain:8545/ && echo\n")
	}
	b.WriteString("    steps:\n      - do: Open the EVM tab\n        expect: Three transactions, the newest reverted\n" +
		"      - do: Open the reverted transaction\n        expect: The call tree shows Error(string) nope\n")
	return b.String()
}

func evmContracts() (reverter, caller string) {
	data := errorData("nope")
	// PUSH1 100 PUSH1 12 PUSH1 0 CODECOPY PUSH1 100 PUSH1 0 REVERT, then data.
	r := append([]byte{0x60, 0x64, 0x60, 0x0c, 0x60, 0x00, 0x39, 0x60, 0x64, 0x60, 0x00, 0xfd}, data...)
	addr, _ := hex.DecodeString(anvilFirst)
	// CALL(gas, reverter, 0, 0, 0, 0, 0), RETURNDATACOPY(0, 0, size), REVERT(0, size).
	c := []byte{0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x60, 0x00, 0x73}
	c = append(c, addr...)
	c = append(c, 0x5a, 0xf1, 0x3d, 0x60, 0x00, 0x60, 0x00, 0x3e, 0x3d, 0x60, 0x00, 0xfd)
	return deployCode(r), deployCode(c)
}

// errorData is the ABI encoding of Error(msg) for a message under 32 bytes:
// 100 bytes.
func errorData(msg string) []byte {
	out := []byte{0x08, 0xc3, 0x79, 0xa0}
	word := func(n int) []byte { b := make([]byte, 32); b[31] = byte(n); return b } //nolint:gosec // G115: n is at most 32
	out = append(out, word(32)...)
	out = append(out, word(len(msg))...)
	text := make([]byte, 32)
	copy(text, msg)
	return append(out, text...)
}

// deployCode wraps runtime code in init code that returns it.
func deployCode(runtime []byte) string {
	init := []byte{0x60, byte(len(runtime)), 0x80, 0x60, 0x0b, 0x60, 0x00, 0x39, 0x60, 0x00, 0xf3} //nolint:gosec // G115: runtime code is under 100 bytes
	return "0x" + hex.EncodeToString(append(init, runtime...))
}

// netFile is a Go app calling out every 3 seconds through the capture
// proxy: a stubbed Stripe refund, a stubbed ping and example.com, which
// offline answers the edge's 502 and online reaches the internet.
const netFile = `version: 1
app:
  build: go build -o /out/app .
  run: /out/app
  ports: [{name: web, container: 8080}]
scenarios:
  - id: refund
    title: Outbound calls through the capture proxy
    stubs:
      - {host: api.stripe.com, path: /v1/refunds, status: 200, body_file: testdata/refund.json}
      - {host: api.example.com, path: /ping, body: pong}
    steps:
      - do: Open the Network tab
        expect: POST api.stripe.com/v1/refunds is a stub with status 200 and Authorization [redacted]
      - do: Find GET example.com
        expect: Offline it is a 502 from the edge
`

const netApp = `package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	mu   sync.Mutex
	last = map[string]string{}
)

func call(method, url string) {
	req, err := http.NewRequest(method, url, strings.NewReader("amount=100"))
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer sk_test_fixture_secret_value")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	out := ""
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		out = "error: " + err.Error()
	} else {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		out = fmt.Sprintf("%d %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	mu.Lock()
	last[method+" "+url] = out
	mu.Unlock()
	fmt.Println(method, url, "->", out)
}

func main() {
	go func() {
		for {
			call("POST", "https://api.stripe.com/v1/refunds")
			call("GET", "https://api.example.com/ping")
			call("GET", "https://example.com/")
			call("GET", "http://169.254.169.254/latest/meta-data/")
			time.Sleep(3 * time.Second)
		}
	}()
	_ = http.ListenAndServe(":8080", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		for k, v := range last {
			fmt.Fprintf(w, "%s -> %s\n", k, v)
		}
	}))
}
`
