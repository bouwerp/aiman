package harness

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness/tools"
)

const (
	testBunDir    = "/home/u/.aiman/harness/tools/bun-v1.3.14-linux-amd64"
	testBunDAPDir = "/home/u/.aiman/harness/tools/bun-dap-v1.3.14-linux-amd64"
	testPrefix    = "0123456789abcdef0123456789abcdef"
	bunDebugBlock = "bun, entry: src/index.ts, cwd: api"
)

func bunSpec(t *testing.T, fileYAML, prefix string) (*File, RunSpec) {
	t.Helper()
	f, err := Parse([]byte(fileYAML))
	if err != nil {
		t.Fatal(err)
	}
	ds, err := BunDebugSpec(testBunDir, testBunDAPDir, f, prefix, "1.3")
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec()
	spec.Debug = ds
	spec.DebugSubnet = netip.MustParsePrefix("10.232.1.0/29")
	spec.AdapterImage = "sha256:adapter"
	return f, spec
}

func TestBunPlan(t *testing.T) {
	file := debugFixture(bunDebugBlock)
	f, spec := bunSpec(t, file, testPrefix)
	p, doc := buildDebug(t, file, spec)

	// The app: Bun's inspector on the debug address under the prefix,
	// waiting for the debugger, with the mounted Bun after the image's own.
	app := svcOf(t, doc, ServiceApp)
	env, _ := app["environment"].(map[string]any)
	if env[InspectEnv] != "10.232.1.2:6499/"+testPrefix {
		t.Fatalf("app %s = %v", InspectEnv, env[InspectEnv])
	}
	cmd := strings.Join(strs(app["command"]), " ")
	for _, want := range []string{
		`'bun' --inspect-wait="$$AIMAN_INSPECT" '/src/src/index.ts'`, "cd '/src/api'",
		`PATH="$$PATH:` + BunToolMount + `"`, "ld-musl",
	} {
		if !strings.Contains(cmd, want) {
			t.Fatalf("app command %q lacks %q", cmd, want)
		}
	}
	if vols := strs(app["volumes"]); !slices.Contains(vols, testBunDir+":"+BunToolMount+":ro") || slices.ContainsFunc(vols, func(v string) bool { return strings.Contains(v, "bun-dap") }) {
		t.Fatalf("app volumes %v: want bun read-only and no bun-dap", vols)
	}

	// The adapter: bun-dap on the pinned Bun, on its debug address.
	ad := svcOf(t, doc, ServiceAdapter)
	acmd := strings.Join(strs(ad["command"]), " ")
	if !strings.Contains(acmd, BunToolMount+"/bun "+BunDAPMount+"/bun-dap.js --listen") || !strings.Contains(acmd, "$$"+AdapterHostEnv+":8124") {
		t.Fatalf("adapter command %q", acmd)
	}
	vols := strs(ad["volumes"])
	for _, want := range []string{testBunDir + ":" + BunToolMount + ":ro", testBunDAPDir + ":" + BunDAPMount + ":ro", testSpec().Src + ":/src:ro"} {
		if !slices.Contains(vols, want) {
			t.Fatalf("adapter volumes %v lack %s", vols, want)
		}
	}
	if nets, _ := ad["networks"].(map[string]any); len(nets) != 1 || nets[NetworkDebug] == nil {
		t.Fatalf("adapter networks %v, want the debug network only", nets)
	}
	var dap Relay
	for _, r := range p.Relays {
		if r.Kind == RelayKindDAP {
			dap = r
		}
	}
	if dap.Target != "10.232.1.3:8124" || dap.Service != ServiceAdapter {
		t.Fatalf("dap relay = %+v", dap)
	}

	// The launch attaches to the inspector URL with the same prefix.
	l, err := (&BunDebug{}).Launch(f, DebugNetOf(spec.DebugSubnet), spec.Debug)
	if err != nil {
		t.Fatal(err)
	}
	if l.Lang != "bun" || l.Request != "attach" || l.Children || l.SourceRoot != "/src" || !l.EditorPort() {
		t.Fatalf("launch = %+v", l)
	}
	if l.Arguments["type"] != "bun" || l.Arguments["url"] != "ws://10.232.1.2:6499/"+testPrefix {
		t.Fatalf("attach = %v", l.Arguments)
	}
	cfg, err := EditorConfig(l, 4711)
	if err != nil || cfg["debugServer"] != 4711 || cfg["type"] != "bun" || cfg[LocalRootKey] != "${workspaceFolder}" || cfg["url"] != l.Arguments["url"] {
		t.Fatalf("editor = %v, %v", cfg, err)
	}

	// A whole debug command runs as written.
	run := debugFixture("bun, run: 'bun --inspect-wait=$AIMAN_INSPECT api/src/index.ts'")
	_, rspec := bunSpec(t, run, testPrefix)
	_, doc = buildDebug(t, run, rspec)
	if cmd := strings.Join(strs(svcOf(t, doc, ServiceApp)["command"]), " "); !strings.Contains(cmd, "bun --inspect-wait=$$AIMAN_INSPECT api/src/index.ts") {
		t.Fatalf("app command %q", cmd)
	}

	// --watch and --hot never reach compose.
	for _, block := range []string{"bun, run: 'bun --watch --inspect-wait=$AIMAN_INSPECT x.ts'", "bun, entry: x.ts, runtime_args: [--hot]"} {
		if _, err := Parse([]byte(debugFixture(block))); err == nil {
			t.Fatalf("%s parsed", block)
		}
	}
}

func TestBunPrefixRandom(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		p, err := NewBunPrefix()
		if err != nil || !bunPrefixRe.MatchString(p) || seen[p] {
			t.Fatalf("prefix %q (%v), seen before %v", p, err, seen[p])
		}
		seen[p] = true
	}

	// Two runs of the same file get different prefixes from Spec, and each
	// launch names its own run's.
	b := &BunDebug{Tools: fakeBunTools(t), Release: "dev"}
	pinFakes(b)
	f, err := Parse([]byte(debugFixture(bunDebugBlock)))
	if err != nil {
		t.Fatal(err)
	}
	var urls []string
	for range 2 {
		ds, err := b.Spec(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		if !bunPrefixRe.MatchString(ds.InspectPath) {
			t.Fatalf("spec prefix %q", ds.InspectPath)
		}
		l, err := b.Launch(f, DebugNetOf(netip.MustParsePrefix("10.232.3.0/29")), ds)
		if err != nil {
			t.Fatal(err)
		}
		url, _ := l.Arguments["url"].(string)
		if !strings.HasSuffix(url, "/"+ds.InspectPath) {
			t.Fatalf("launch url %q does not carry the spec's prefix %q", url, ds.InspectPath)
		}
		urls = append(urls, url)
	}
	if urls[0] == urls[1] {
		t.Fatalf("two runs share the inspector url %q", urls[0])
	}
	// A spec without a prefix cannot be launched, and compose refuses a
	// path that is not plain.
	if _, err := b.Launch(f, DebugNet{}, &DebugSpec{}); err == nil {
		t.Fatal("launch without a prefix")
	}
	if _, err := BunDebugSpec(testBunDir, testBunDAPDir, f, "short", "1.3"); err == nil {
		t.Fatal("a short prefix was accepted")
	}
	_, spec := bunSpec(t, debugFixture(bunDebugBlock), testPrefix)
	spec.Debug.InspectPath = "x/../y"
	if _, err := Compose(f, spec); err == nil {
		t.Fatal("compose took an inspector path with a slash")
	}
}

// fakeBunTools is a tools store whose downloads come from memory: a Bun
// zip and a bun-dap file whose sums the test pins through Pin overrides.
func fakeBunTools(t *testing.T) *tools.Store {
	t.Helper()
	return &tools.Store{Dir: t.TempDir(), Client: &http.Client{Transport: fakeTransport{}}}
}

type fakeTransport struct{}

func (fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body := fakeDAP
	if strings.HasSuffix(r.URL.Path, ".zip") {
		body = fakeBunZip()
	}
	return &http.Response{StatusCode: http.StatusOK, Body: nopCloser{bytes.NewReader(body)}, Request: r, Header: http.Header{}}, nil
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

var fakeDAP = []byte("// bun-dap\n")

func fakeBunZip() []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	h := &zip.FileHeader{Name: "bun-linux-x64/bun", Method: zip.Store, Modified: time.Unix(0, 0).UTC()}
	h.SetMode(0o755)
	w, _ := zw.CreateHeader(h)
	_, _ = w.Write([]byte("#!bun"))
	_ = zw.Close()
	return buf.Bytes()
}

func hexSum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// pinFakes pins the fake store's bytes; the pins in code are for the real
// archives.
func pinFakes(b *BunDebug) {
	b.Bun = tools.Pin{Version: "v1.3.99", SHA256: hexSum(fakeBunZip())}
	b.DAP = tools.Pin{Version: "v1.3.99", SHA256: hexSum(fakeDAP)}
}

func TestBunMinorMismatch(t *testing.T) {
	// The probe: the app's Bun must be of the adapter's minor.
	ok := "Container x Creating\nAIMAN_BUN_PATH=/usr/local/bin/bun\nAIMAN_BUN_VERSION=1.3.6\n"
	if err := CheckBunProbe(ok, "1.3"); err != nil {
		t.Fatalf("1.3.6 on a 1.3 adapter: %v", err)
	}
	newer := "AIMAN_BUN_PATH=/usr/local/bin/bun\nAIMAN_BUN_VERSION=1.4.4\n"
	err := CheckBunProbe(newer, "1.3")
	var he *Error
	if !errors.As(err, &he) || he.Code != CodeDebugUnsupported || he.Reason != ReasonBunVersion || he.Data()["reason"] != ReasonBunVersion {
		t.Fatalf("1.4.4 on a 1.3 adapter: %v", err)
	}
	for _, want := range []string{"Bun 1.4.4", "Bun 1.3", "Vitest", "Node", "P6d"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not name %q", err, want)
		}
	}
	// The mounted Bun on a musl image is refused; an image's own Bun on
	// musl is fine.
	musl := "AIMAN_BUN_PATH=" + BunToolMount + "/bun\nAIMAN_BUN_VERSION=1.3.14\nAIMAN_MUSL=1\n"
	if err := CheckBunProbe(musl, "1.3"); !errors.As(err, &he) || he.Reason != ReasonMusl {
		t.Fatalf("mounted bun on musl: %v", err)
	}
	if err := CheckBunProbe("AIMAN_BUN_PATH=/usr/local/bin/bun\nAIMAN_BUN_VERSION=1.3.14\nAIMAN_MUSL=1\n", "1.3"); err != nil {
		t.Fatalf("the image's own bun on musl: %v", err)
	}
	for _, bad := range []string{"AIMAN_BUN_PATH=\n", "AIMAN_BUN_PATH=/x/bun\nAIMAN_BUN_VERSION=oops\n"} {
		if err := CheckBunProbe(bad, "1.3"); err == nil {
			t.Fatalf("probe %q accepted", bad)
		}
	}

	// harness.up refuses an official Bun image tag of another minor before
	// anything starts.
	b := &BunDebug{}
	for image, refused := range map[string]bool{
		"oven/bun:1.3-slim": false, "oven/bun:1.3.14-alpine": false, "oven/bun:latest": false,
		"docker.io/oven/bun:1.3": false, "node:24": false, "oven/bun:1.3@sha256:" + strings.Repeat("a", 64): false,
		"oven/bun:1.4-slim": true, "oven/bun:1.2.20": true, "docker.io/oven/bun:1.4": true,
	} {
		f, err := Parse([]byte(strings.Replace(debugFixture(bunDebugBlock), "app:\n", "app:\n  image: "+image+"\n", 1)))
		if err != nil {
			t.Fatalf("%s: %v", image, err)
		}
		_, err = DebugAdapterFor(f, []DebugAdapter{&GoDebug{}, b})
		if got := errors.As(err, &he) && he.Code == CodeDebugUnsupported && he.Reason == ReasonBunVersion; got != refused {
			t.Fatalf("%s: err = %v, want refused %v", image, err, refused)
		}
	}
}

// stubBunDebug is the Bun adapter with fixed tool paths and prefix.
type stubBunDebug struct{ BunDebug }

func (*stubBunDebug) Spec(_ context.Context, f *File) (*DebugSpec, error) {
	return BunDebugSpec("/opt/aiman-tools/bun", "/opt/aiman-tools/bun-dap", f, testPrefix, "1.3")
}

const bunHarnessFile = `version: 1
app:
  image: oven/bun:1.3-slim
  run: bun src/index.ts
  ports: [{name: web, container: 3000}]
  debug: {lang: bun, entry: src/index.ts}
scenarios:
  - id: plain
`

func TestManagerBunProbeRefuses(t *testing.T) {
	for version, want := range map[string]string{"1.3.14": StateReady, "1.4.4": StateFailed} {
		tm := newTestManager(t, func(o *Options) {
			o.Images = &recordingVetter{}
			o.DebugAdapters = []DebugAdapter{&stubBunDebug{}}
		})
		tm.docker.answer = func(args []string) (string, int, bool) {
			if len(args) > 4 && args[0] == "run" && args[3] == ServiceApp && strings.Contains(strings.Join(args, " "), "AIMAN_BUN_PATH") {
				return "AIMAN_BUN_PATH=/usr/local/bin/bun\nAIMAN_BUN_VERSION=" + version + "\n", 0, true
			}
			return "", 0, false
		}
		repo := testRepo(t, bunHarnessFile)
		run, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "plain", Debug: true, Actor: domain.EventActorUser})
		if err != nil {
			t.Fatal(err)
		}
		run = tm.Wait(context.Background(), run.ID, 5*time.Second)
		if run.State != want {
			t.Fatalf("bun %s: run %s (%s: %s), want %s", version, run.State, run.FailedStep, run.Error, want)
		}
		if want == StateFailed {
			if run.FailedStep != "debug probe" || run.ErrorCode != CodeDebugUnsupported || !strings.Contains(run.Error, "Bun 1.4.4") {
				t.Fatalf("failed run = %+v", run)
			}
			if tm.docker.called("up -d "+ServiceApp) != 0 {
				t.Fatalf("the app started after the probe refused; calls %q", tm.docker.calls)
			}
			continue
		}
		if l := run.DebugLaunch; l == nil || l.Arguments["url"] == nil || !strings.HasSuffix(l.Arguments["url"].(string), "/"+testPrefix) {
			t.Fatalf("launch %+v", run.DebugLaunch)
		}
	}
}
