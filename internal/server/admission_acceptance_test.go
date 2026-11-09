package server

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/ptyruntime/ptyfake"
)

// Admission (docs/specs/serve-load-guard.md rev 3, Behaviour 3, piece P2).
//
// Contract pinned here beyond the design's Contracts block:
//   - dispatch runs admission itself, after decoding the request, so a direct
//     dispatch call (as scopeCall makes) is counted, capped and limited.
//   - (*Server).SetHostLoad(config.HostLoadConfig) applies host.load; a zero
//     field keeps its default (64 in flight, 300 per caller per minute).
//   - The bucket and the by_caller key come from the peer pid through the
//     sessionOfPID seam when it is set (else holderSessionOf), never from
//     req.Caller; a peer outside every holder is "operator" when it names no
//     caller, and is accounted under its caller (never limited) when it does.
//   - The capture budget (4) wraps PTYRuntime.CaptureScreen calls serve makes
//     for itself (captureLive).

// admCall dispatches one request on ctx as caller, from peer pid when set.
func admCall(ctx context.Context, t *testing.T, srv *Server, caller string, pid int, method string, params map[string]any) Response {
	t.Helper()
	b, err := json.Marshal(params)
	if err != nil {
		t.Error(err)
		return Response{}
	}
	line, err := json.Marshal(Request{ID: "1", Method: method, Params: b, Caller: caller})
	if err != nil {
		t.Error(err)
		return Response{}
	}
	return srv.dispatch(withPeerPID(ctx, pid, pid != scopeNoPID), line)
}

func errCode(resp Response) string {
	if resp.Error == nil {
		return ""
	}
	return resp.Error.Code
}

// retryAfterMS reads data.retry_after_ms from a refusal, or fails.
func retryAfterMS(t *testing.T, resp Response) int {
	t.Helper()
	if resp.Error == nil {
		t.Fatal("no error")
	}
	b, err := json.Marshal(resp.Error.Data)
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		RetryAfterMS *int `json:"retry_after_ms"`
	}
	if err := json.Unmarshal(b, &data); err != nil || data.RetryAfterMS == nil || *data.RetryAfterMS <= 0 {
		t.Fatalf("data = %s, want retry_after_ms > 0", b)
	}
	return *data.RetryAfterMS
}

func countOf(list []RPCCount, name string) int {
	for _, c := range list {
		if c.Name == name {
			return c.Count
		}
	}
	return 0
}

// capturePTY wraps the fake runtime: it counts CaptureScreen calls, tracks how
// many run at once, and can hold each one on gate or sleep for delay.
type capturePTY struct {
	*ptyfake.FakePTY
	gate   chan struct{}
	delay  time.Duration
	calls  atomic.Int32
	active atomic.Int32
	peak   atomic.Int32
}

func (c *capturePTY) CaptureScreen(id string) (string, error) {
	c.calls.Add(1)
	n := c.active.Add(1)
	defer c.active.Add(-1)
	for {
		p := c.peak.Load()
		if n <= p || c.peak.CompareAndSwap(p, n) {
			break
		}
	}
	if c.gate != nil {
		<-c.gate
	}
	time.Sleep(c.delay)
	return c.FakePTY.CaptureScreen(id)
}

// unsettledServer is a serve with sessions whose blank screens never settle,
// its runtime wrapped in a capturePTY.
func unsettledServer(t *testing.T, ids ...string) (*Server, *capturePTY) {
	t.Helper()
	rows := make([]domain.Session, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, scopeRow(id, ""))
	}
	srv, fake, _ := ptyServer(t, rows...)
	for _, id := range ids {
		fake.Add(ptyruntime.SessionInfo{ID: id, Name: id, Dir: "/w/" + id})
	}
	cp := &capturePTY{FakePTY: fake}
	srv.pty = cp
	return srv, cp
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRPCStatsCountsMethodAndHolderCaller(t *testing.T) {
	srv, _, _, _ := peerServer(t)
	ctx := context.Background()
	for _, c := range []struct {
		caller string
		pid    int
	}{
		{"", pidA},       // holder peer, no caller
		{scopeB, pidA},   // holder peer naming another session
		{scopeA, pidA},   // holder peer naming itself
		{"", scopeNoPID}, // no peer pid, no caller
		{"", pidOutside}, // peer outside every holder
	} {
		if resp := admCall(ctx, t, srv, c.caller, c.pid, "session.list", nil); resp.Error != nil {
			t.Fatalf("session.list as %q/%d: %+v", c.caller, c.pid, resp.Error)
		}
	}
	st := srv.RPCStats()
	if st.WindowSeconds != 60 {
		t.Errorf("WindowSeconds = %d, want 60", st.WindowSeconds)
	}
	if st.Total < 5 {
		t.Errorf("Total = %d, want >= 5", st.Total)
	}
	if n := countOf(st.ByMethod, "session.list"); n != 5 {
		t.Errorf("by_method session.list = %d, want 5 (%+v)", n, st.ByMethod)
	}
	if n := countOf(st.ByCaller, scopeA); n != 3 {
		t.Errorf("by_caller %s = %d, want 3 (%+v)", scopeA, n, st.ByCaller)
	}
	if n := countOf(st.ByCaller, "operator"); n != 2 {
		t.Errorf("by_caller operator = %d, want 2 (%+v)", n, st.ByCaller)
	}
	if n := countOf(st.ByCaller, scopeB); n != 0 {
		t.Errorf("by_caller %s = %d, want 0: the holder, not req.Caller, is the key", scopeB, n)
	}
}

func TestHostStatsCarriesRPC(t *testing.T) {
	srv, _, _, _ := peerServer(t)
	resp := admCall(context.Background(), t, srv, "", scopeNoPID, "host.stats", nil)
	if resp.Error != nil {
		t.Fatalf("host.stats: %+v", resp.Error)
	}
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		RPC *struct {
			WindowSeconds int        `json:"window_seconds"`
			ByMethod      []RPCCount `json:"by_method"`
		} `json:"rpc"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.RPC == nil || out.RPC.WindowSeconds != 60 {
		t.Fatalf("host.stats result has no rpc object: %s", b)
	}
}

func TestConcurrencyCapAnswersServerBusy(t *testing.T) {
	srv, cp := unsettledServer(t, "s1", "s2")
	cp.gate = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); close(cp.gate); wg.Wait() })
	srv.SetHostLoad(config.HostLoadConfig{MaxInflight: 2})
	for _, id := range []string{"s1", "s2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			admCall(ctx, t, srv, "", scopeNoPID, "session.wait", map[string]any{"id": id, "timeout_ms": 60000})
		}()
	}
	waitFor(t, "two waits inside a capture", func() bool { return cp.active.Load() == 2 })
	if got := srv.RPCStats().Inflight; got != 2 {
		t.Errorf("Inflight = %d, want 2", got)
	}
	start := time.Now()
	resp := admCall(context.Background(), t, srv, "", scopeNoPID, "pty.list", nil)
	if took := time.Since(start); took > 200*time.Millisecond {
		t.Errorf("refusal took %v, want within about 200ms", took)
	}
	if errCode(resp) != CodeServerBusy {
		t.Fatalf("third call = %+v, want %s", resp.Error, CodeServerBusy)
	}
	retryAfterMS(t, resp)
	if got := srv.RPCStats().Rejected; got < 1 {
		t.Errorf("Rejected = %d, want >= 1", got)
	}
}

func TestSessionWaitReleasesSlotBetweenTicks(t *testing.T) {
	srv, _ := unsettledServer(t, "s1")
	srv.SetHostLoad(config.HostLoadConfig{MaxInflight: 1})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		admCall(ctx, t, srv, "", scopeNoPID, "session.wait", map[string]any{"id": "s1", "timeout_ms": 60000})
	}()
	time.Sleep(100 * time.Millisecond)
	ok := 0
	for range 6 {
		resp := admCall(context.Background(), t, srv, "", scopeNoPID, "session.list", nil)
		if errCode(resp) != CodeServerBusy {
			ok++
		}
		time.Sleep(250 * time.Millisecond)
	}
	if ok == 0 {
		t.Fatal("session.list answered server_busy for 1.5s while one session.wait ran: the wait holds its slot between ticks")
	}
	if got := srv.RPCStats().Waiting; got != 1 {
		t.Errorf("Waiting = %d, want 1 while the wait sits between ticks", got)
	}
}

// burn makes n calls of method as caller from pid and returns how many were
// rate_limited.
func burn(t *testing.T, srv *Server, caller string, pid, n int, method string, params map[string]any) int {
	t.Helper()
	limited := 0
	for range n {
		if errCode(admCall(context.Background(), t, srv, caller, pid, method, params)) == CodeRateLimited {
			limited++
		}
	}
	return limited
}

func budgetServer(t *testing.T) *Server {
	t.Helper()
	srv, _, _, _ := peerServer(t)
	srv.SetHostLoad(config.HostLoadConfig{PerCallerPerMinute: 60})
	return srv
}

func TestPerCallerBudgetLimitsOneHolder(t *testing.T) {
	srv := budgetServer(t)
	if n := burn(t, srv, scopeA, pidA, 60, "pty.list", nil); n != 0 {
		t.Fatalf("%d of the first 60 calls were rate_limited, want 0 (burst 60)", n)
	}
	resp := admCall(context.Background(), t, srv, scopeA, pidA, "pty.list", nil)
	if errCode(resp) != CodeRateLimited {
		t.Fatalf("61st call = %+v, want %s", resp.Error, CodeRateLimited)
	}
	retryAfterMS(t, resp)
	if got := srv.RPCStats().Limited; got < 1 {
		t.Errorf("Limited = %d, want >= 1", got)
	}
	if n := burn(t, srv, scopeB, pidB, 1, "pty.list", nil); n != 0 {
		t.Error("another holder's call was limited by sess-a's bucket")
	}
}

func TestPerCallerBudgetFollowsTheHolderNotTheCaller(t *testing.T) {
	t.Run("empty caller shares the holder's bucket", func(t *testing.T) {
		srv := budgetServer(t)
		burn(t, srv, scopeC, pidC, 60, "pty.list", nil)
		if errCode(admCall(context.Background(), t, srv, "", pidC, "pty.list", nil)) != CodeRateLimited {
			t.Fatal("a holder peer with no caller escaped its holder's bucket")
		}
	})
	t.Run("naming another caller does not reset the bucket", func(t *testing.T) {
		srv := budgetServer(t)
		burn(t, srv, scopeD, pidD, 60, "pty.list", nil)
		if errCode(admCall(context.Background(), t, srv, scopeB, pidD, "pty.list", nil)) != CodeRateLimited {
			t.Fatal("a holder peer naming sess-b escaped its holder's bucket")
		}
	})
}

func TestOperatorIsNeverRateLimited(t *testing.T) {
	srv := budgetServer(t)
	if n := burn(t, srv, "", scopeNoPID, 200, "pty.list", nil); n != 0 {
		t.Fatalf("operator with no peer pid limited %d times", n)
	}
	if n := burn(t, srv, "", pidOutside, 200, "pty.list", nil); n != 0 {
		t.Fatalf("operator peer outside the holders limited %d times", n)
	}
	if n := burn(t, srv, scopeA, pidOutside, 200, "pty.list", nil); n != 0 {
		t.Fatalf("a process outside the holders naming a caller limited %d times", n)
	}
	if n := countOf(srv.RPCStats().ByCaller, scopeA); n != 200 {
		t.Errorf("by_caller %s = %d, want 200: an outside process is accounted under its caller", scopeA, n)
	}
}

func TestHooksAndMirrorAreExemptButCounted(t *testing.T) {
	srv := budgetServer(t)
	report := map[string]any{"id": scopeA, "state": string(domain.AgentStateWorking), "source": "lifecycle"}
	if n := burn(t, srv, scopeA, pidA, 200, "session.report_agent_session", report); n != 0 {
		t.Fatalf("session.report_agent_session limited %d times", n)
	}
	mirror := map[string]any{"session": scopeA, "items": []map[string]any{}}
	if n := burn(t, srv, scopeA, pidA, 200, "todo.mirror", mirror); n != 0 {
		t.Fatalf("todo.mirror limited %d times", n)
	}
	st := srv.RPCStats()
	for _, m := range []string{"session.report_agent_session", "todo.mirror"} {
		if n := countOf(st.ByMethod, m); n != 200 {
			t.Errorf("by_method %s = %d, want 200", m, n)
		}
	}
}

func TestCaptureBudgetBoundsConcurrentCaptures(t *testing.T) {
	ids := []string{"s0", "s1", "s2", "s3", "s4", "s5", "s6", "s7", "s8", "s9"}
	srv, cp := unsettledServer(t, ids...)
	cp.delay = 50 * time.Millisecond
	ctx := context.Background()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			admCall(ctx, t, srv, "", scopeNoPID, "session.wait", map[string]any{"id": id, "timeout_ms": 1200})
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		srv.sweepStates(ctx)
		srv.sweepStates(ctx)
	}()
	wg.Wait()
	if cp.calls.Load() == 0 {
		t.Fatal("no capture ran")
	}
	if peak := cp.peak.Load(); peak > 4 {
		t.Fatalf("%d captures ran at once, want at most 4", peak)
	}
}

func TestWaitReadsSweptStateWithoutCapturing(t *testing.T) {
	srv, cp := unsettledServer(t, "s1")
	cp.SetScreen("s1", realMenu)
	srv.sweepStates(context.Background())
	if st, ok := srv.cachedSessionState("s1"); !ok || st.State != string(domain.AgentStateWaitingInput) {
		t.Fatalf("sweep cached %+v %v, want waiting_input", st, ok)
	}
	// A capture now would read a blank, unsettled screen and the wait would
	// time out; only the swept state settles it.
	cp.SetScreen("s1", "")
	cp.calls.Store(0)
	resp := admCall(context.Background(), t, srv, "", scopeNoPID, "session.wait",
		map[string]any{"id": "s1", "until": "settled", "timeout_ms": 1500})
	if resp.Error != nil {
		t.Fatalf("session.wait: %+v", resp.Error)
	}
	if n := cp.calls.Load(); n != 0 {
		t.Fatalf("session.wait made %d captures, want 0: it reads sessionStates", n)
	}
}
