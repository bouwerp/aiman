//go:build pileup_d

package server

// Contract (docs/specs/client-pileup-guard.md, Behaviour P3, Contracts):
//   - type PressureGate struct { Closed bool; Since time.Time;
//     MemorySomeAvg10, MemoryFullAvg10 float64; SliceOverHigh bool } with the
//     JSON tags closed, since, memory_some_avg10, memory_full_avg10,
//     slice_over_high.
//   - const CodeHostUnderPressure = "host_under_pressure". Its Error.Data
//     marshals to {retry_after_ms: 30000, memory_full_avg10,
//     memory_some_avg10, since}, the values of the gate when it answered.
//   - type PressureReading struct { Memory cgroup.Pressure; SliceOverHigh bool }:
//     one read of the user slice (memory.pressure, and memory.current >=
//     memory.high for SliceOverHigh).
//   - func (s *Server) SetPressureSource(read func() (PressureReading, error), now func() time.Time):
//     replaces the reader and the clock the gate uses. Works on a zero Server
//     (newHandoverTestServer builds one as a literal).
//   - func (s *Server) stepPressure(ctx context.Context): one read and one
//     step of the gate; runPressure calls it every 5 seconds.
//   - func (s *Server) Pressure() PressureGate: the gate's current state.
//   - The gate closes when full avg10 >= 20 and opens after 30 seconds in a
//     row of full avg10 < 10; some and SliceOverHigh never close it. Those
//     are the defaults with no host.load.pressure_gate config, so the gate
//     enforces without any SetHostLoad call.
//   - var ErrHostUnderPressure error, whose text is "host under pressure":
//     serverSessions.CreateChild returns it (wrapped or not) while the gate is
//     closed, without calling the creator.
//   - session.create and review.run take "force": true. From the operator it
//     passes a closed gate; from a session caller it is needs_operator.
//   - domain.NotifyHostPressure NotifyKind = "host_pressure", NeedsYou true.
//     Each closing emits one with an empty session id and the text
//     "<host> is under memory pressure: full <n>%; new sessions, reviews and
//     CI runs wait" (notify.Format returns that detail as the whole text), at
//     most once per 10 minutes.

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/cgroup"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/handoverchild"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/notify"
	"github.com/bouwerp/aiman/internal/review"
)

// fakePressure is the gate's reader and clock.
type fakePressure struct {
	mu  sync.Mutex
	r   PressureReading
	now time.Time
}

func (f *fakePressure) read() (PressureReading, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.r, nil
}

func (f *fakePressure) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakePressure) set(full, some float64, overHigh bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.r = PressureReading{Memory: cgroup.Pressure{SomeAvg10: some, FullAvg10: full}, SliceOverHigh: overHigh}
}

func (f *fakePressure) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

var pressureEpoch = time.Date(2026, 11, 13, 9, 30, 0, 0, time.UTC)

func withFakePressure(srv *Server) *fakePressure {
	f := &fakePressure{now: pressureEpoch}
	srv.SetPressureSource(f.read, f.clock)
	return f
}

// tick advances the clock by d, sets the reading and steps the gate.
func (f *fakePressure) tick(srv *Server, d time.Duration, full, some float64, overHigh bool) PressureGate {
	f.advance(d)
	f.set(full, some, overHigh)
	srv.stepPressure(context.Background())
	return srv.Pressure()
}

// closeGate drives the gate closed at full 35, some 80 and returns the time
// it closed.
func closeGate(t *testing.T, srv *Server, f *fakePressure) time.Time {
	t.Helper()
	g := f.tick(srv, 5*time.Second, 35, 80, true)
	if !g.Closed {
		t.Fatalf("gate open at full 35: %+v", g)
	}
	return f.clock()
}

// wantPressureRefusal checks a host_under_pressure answer and its data.
func wantPressureRefusal(t *testing.T, what string, resp Response, since time.Time) {
	t.Helper()
	if resp.Error == nil || resp.Error.Code != CodeHostUnderPressure {
		t.Fatalf("%s: want %s, got error %+v result %+v", what, CodeHostUnderPressure, resp.Error, resp.Result)
	}
	raw, err := json.Marshal(resp.Error.Data)
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		RetryAfterMS    *int64   `json:"retry_after_ms"`
		MemoryFullAvg10 *float64 `json:"memory_full_avg10"`
		MemorySomeAvg10 *float64 `json:"memory_some_avg10"`
		Since           string   `json:"since"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("%s: data %s: %v", what, raw, err)
	}
	if data.RetryAfterMS == nil || *data.RetryAfterMS != 30000 {
		t.Errorf("%s: retry_after_ms in %s, want 30000", what, raw)
	}
	if data.MemoryFullAvg10 == nil || *data.MemoryFullAvg10 != 35 {
		t.Errorf("%s: memory_full_avg10 in %s, want 35", what, raw)
	}
	if data.MemorySomeAvg10 == nil || *data.MemorySomeAvg10 != 80 {
		t.Errorf("%s: memory_some_avg10 in %s, want 80", what, raw)
	}
	if got, err := time.Parse(time.RFC3339Nano, data.Since); err != nil || !got.Equal(since) {
		t.Errorf("%s: since %q in %s, want %s", what, data.Since, raw, since.Format(time.RFC3339))
	}
}

func TestAcceptancePressureGateClosesOnFullAndReopensAfterHold(t *testing.T) {
	srv, _, _ := todoServer(t)
	f := withFakePressure(srv)

	if g := f.tick(srv, 0, 5, 3, false); g.Closed {
		t.Fatalf("closed at full 5: %+v", g)
	}
	if g := f.tick(srv, 5*time.Second, 19.9, 40, false); g.Closed {
		t.Fatalf("closed at full 19.9: %+v", g)
	}
	g := f.tick(srv, 5*time.Second, 20, 70, false)
	if !g.Closed {
		t.Fatalf("open at full 20 (the default threshold, no config): %+v", g)
	}
	closedAt := f.clock()
	if !g.Since.Equal(closedAt) || g.MemoryFullAvg10 != 20 || g.MemorySomeAvg10 != 70 {
		t.Fatalf("gate %+v, want since %s, full 20, some 70", g, closedAt)
	}

	// Under 10 for 25s, then a reading between 10 and 20 restarts the hold.
	for range 5 {
		if g := f.tick(srv, 5*time.Second, 9, 30, false); !g.Closed {
			t.Fatalf("opened before 30s under 10: %+v", g)
		}
	}
	if g := f.tick(srv, 5*time.Second, 10, 30, false); !g.Closed {
		t.Fatalf("opened at full 10, which is not under the reopen value: %+v", g)
	}
	if !srv.Pressure().Since.Equal(closedAt) {
		t.Fatalf("since moved while closed: %+v", srv.Pressure())
	}

	// A fresh run under 10: still closed 25s in, open once 30s have passed.
	if g := f.tick(srv, 5*time.Second, 2, 30, false); !g.Closed {
		t.Fatalf("opened on the first reading under 10: %+v", g)
	}
	for range 5 {
		if g := f.tick(srv, 5*time.Second, 2, 30, false); !g.Closed {
			t.Fatalf("opened inside the 30s hold: %+v", g)
		}
	}
	f.tick(srv, 5*time.Second, 2, 30, false)
	if g := f.tick(srv, 5*time.Second, 2, 30, false); g.Closed {
		t.Fatalf("still closed after 35s under 10: %+v", g)
	}
}

func TestAcceptancePressureGateIgnoresSomeAndSliceAtHigh(t *testing.T) {
	srv, _, _ := todoServer(t)
	f := withFakePressure(srv)
	for i := range 20 {
		g := f.tick(srv, 5*time.Second, 0, 99, i%2 == 0)
		if g.Closed {
			t.Fatalf("closed on some 99 / slice at memory.high with full 0: %+v", g)
		}
	}
	g := f.tick(srv, 5*time.Second, 1, 95, true)
	if g.Closed || !g.SliceOverHigh || g.MemorySomeAvg10 != 95 || g.MemoryFullAvg10 != 1 {
		t.Fatalf("gate %+v, want open and reporting slice_over_high, some 95, full 1", g)
	}
}

func TestAcceptancePressureGateRefusesSessionCreate(t *testing.T) {
	srv, _, _ := todoServer(t, todoSession("coord"))
	creator := &fakeCreator{}
	srv.create = creator
	f := withFakePressure(srv)
	since := closeGate(t, srv, f)

	params := map[string]any{"quick": true, "agent": "claude"}
	wantPressureRefusal(t, "operator create", rpc(t, srv, "", "session.create", params), since)
	wantPressureRefusal(t, "session create", rpc(t, srv, "coord", "session.create", params), since)
	if creator.calls != 0 {
		t.Fatalf("creator ran %d times behind a closed gate", creator.calls)
	}
}

func TestAcceptancePressureGateForceIsOperatorOnly(t *testing.T) {
	srv, _, _ := todoServer(t, todoSession("coord"))
	creator := &fakeCreator{}
	srv.create = creator
	f := withFakePressure(srv)
	closeGate(t, srv, f)

	resp := rpc(t, srv, "coord", "session.create", map[string]any{"quick": true, "agent": "claude", "force": true})
	if resp.Error == nil || resp.Error.Code != CodeNeedsOperator {
		t.Fatalf("force from a session: %+v", resp.Error)
	}
	if creator.calls != 0 {
		t.Fatalf("a session's force reached the creator")
	}
	resp = rpc(t, srv, "", "session.create", map[string]any{"quick": true, "agent": "claude", "force": true})
	if resp.Error != nil {
		t.Fatalf("operator force: %+v", resp.Error)
	}
	if creator.calls != 1 {
		t.Fatalf("creator calls %d after an operator force, want 1", creator.calls)
	}
}

func TestAcceptancePressureGateRefusesHelperSpawn(t *testing.T) {
	srv, _, _ := todoServer(t, todoSession("parent"))
	creator := &fakeCreator{}
	srv.create = creator
	f := withFakePressure(srv)
	closeGate(t, srv, f)

	helpers := serverSessions{s: srv, cleanupReason: "review_cleanup"}
	cfg := domain.SessionConfig{Name: "rev", Group: "g", ParentID: "parent", Role: domain.SessionRoleReviewer}
	_, err := helpers.CreateChild(context.Background(), cfg)
	if !errors.Is(err, ErrHostUnderPressure) {
		t.Fatalf("CreateChild behind a closed gate: %v, want ErrHostUnderPressure", err)
	}
	if creator.calls != 0 {
		t.Fatal("helper spawn reached the creator behind a closed gate")
	}

	for range 8 {
		f.tick(srv, 5*time.Second, 1, 5, false)
	}
	if srv.Pressure().Closed {
		t.Fatalf("gate still closed after 35s at full 1: %+v", srv.Pressure())
	}
	if _, err := helpers.CreateChild(context.Background(), cfg); err != nil {
		t.Fatalf("CreateChild with the gate open: %v", err)
	}
}

func TestAcceptancePressureGateRefusesReviewRun(t *testing.T) {
	r := newReviewRig(t)
	dock := &fakeDocker{out: "ok\n"}
	r.srv.SetReviewRunner(review.Runner{Docker: dock.run, UID: 1000, GID: 1000})
	r.srv.SetReviewConfig(config.ReviewConfig{}, map[string]config.RepoBootstrap{
		"o/r": {Review: config.RepoReview{Image: "golang:1.26"}},
	})
	info := createReview(t, r.srv)
	f := withFakePressure(r.srv)
	since := closeGate(t, r.srv, f)

	cmd := []string{"go", "test", "./..."}
	wantPressureRefusal(t, "review.run", rpc(t, r.srv, info.ID, "review.run", map[string]any{"cmd": cmd}), since)
	resp := rpc(t, r.srv, info.ID, "review.run", map[string]any{"cmd": cmd, "force": true})
	if resp.Error == nil || resp.Error.Code != CodeNeedsOperator {
		t.Fatalf("review.run force from the review session: %+v", resp.Error)
	}
	if dock.last() != nil {
		t.Fatalf("sandbox ran behind a closed gate: %v", dock.last())
	}
	rpcOK(t, r.srv, "", "review.run", map[string]any{"session": info.ID, "cmd": cmd, "force": true}, nil)
	if dock.last() == nil {
		t.Fatal("operator force did not run the sandbox")
	}
}

func TestAcceptancePressureGateDoesNotGateRestart(t *testing.T) {
	r := newLifecycleRig(t)
	s, eng := r.restartable("claude")
	f := withFakePressure(r.srv)
	closeGate(t, r.srv, f)

	got := lifecycleResult(t, r.call("session.restart", map[string]any{"session": s.ID, "agent": "claude"}))
	if got["type"] != "session" {
		t.Fatalf("restart behind a closed gate: %v", got)
	}
	if a := eng.last(t); a.Command != "claude" {
		t.Fatalf("launch %+v", a)
	}
}

// pressureChildSessions knows the worker and spawns the handover child
// through serve's own helper path, where the gate stands.
type pressureChildSessions struct {
	worker domain.Session
	inner  serverSessions
}

func (c pressureChildSessions) Get(_ context.Context, id string) (domain.Session, error) {
	if id == c.worker.ID {
		return c.worker, nil
	}
	return domain.Session{}, domain.ErrSessionNotFound
}

func (c pressureChildSessions) CreateChild(ctx context.Context, cfg domain.SessionConfig) (domain.Session, error) {
	return c.inner.CreateChild(ctx, cfg)
}

func (c pressureChildSessions) Forget(context.Context, string) error { return nil }

func TestAcceptancePressureGateHandoverWritesFileAndStartsNoChild(t *testing.T) {
	srv := newHandoverTestServer(t, paneText("$ go test"), todosWith("t1", "Ship it", "pending"), "")
	creator := &fakeCreator{}
	srv.create = creator
	sess := domain.Session{ID: "s1", Name: "WTB-12", WorkingDirectory: t.TempDir(), AgentName: "claude"}

	base := shortTempDir(t)
	store := handoverchild.NewStore(filepath.Join(base, "runs"))
	d := srv.HandoverDeps(store, func(string) bool { return true })
	d.Sessions = pressureChildSessions{worker: sess, inner: serverSessions{s: srv, cleanupReason: "child_cleanup"}}
	d.Terminal = deadTerminal{}
	d.Candidates = func(string) []config.PreferredAgent { return config.PreferredFromCommands("claude") }
	d.BaseDir = base
	d.Poll = 10 * time.Millisecond
	m := handoverchild.NewManager(d)
	srv.SetHandovers(m)
	t.Cleanup(m.Close)

	f := withFakePressure(srv)
	closeGate(t, srv, f)

	res, err := srv.writeHandover(context.Background(), sess, "archive")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Written {
		t.Fatalf("result %+v, want the mechanical file written", res)
	}
	if _, err := os.Stat(filepath.Join(sess.WorkingDirectory, domain.AimanHandoverFileName)); err != nil {
		t.Fatalf("mechanical handover file: %v", err)
	}
	eventually(t, 3*time.Second, func() bool {
		runs, err := store.List()
		if err != nil || len(runs) != 1 {
			return false
		}
		return runs[0].Status == handoverchild.StatusFailed && strings.Contains(runs[0].Error, "host under pressure")
	})
	if creator.calls != 0 {
		t.Fatalf("a handover child was created behind a closed gate (%d creates)", creator.calls)
	}
}

func TestAcceptancePressureGateNotifiesOncePerClosing(t *testing.T) {
	srv, _, _ := todoServer(t)
	store := &notifyStore{}
	srv.EnableNotifications(notify.NewRecorder(store, notify.Settings{
		Events: map[domain.NotifyKind]bool{domain.NotifyHostPressure: true}, Host: "h",
	}, nil), nil)
	if !domain.NeedsYou(domain.NotifyHostPressure) {
		t.Fatal("host_pressure must need the operator")
	}
	f := withFakePressure(srv)
	rows := func() []domain.Notification {
		store.mu.Lock()
		defer store.mu.Unlock()
		return append([]domain.Notification(nil), store.rows...)
	}
	reopen := func() {
		t.Helper()
		for range 8 {
			f.tick(srv, 5*time.Second, 1, 5, false)
		}
		if srv.Pressure().Closed {
			t.Fatalf("gate did not reopen: %+v", srv.Pressure())
		}
	}

	closeGate(t, srv, f)
	for range 10 {
		f.tick(srv, 5*time.Second, 40, 90, true)
	}
	got := rows()
	if len(got) != 1 {
		t.Fatalf("%d notifications for one closing, want 1: %+v", len(got), got)
	}
	n := got[0]
	if n.Kind != domain.NotifyHostPressure || n.SessionID != "" {
		t.Fatalf("notification %+v, want kind host_pressure with no session", n)
	}
	if !strings.Contains(n.Text, "is under memory pressure: full 35%; new sessions, reviews and CI runs wait") {
		t.Fatalf("notification text %q", n.Text)
	}

	// Closing again inside 10 minutes of the last notice stays quiet.
	reopen()
	closeGate(t, srv, f)
	if got := rows(); len(got) != 1 {
		t.Fatalf("%d notifications after a second closing within 10 minutes, want 1", len(got))
	}

	// After 10 minutes, the next closing notifies again.
	reopen()
	f.advance(10 * time.Minute)
	closeGate(t, srv, f)
	if got := rows(); len(got) != 2 {
		t.Fatalf("%d notifications after a closing past 10 minutes, want 2", len(got))
	}
}
