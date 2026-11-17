package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/harness/edge"
)

// stubGoDebug is the Go adapter with a fixed dlv path, so no build runs.
type stubGoDebug struct {
	GoDebug
	err error
}

func (s *stubGoDebug) Spec(context.Context, *File) (*DebugSpec, error) {
	if s.err != nil {
		return nil, s.err
	}
	return GoDebugSpec("/opt/aiman-tools/dlv"), nil
}

// holdingEdge is an edge whose DAP relay has a holder until dropped.
type holdingEdge struct {
	mu      sync.Mutex
	holder  *edge.DAPHolder
	drops   []string
	targets []string
}

func (e *holdingEdge) Health(context.Context, string, string) (edge.Health, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return edge.Health{Outer: "10.0.0.2", Relays: []edge.RelayStatus{
		{RelaySpec: edge.RelaySpec{Name: "web"}},
		{RelaySpec: edge.RelaySpec{Name: RelayDAP, Kind: edge.KindDAP}, Holder: e.holder},
	}}, nil
}

func (e *holdingEdge) DAPDrop(_ context.Context, _, _, relay string) (*edge.DAPHolder, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.drops = append(e.drops, relay)
	h := e.holder
	e.holder = nil
	return h, nil
}

func (e *holdingEdge) DAPTarget(_ context.Context, _, _, _, target string) (*edge.DAPHolder, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.targets = append(e.targets, target)
	h := e.holder
	e.holder = nil
	return h, nil
}

func (e *holdingEdge) setHolder(h *edge.DAPHolder) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.holder = h
}

func (e *holdingEdge) targetsSeen() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.targets...)
}

const debugHarnessFile = `version: 1
app:
  build: go build -o /out/app .
  run: /out/app serve --addr :8080
  ports: [{name: web, container: 8080}]
  debug: {lang: go}
scenarios:
  - id: plain
`

func TestManagerDebugRun(t *testing.T) {
	he := &holdingEdge{holder: &edge.DAPHolder{ClientName: "aiman console", Since: time.Now()}}
	tm := newTestManager(t, func(o *Options) {
		o.Edge = he
		o.DebugAdapters = []DebugAdapter{&stubGoDebug{}}
	})
	repo := testRepo(t, debugHarnessFile)
	run, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "plain", Debug: true, Actor: domain.EventActorUser})
	if err != nil {
		t.Fatal(err)
	}
	run = tm.Wait(context.Background(), run.ID, 5*time.Second)
	if run.State != StateReady || !run.Debug {
		t.Fatalf("run %s debug=%v: %s %s", run.State, run.Debug, run.FailedStep, run.Error)
	}
	if run.DebugLaunch == nil || run.DebugLaunch.Arguments["program"] != "/out/app" {
		t.Fatalf("debug launch %+v", run.DebugLaunch)
	}
	var names []string
	for _, p := range run.Ports {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "web,dap" {
		t.Fatalf("ports %v, want web and dap", names)
	}
	yml, err := os.ReadFile(filepath.Join(tm.dir, "s1", run.ID, "sandbox", "compose.yaml")) //nolint:gosec // G304: the test's run directory
	if err != nil || !strings.Contains(string(yml), "ipv4_address: 10.232.") || !strings.Contains(string(yml), "/opt/aiman-tools/dlv:"+DlvMount+":ro") {
		t.Fatalf("compose file lacks the debug setup: %v\n%s", err, yml)
	}
	if p := tm.rec.last(domain.EventHarnessStarted); p["debug"] != true {
		t.Fatalf("harness.started = %v", p)
	}

	st, err := tm.Debug(context.Background(), run.ID, DebugStatus, domain.EventActorUser)
	if err != nil || st.Holder == nil || st.Holder.ClientName != "aiman console" || st.HostPort != 41873 || st.Launch == nil {
		t.Fatalf("status = %+v, %v", st, err)
	}
	st, err = tm.Debug(context.Background(), run.ID, DebugTake, domain.EventActorUser)
	if err != nil || st.Dropped == nil || st.Holder != nil || len(he.drops) != 1 || he.drops[0] != RelayDAP {
		t.Fatalf("take = %+v, %v, drops %v", st, err, he.drops)
	}
	if p := tm.rec.last(domain.EventHarnessDebug); p["op"] != DebugTake || p["dropped"] != "aiman console" {
		t.Fatalf("harness.debug = %v", p)
	}
	if _, err := tm.Debug(context.Background(), run.ID, "pause", domain.EventActorUser); !errors.Is(err, ErrParams) {
		t.Fatalf("unknown op: %v", err)
	}
}

func TestManagerDebugRefusals(t *testing.T) {
	tm := newTestManager(t, func(o *Options) { o.DebugAdapters = []DebugAdapter{&stubGoDebug{}} })
	plain := testRepo(t, testHarnessFile)
	_, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: plain, Scenario: "plain", Debug: true})
	var he *Error
	if !errors.As(err, &he) || he.Code != CodeInvalid {
		t.Fatalf("debug without app.debug: %v", err)
	}
	bun := testRepo(t, strings.Replace(debugHarnessFile, "{lang: go}", "{lang: bun, entry: src/index.ts}", 1))
	if _, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: bun, Scenario: "plain", Debug: true}); !errors.As(err, &he) || he.Code != CodeDebugUnsupported || he.Data()["lang"] != "bun" {
		t.Fatalf("bun debug: %v", err)
	}
	run := tm.upReady(t, plain, "plain")
	if _, err := tm.Debug(context.Background(), run.ID, DebugStatus, domain.EventActorUser); !errors.Is(err, ErrParams) {
		t.Fatalf("debug status on a plain run: %v", err)
	}
}

func TestManagerDebugToolsFail(t *testing.T) {
	tm := newTestManager(t, func(o *Options) {
		o.DebugAdapters = []DebugAdapter{&stubGoDebug{err: errors.New("no go on this host")}}
	})
	repo := testRepo(t, debugHarnessFile)
	run, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: repo, Scenario: "plain", Debug: true})
	if err != nil {
		t.Fatal(err)
	}
	run = tm.Wait(context.Background(), run.ID, 5*time.Second)
	if run.State != StateFailed || run.FailedStep != "debugger" || !strings.Contains(run.Error, "no go on this host") {
		t.Fatalf("run %s step %q: %s", run.State, run.FailedStep, run.Error)
	}
	if tm.docker.called("up") != 0 {
		t.Fatal("compose ran although the debugger was not ready")
	}
}
