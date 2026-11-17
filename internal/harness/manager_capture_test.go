package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/bouwerp/aiman/internal/harness/edge"
)

// captureEdge is a fake edge with a capture proxy.
type captureEdge struct {
	fakeEdge
	mu    sync.Mutex
	flows []edge.Flow
	calls int
}

const testCAPEM = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"

func (e *captureEdge) Capture(_ context.Context, _, _ string, after int64, limit int) ([]edge.Flow, edge.CaptureInfo, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	info := edge.CaptureInfo{Network: "offline", Total: int64(len(e.flows)), Next: after}
	out := []edge.Flow{}
	for _, f := range e.flows {
		if f.ID > after && len(out) < limit {
			out = append(out, f)
			info.Next = f.ID
		}
	}
	return out, info, nil
}

func (e *captureEdge) CA(context.Context, string, string) ([]byte, error) {
	return []byte(testCAPEM), nil
}

const stubHarnessFile = `version: 1
app:
  build: echo built
  run: exec httpd -f -p 8080
  ports: [{name: web, container: 8080}]
scenarios:
  - id: refund
    stubs:
      - {host: api.stripe.com, path: /v1/refunds, status: 200, body_file: testdata/refund.json}
      - {host: api.example.com, path: /ping, body: pong}
`

func stubRepo(t *testing.T) string {
	t.Helper()
	repo := testRepo(t, stubHarnessFile)
	writeTestFile(t, filepath.Join(repo, "testdata", "refund.json"), `{"id":"re_1"}`)
	gitRun(t, repo, "add", "-A")
	gitRun(t, repo, "commit", "-q", "-m", "stub")
	return repo
}

func TestManagerCaptureOffline(t *testing.T) {
	ce := &captureEdge{flows: []edge.Flow{
		{ID: 1, Method: "POST", URL: "https://api.stripe.com/v1/refunds", Outcome: edge.OutcomeStub, Status: 200,
			RequestHeaders: map[string][]string{"Authorization": {"Bearer " + fakeStripeKey}}},
		{ID: 2, Method: "GET", URL: "https://example.com/", Outcome: edge.OutcomeOffline, Status: 502},
	}}
	tm := newTestManager(t, func(o *Options) { o.Edge = ce })
	run := tm.upReady(t, stubRepo(t), "refund")
	if run.Network != NetworkOffline {
		t.Fatalf("network %q", run.Network)
	}
	dir := filepath.Join(tm.dir, "s1", run.ID)

	// The edge starts before anything that may call out through it.
	tm.docker.mu.Lock()
	calls := slices.Clone(tm.docker.calls)
	tm.docker.mu.Unlock()
	edgeAt := slices.Index(calls, "up -d --wait edge")
	buildAt := slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, "run --rm app sh -c echo built") })
	if edgeAt < 0 || buildAt < 0 || edgeAt > buildAt {
		t.Fatalf("edge must start before the build; calls %q", calls)
	}

	compose, _ := os.ReadFile(filepath.Join(dir, "sandbox", "compose.yaml"))
	for _, want := range []string{"--network", "offline", "--stubs", ControlMount + "/" + StubsFile, "HTTPS_PROXY: http://edge:3128"} {
		if !strings.Contains(string(compose), want) {
			t.Errorf("compose.yaml misses %q", want)
		}
	}
	var stubs []edge.Stub
	b, err := os.ReadFile(filepath.Join(dir, "control", StubsFile))
	if err != nil || json.Unmarshal(b, &stubs) != nil || len(stubs) != 2 {
		t.Fatalf("stubs.json = %s, %v", b, err)
	}
	if string(stubs[0].Body) != `{"id":"re_1"}` || string(stubs[1].Body) != "pong" || stubs[0].Host != "api.stripe.com" {
		t.Fatalf("stubs = %+v", stubs)
	}
	if ca, err := os.ReadFile(filepath.Join(dir, CAFile)); err != nil || string(ca) != testCAPEM {
		t.Fatalf("ca.pem = %q, %v", ca, err)
	}

	flows, info, err := tm.Capture(context.Background(), run.ID, 0, 1)
	if err != nil || len(flows) != 1 || flows[0].ID != 1 || info.Next != 1 {
		t.Fatalf("live capture = %+v %+v %v", flows, info, err)
	}

	if err := tm.Down(context.Background(), run.ID, StopDown); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(filepath.Join(dir, CaptureFile))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "sk_live_") {
		t.Fatalf("capture.json keeps a secret: %s", saved)
	}
	flows, info, err = tm.Capture(context.Background(), run.ID, 1, 10)
	if err != nil || len(flows) != 1 || flows[0].ID != 2 || info.Next != 2 || info.Total != 2 {
		t.Fatalf("capture of a stopped run = %+v %+v %v", flows, info, err)
	}
}

func TestManagerOnlineNeedsRoots(t *testing.T) {
	roots := filepath.Join(t.TempDir(), "roots.pem")
	writeTestFile(t, roots, testCAPEM)
	tm := newTestManager(t, func(o *Options) {
		o.Edge = &captureEdge{}
		o.Network, o.UpstreamRoots = NetworkOnline, roots
	})
	run := tm.upReady(t, testRepo(t, testHarnessFile), "plain")
	if run.Network != NetworkOnline {
		t.Fatalf("network %q", run.Network)
	}
	compose, _ := os.ReadFile(filepath.Join(tm.dir, "s1", run.ID, "sandbox", "compose.yaml"))
	if !strings.Contains(string(compose), "online") || !strings.Contains(string(compose), roots+":"+edgeRootsPath+":ro") {
		t.Fatalf("online compose.yaml:\n%s", compose)
	}
	if p := tm.rec.last("harness.started"); p == nil || p["network"] != NetworkOnline {
		t.Fatalf("harness.started = %v", p)
	}
}

func TestManagerStubFileMissing(t *testing.T) {
	tm := newTestManager(t)
	_, err := tm.Up(context.Background(), UpRequest{Session: "s1", Repo: testRepo(t, stubHarnessFile), Scenario: "refund"})
	var he *Error
	if !asError(err, &he) || he.Code != CodeInvalid {
		t.Fatalf("up with a missing stub body file: %v", err)
	}
}

func asError(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}

// fakeStripeKey looks like a live Stripe key, built at run time so secret
// scanners do not flag the source.
var fakeStripeKey = "sk_" + "live_" + strings.Repeat("0", 24)
