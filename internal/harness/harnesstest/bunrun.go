package harnesstest

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/harness"
	"github.com/bouwerp/aiman/internal/harness/tools"
)

// BunImage is the official Bun image the Bun debugger test runs the app in.
const BunImage = "oven/bun:1.3-slim"

// BunBreakLine is the line of bump's body in BunSource.
const BunBreakLine = 9

// BunSource is the Bun debuggee: a Hono app served by Bun's default-export
// server. A timer drives requests through Hono in process, so the
// breakpoint is hit without a client.
const BunSource = `// A tiny Hono app on Bun for the harness debugger test.
import { Hono } from "hono";

interface Count {
  n: number;
}

function bump(c: Count): Count {
  const next: Count = { n: c.n + 1 };
  return next;
}

let hits: Count = { n: 0 };
const app = new Hono();
app.get("/", (c) => {
  hits = bump(hits);
  return c.text("hits " + hits.n);
});

setInterval(() => {
  void app.request("/");
}, 100);

export default { port: 3000, fetch: app.fetch };
`

const bunDebugFile = `version: 1
app:
  image: ` + BunImage + `
  run: bun src/index.ts
  ports: [{name: web, container: 3000}]
  debug: {lang: bun, entry: src/index.ts}
scenarios:
  - id: dbg
`

// honoPin is the Hono package the fixture imports, from the npm registry,
// checked like a debug tool.
var honoPin = tools.Spec{
	Name: "hono", Version: "v4.13.14", Strip: 1,
	URL:    "https://registry.npmjs.org/hono/-/hono-4.13.14.tgz",
	SHA256: "7c8a0197c892b62fe137f226992798cdb57f2678f3d3161931a1ea6ec45e4599",
}

var (
	bunToolsOnce               sync.Once
	bunDir, bunDAPDir, honoDir string
	bunToolsErr                error
)

// bunTools are the pinned Bun, bun-dap and Hono, fetched and checked through
// the tools store and cached in the user cache. No aiman release carries
// this tree's bun-dap, so it is built on the host by build/bun-dap/build.sh
// with the pinned Bun, and must match tools.BunDAPSHA256.
func bunTools(t *testing.T) (string, string, string) {
	t.Helper()
	bunToolsOnce.Do(func() {
		bunToolsErr = func() error {
			dir, err := CacheDir()
			if err != nil {
				return err
			}
			store := &tools.Store{Dir: filepath.Join(dir, "tools")}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
			defer cancel()
			b, err := tools.Bun(runtime.GOARCH, tools.Pin{})
			if err != nil {
				return err
			}
			if bunDir, err = store.Ensure(ctx, b); err != nil {
				return err
			}
			built := filepath.Join(dir, "bun-dap", tools.BunDAPAsset(tools.BunVersion))
			if _, err := os.Stat(built); err != nil {
				if err := buildBunDAP(ctx, filepath.Join(bunDir, "bun"), filepath.Dir(built)); err != nil {
					return err
				}
			}
			d, err := tools.BunDAP("dev", tools.Pin{Version: tools.BunVersion, URL: built})
			if err != nil {
				return err
			}
			if bunDAPDir, err = store.Ensure(ctx, d); err != nil {
				return err
			}
			honoDir, err = store.Ensure(ctx, honoPin)
			return err
		}()
	})
	if bunToolsErr != nil {
		t.Fatal(bunToolsErr)
	}
	return bunDir, bunDAPDir, honoDir
}

func buildBunDAP(ctx context.Context, bun, out string) error {
	_, file, _, _ := runtime.Caller(0)
	script := filepath.Join(filepath.Dir(file), "..", "..", "..", "build", "bun-dap", "build.sh")
	cmd := exec.CommandContext(ctx, "bash", script, out) //nolint:gosec // G204: the repository's own build script
	cmd.Env = append(os.Environ(), "BUN="+bun)
	if b, err := cmd.CombinedOutput(); err != nil {
		return &buildError{err: err, out: string(b)}
	}
	return nil
}

type buildError struct {
	err error
	out string
}

func (e *buildError) Error() string { return "build bun-dap: " + e.err.Error() + ": " + e.out }

// CopyTree copies the regular files under src to dst, leaving out the tools
// store's manifest.
func CopyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755) //nolint:gosec // G301: the app container reads it as the host uid
		}
		if !d.Type().IsRegular() || rel == "tool.json" {
			return nil
		}
		b, err := os.ReadFile(p) //nolint:gosec // G304: the test's tools store
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644) //nolint:gosec // G306: as above
	})
}

// HonoPackage is the pinned Hono package the Bun fixture imports, fetched
// and checked through the tools store in the user cache; the directory is
// the unpacked package.
func HonoPackage(ctx context.Context) (string, error) {
	dir, err := CacheDir()
	if err != nil {
		return "", err
	}
	return (&tools.Store{Dir: filepath.Join(dir, "tools")}).Ensure(ctx, honoPin)
}

// UpBunDebugRun starts a Bun debug run of the Hono app in the official Bun
// image; bun-dap runs on the pinned Bun in aiman's adapter container.
func UpBunDebugRun(t *testing.T) *DebugRun {
	t.Helper()
	NeedDocker(t)
	for _, img := range []string{BusyboxImage, BunImage, harness.AdapterImage} {
		ensureImage(t, img)
	}
	bunPath, dapPath, honoPath := bunTools(t)
	aiman := edgeBinary(t)
	dir := t.TempDir()
	for _, d := range []string{"src", "src/src", "out", "control"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "src", "index.ts"), []byte(BunSource), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CopyTree(honoPath, filepath.Join(dir, "src", "node_modules", "hono")); err != nil {
		t.Fatal(err)
	}
	token := randomHex(t, 32)
	if err := os.WriteFile(filepath.Join(dir, "control", harness.ControlTokenName), []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := harness.Parse([]byte(bunDebugFile))
	if err != nil {
		t.Fatal(err)
	}
	b := &harness.BunDebug{}
	if err := b.Check(f); err != nil {
		t.Fatal(err)
	}
	prefix, err := harness.NewBunPrefix()
	if err != nil {
		t.Fatal(err)
	}
	ds, err := harness.BunDebugSpec(bunPath, dapPath, f, prefix, "1.3")
	if err != nil {
		t.Fatal(err)
	}
	inner, outer, dbg := randomSubnets(t)
	launch, err := b.Launch(f, harness.DebugNetOf(dbg), ds)
	if err != nil {
		t.Fatal(err)
	}
	spec := harness.RunSpec{
		RunID: "b" + randomHex(t, 5), Scenario: "dbg",
		Src: filepath.Join(dir, "src"), Out: filepath.Join(dir, "out"), ControlDir: filepath.Join(dir, "control"),
		AimanBin: aiman, UID: os.Getuid(), GID: os.Getgid(),
		AppImage: BunImage, EdgeImage: BusyboxImage, AdapterImage: harness.AdapterImage,
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

// HitBunBreakpoint runs the bun-dap flow on one connection: initialize,
// attach to the inspector, a breakpoint in the .ts source, configurationDone,
// then the stop in bump. It returns the top frame's source path.
func HitBunBreakpoint(t *testing.T, tr Transport, args map[string]any, tsPath string) (string, *Session) {
	t.Helper()
	s := NewSession(t, tr)
	s.Must("initialize", map[string]any{
		"clientID": "test", "clientName": "harness test", "adapterID": "bun",
		"pathFormat": "path", "linesStartAt1": true, "columnsStartAt1": true,
	})
	s.Start("attach", args)
	s.Event("initialized")
	s.Must("setBreakpoints", map[string]any{
		"source":      map[string]any{"path": tsPath},
		"breakpoints": []map[string]any{{"line": BunBreakLine}},
	})
	s.Must("configurationDone", map[string]any{})
	stopped := s.Event("stopped")
	body, _ := stopped["body"].(map[string]any)
	st := s.Must("stackTrace", map[string]any{"threadId": body["threadId"], "levels": 5})
	frames, _ := st["body"].(map[string]any)["stackFrames"].([]any)
	if len(frames) == 0 {
		t.Fatalf("no frames: %v", st)
	}
	top := frames[0].(map[string]any)
	if line, _ := top["line"].(float64); int(line) != BunBreakLine || top["name"] != "bump" {
		t.Fatalf("top frame %v, want bump at line %d of the .ts source (stopped %v)", top, BunBreakLine, body)
	}
	src, _ := top["source"].(map[string]any)
	return src["path"].(string), s
}
