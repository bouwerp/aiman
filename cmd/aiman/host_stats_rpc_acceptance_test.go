package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/bouwerp/aiman/internal/infra/config"
)

// The CLI side of the serve load guard (docs/specs/serve-load-guard.md rev 3,
// Behaviour 3, piece P2): `aiman host stats` shows the rpc window, the client
// retries server_busy and rate_limited once, and host.load reaches serve.

const statsRPCResult = `{"id":"1","result":{"type":"host_stats","rpc":{` +
	`"window_seconds":60,"total":420,"inflight":6,"waiting":20,"rejected":0,"limited":1,"sweep_skipped":14,` +
	`"by_method":[{"name":"session.read","count":300,"avg_us":900},{"name":"session.wait","count":100,"avg_us":1200}],` +
	`"by_caller":[{"name":"sess-c","count":300,"avg_us":900},{"name":"operator","count":120,"avg_us":400}]}}}`

const (
	busyOnce    = `{"id":"1","error":{"code":"server_busy","message":"serve is busy","data":{"retry_after_ms":10}}}`
	limitedOnce = `{"id":"1","error":{"code":"rate_limited","message":"caller sess-c over budget: 300/min","data":{"retry_after_ms":10}}}`
)

// fakeServe answers one request per connection, the i-th with answers[i] (the
// last answer repeats), and counts the requests.
func fakeServe(t *testing.T, answers ...string) *atomic.Int32 {
	t.Helper()
	dir, err := os.MkdirTemp("", "hst")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var n atomic.Int32
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			i := int(n.Add(1)) - 1
			_, _ = bufio.NewReader(c).ReadBytes('\n')
			_, _ = c.Write([]byte(answers[min(i, len(answers)-1)] + "\n"))
			_ = c.Close()
		}
	}()
	t.Setenv("AIMAN_SOCKET_PATH", sock)
	t.Setenv("AIMAN_ID", "")
	return &n
}

func TestHostStatsJSONCarriesRPC(t *testing.T) {
	fakeServe(t, statsRPCResult)
	var runErr error
	stdout, stderr := captureStdIO(t, func() { runErr = runHost([]string{"stats", "--json"}) })
	if runErr != nil {
		t.Fatalf("host stats --json: %v (stderr %q)", runErr, stderr)
	}
	var out struct {
		RPC map[string]json.RawMessage `json:"rpc"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil || out.RPC == nil {
		t.Fatalf("stdout has no rpc object: %v\n%s", err, stdout)
	}
	for _, k := range []string{"window_seconds", "total", "inflight", "waiting", "rejected", "limited", "sweep_skipped", "by_method", "by_caller"} {
		if _, ok := out.RPC[k]; !ok {
			t.Errorf("rpc lacks %q: %s", k, stdout)
		}
	}
}

func TestHostStatsTextPrintsRPCBlock(t *testing.T) {
	fakeServe(t, statsRPCResult)
	var runErr error
	stdout, stderr := captureStdIO(t, func() { runErr = runHost([]string{"stats"}) })
	if runErr != nil {
		t.Fatalf("host stats: %v (stderr %q)", runErr, stderr)
	}
	for _, want := range []string{"rpc", "total/min", "inflight", "limited", "session.read", "session.wait"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("text output lacks %q:\n%s", want, stdout)
		}
	}
}

func TestClientRetriesServerBusyOnce(t *testing.T) {
	n := fakeServe(t, busyOnce, statsRPCResult)
	var runErr error
	_, stderr := captureStdIO(t, func() { runErr = runHost([]string{"stats", "--json"}) })
	if runErr != nil {
		t.Fatalf("host stats after one server_busy: %v (stderr %q)", runErr, stderr)
	}
	if got := n.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2 (one retry)", got)
	}
}

func TestClientGivesUpAfterSecondRateLimit(t *testing.T) {
	n := fakeServe(t, limitedOnce, limitedOnce, statsRPCResult)
	var runErr error
	_, stderr := captureStdIO(t, func() { runErr = runHost([]string{"stats", "--json"}) })
	if runErr == nil {
		t.Fatal("host stats succeeded after two rate_limited answers")
	}
	if !strings.Contains(runErr.Error(), "rate limited") && !strings.Contains(stderr, "rate limited") {
		t.Fatalf("error %q (stderr %q) does not say rate limited", runErr, stderr)
	}
	if got := n.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2 (one retry, then the error)", got)
	}
}

func TestHostLoadReachesServeConfig(t *testing.T) {
	nice := 5
	cfg := &config.Config{Host: config.HostConfig{Load: config.HostLoadConfig{
		MaxInflight: 8, PerCallerPerMinute: 120, AgentNice: &nice,
	}}}
	body, err := cfg.MarshalServeConfig("h1")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"load:", "max_inflight: 8", "per_caller_per_minute: 120", "agent_nice: 5"} {
		if !strings.Contains(string(body), key) {
			t.Errorf("serve config lacks %q:\n%s", key, body)
		}
	}
	var serve config.ServeConfig
	if err := yaml.Unmarshal(body, &serve); err != nil {
		t.Fatal(err)
	}
	l := serve.Host.Load
	if l.MaxInflight != 8 || l.PerCallerPerMinute != 120 || l.AgentNice == nil || *l.AgentNice != 5 {
		t.Fatalf("serve Host.Load = %+v", l)
	}
	empty, err := (&config.Config{}).MarshalServeConfig("h1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(empty), "agent_nice") {
		t.Errorf("an unset agent_nice was written:\n%s", empty)
	}
}
