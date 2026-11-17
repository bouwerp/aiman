package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// stubNodeDebug is the Node adapter with fixed tool paths, so nothing is
// downloaded.
type stubNodeDebug struct{ NodeDebug }

func (*stubNodeDebug) Spec(_ context.Context, f *File) (*DebugSpec, error) {
	return NodeDebugSpec("/opt/aiman-tools/node", "/opt/aiman-tools/js-debug", f)
}

const nodeHarnessFile = `version: 1
app:
  image: node:24
  build: npx tsc -p .
  run: node dist/server.js
  ports: [{name: web, container: 8080}]
  debug: {lang: node, entry: dist/server.js}
scenarios:
  - id: plain
`

func TestManagerNodeDebugRun(t *testing.T) {
	v := &recordingVetter{}
	tm := newTestManager(t, func(o *Options) {
		o.Images = v
		o.DebugAdapters = []DebugAdapter{&stubGoDebug{}, &stubNodeDebug{}}
	})
	repo := testRepo(t, nodeHarnessFile)
	run, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "plain", Debug: true, Actor: domain.EventActorUser})
	if err != nil {
		t.Fatal(err)
	}
	run = tm.Wait(context.Background(), run.ID, 5*time.Second)
	if run.State != StateReady {
		t.Fatalf("run %s: %s %s", run.State, run.FailedStep, run.Error)
	}
	// app.image replaces the picked image; the adapter image is vetted as
	// a builtin like the edge's.
	byRef := map[string]ImageRequest{}
	for _, r := range v.reqs {
		byRef[r.Ref] = r
	}
	if _, ok := byRef["node:24"]; !ok {
		t.Fatalf("app.image not asked for: %+v", v.reqs)
	}
	if r, ok := byRef[AdapterImage]; !ok || !r.Builtin {
		t.Fatalf("adapter image request %+v (asked %v)", r, ok)
	}
	if _, ok := byRef["golang:1.25"]; ok {
		t.Fatal("the picked image was asked for although app.image is set")
	}
	l := run.DebugLaunch
	if l == nil || l.Request != "attach" || !strings.HasPrefix(l.Arguments["address"].(string), "10.232.") {
		t.Fatalf("debug launch %+v", l)
	}
	yml, err := os.ReadFile(filepath.Join(tm.dir, "s1", run.ID, "sandbox", "compose.yaml")) //nolint:gosec // G304: the test's run directory
	if err != nil || !strings.Contains(string(yml), ServiceAdapter+":") || !strings.Contains(string(yml), "dapDebugServer.js") {
		t.Fatalf("compose file lacks the adapter: %v\n%s", err, yml)
	}
	if tm.docker.called("up -d "+ServiceAdapter) != 1 {
		t.Fatalf("adapter not started; calls %q", tm.docker.calls)
	}
}

func TestManagerNodeDebugCountsAdapterMemory(t *testing.T) {
	tm := newTestManager(t, func(o *Options) {
		o.DebugAdapters = []DebugAdapter{&stubNodeDebug{}}
		o.RunMemory = DefaultAppMemory + EdgeLimits.Memory + 100<<20
	})
	repo := testRepo(t, strings.Replace(nodeHarnessFile, "  image: node:24\n", "", 1))
	_, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "plain", Debug: true})
	var he *Error
	if !errors.As(err, &he) || he.Code != CodeTooLarge {
		t.Fatalf("err = %v, want harness_too_large with the adapter's memory", err)
	}
	// The same scenario without the debugger fits.
	if _, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "plain"}); err != nil {
		t.Fatal(err)
	}
}
