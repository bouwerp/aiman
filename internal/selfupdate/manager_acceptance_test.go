package selfupdate_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/selfupdate"
)

var testNow = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

type fakeFetcher struct {
	mu      sync.Mutex
	rel     selfupdate.Release
	changed bool
	err     error
	calls   int
	etags   []string
}

func (f *fakeFetcher) Latest(_ context.Context, etag string) (selfupdate.Release, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.etags = append(f.etags, etag)
	if !f.changed {
		return selfupdate.Release{}, false, f.err
	}
	return f.rel, true, f.err
}

func (f *fakeFetcher) set(rel selfupdate.Release, changed bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rel, f.changed = rel, changed
}

func (f *fakeFetcher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeRunner struct {
	mu      sync.Mutex
	tags    []string
	scripts []string
	// onStart runs inside Start, before it returns, so a test can look at the
	// state file at the moment the script would start.
	onStart func(tag string)
	pid     int
	err     error
}

const fakePID = 4242

func (r *fakeRunner) Start(tag, script string) (int, error) {
	if r.onStart != nil {
		r.onStart(tag)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tags = append(r.tags, tag)
	r.scripts = append(r.scripts, script)
	if r.err != nil {
		return 0, r.err
	}
	return r.pid, nil
}

func (r *fakeRunner) started() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.tags...)
}

type harness struct {
	m       *selfupdate.Manager
	fetcher *fakeFetcher
	runner  *fakeRunner
	notes   map[string]int
	applied []selfupdate.Attempt
	mu      sync.Mutex
}

func (h *harness) notified() map[string]int {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := map[string]int{}
	for k, v := range h.notes {
		out[k] = v
	}
	return out
}

func (h *harness) appliedAttempts() []selfupdate.Attempt {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]selfupdate.Attempt(nil), h.applied...)
}

// setAlive replaces selfupdate.Alive for the rest of the test.
func setAlive(t *testing.T, f func(pid int) bool) {
	t.Helper()
	prev := selfupdate.Alive
	selfupdate.Alive = f
	t.Cleanup(func() { selfupdate.Alive = prev })
}

// writeResultFile writes a Result exactly as the wrapper does:
// {"tag":"<tag>","exit":<n>,"at":"<RFC3339 UTC>"}.
func writeResultFile(t *testing.T, path, tag string, exit int, at time.Time) {
	t.Helper()
	b := fmt.Sprintf(`{"tag":%q,"exit":%d,"at":%q}`+"\n", tag, exit, at.UTC().Format(time.RFC3339))
	if err := os.WriteFile(path, []byte(b), 0o600); err != nil {
		t.Fatal(err)
	}
}

func testScript(tag string) string { return "#!/bin/sh\n# install " + tag + "\n" }

func newHarness(t *testing.T, version string, mode selfupdate.Mode) *harness {
	t.Helper()
	dir := t.TempDir()
	h := &harness{fetcher: &fakeFetcher{}, runner: &fakeRunner{pid: fakePID}, notes: map[string]int{}}
	// No wrapper is running unless a test says so.
	setAlive(t, func(int) bool { return false })
	h.m = &selfupdate.Manager{
		Version:    version,
		Fetcher:    h.fetcher,
		Runner:     h.runner,
		Script:     testScript,
		StatePath:  filepath.Join(dir, "host-update.json"),
		ResultPath: filepath.Join(dir, "host-update.result.json"),
		Policy: selfupdate.Policy{
			Mode:       mode,
			CheckEvery: selfupdate.DefaultCheckEvery,
			RetryAfter: selfupdate.DefaultRetryAfter,
		},
		OnNewVersion: func(r selfupdate.Release) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.notes[r.Tag]++
		},
		OnApplied: func(a selfupdate.Attempt) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.applied = append(h.applied, a)
		},
		Logf: t.Logf,
		Now:  func() time.Time { return testNow },
	}
	return h
}

func (h *harness) seed(t *testing.T, s selfupdate.State) {
	t.Helper()
	if err := s.Save(h.m.StatePath); err != nil {
		t.Fatalf("seed state: %v", err)
	}
}

func (h *harness) state(t *testing.T) selfupdate.State {
	t.Helper()
	s, err := selfupdate.LoadState(h.m.StatePath)
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	return s
}

func ready(tag string) selfupdate.Release {
	return selfupdate.Release{Tag: tag, Ready: true, ETag: `"etag-` + tag + `"`}
}

func pollWithin(t *testing.T, m *selfupdate.Manager) selfupdate.Status {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan selfupdate.Status, 1)
	go func() { done <- m.Poll(ctx) }()
	select {
	case st := <-done:
		return st
	case <-time.After(10 * time.Second):
		t.Fatal("Poll blocked; it must never wait on the gate")
		return selfupdate.Status{}
	}
}

func TestP1PollDevVersionIsUnknownAndCallsNothing(t *testing.T) {
	for _, mode := range []selfupdate.Mode{selfupdate.ModeAuto, selfupdate.ModeNotify} {
		t.Run(string(mode), func(t *testing.T) {
			h := newHarness(t, "dev", mode)
			h.fetcher.set(ready("v0.35.0"), true)
			st := pollWithin(t, h.m)
			if st.Status != selfupdate.StatusUnknown {
				t.Fatalf("status = %q, want %q", st.Status, selfupdate.StatusUnknown)
			}
			if n := h.fetcher.count(); n != 0 {
				t.Fatalf("Fetcher called %d times for a dev build", n)
			}
			if got := h.runner.started(); len(got) != 0 {
				t.Fatalf("Runner started %v for a dev build", got)
			}
			if len(h.notified()) != 0 {
				t.Fatalf("OnNewVersion fired for a dev build: %v", h.notified())
			}
		})
	}
}

func TestP1PollModeOffIsHeldAndNeverFetches(t *testing.T) {
	h := newHarness(t, "v0.34.0", selfupdate.ModeOff)
	h.fetcher.set(ready("v0.35.0"), true)
	st := pollWithin(t, h.m)
	if st.Status != selfupdate.StatusHeld {
		t.Fatalf("status = %q, want %q", st.Status, selfupdate.StatusHeld)
	}
	if n := h.fetcher.count(); n != 0 {
		t.Fatalf("Fetcher called %d times in mode off", n)
	}
	if got := h.runner.started(); len(got) != 0 {
		t.Fatalf("Runner started %v in mode off", got)
	}
	if st := h.m.Status(); st.Status != selfupdate.StatusHeld {
		t.Fatalf("Status() = %q, want %q", st.Status, selfupdate.StatusHeld)
	}
}

func TestP1PollNewerUnreadyIsNotReady(t *testing.T) {
	for _, mode := range []selfupdate.Mode{selfupdate.ModeAuto, selfupdate.ModeNotify} {
		t.Run(string(mode), func(t *testing.T) {
			h := newHarness(t, "v0.34.0", mode)
			h.fetcher.set(selfupdate.Release{Tag: "v0.35.0", Ready: false}, true)
			st := pollWithin(t, h.m)
			if st.Status != selfupdate.StatusNotReady {
				t.Fatalf("status = %q, want %q", st.Status, selfupdate.StatusNotReady)
			}
			if st.Latest != "v0.35.0" || st.LatestReady {
				t.Fatalf("status latest = %q ready=%v, want v0.35.0 not ready", st.Latest, st.LatestReady)
			}
			if len(h.notified()) != 0 {
				t.Fatalf("OnNewVersion fired for an unready release: %v", h.notified())
			}
			if got := h.runner.started(); len(got) != 0 {
				t.Fatalf("Runner started %v for an unready release", got)
			}
		})
	}
}

func TestP1PollCurrentWhenLatestIsNotNewer(t *testing.T) {
	for _, latest := range []string{"v0.34.0", "v0.33.9"} {
		t.Run(latest, func(t *testing.T) {
			h := newHarness(t, "v0.34.0", selfupdate.ModeAuto)
			h.fetcher.set(ready(latest), true)
			st := pollWithin(t, h.m)
			if st.Status != selfupdate.StatusCurrent {
				t.Fatalf("status = %q, want %q", st.Status, selfupdate.StatusCurrent)
			}
			if got := h.runner.started(); len(got) != 0 {
				t.Fatalf("Runner started %v with nothing newer", got)
			}
		})
	}
}

func TestP1PollNotifyIsAvailableAndNotifiesOncePerTag(t *testing.T) {
	h := newHarness(t, "v0.34.0", selfupdate.ModeNotify)

	h.fetcher.set(ready("v0.35.0"), true)
	if st := pollWithin(t, h.m); st.Status != selfupdate.StatusAvailable {
		t.Fatalf("first poll status = %q, want %q", st.Status, selfupdate.StatusAvailable)
	}
	// Unchanged answer (304): the stored release still decides.
	h.fetcher.set(selfupdate.Release{}, false)
	if st := pollWithin(t, h.m); st.Status != selfupdate.StatusAvailable {
		t.Fatalf("304 poll status = %q, want %q", st.Status, selfupdate.StatusAvailable)
	}
	// The same tag answered again with a 200.
	h.fetcher.set(ready("v0.35.0"), true)
	pollWithin(t, h.m)

	if got := h.notified(); got["v0.35.0"] != 1 || len(got) != 1 {
		t.Fatalf("OnNewVersion = %v, want v0.35.0 exactly once", got)
	}
	if got := h.runner.started(); len(got) != 0 {
		t.Fatalf("Runner started %v in notify mode", got)
	}

	// A fresh manager on the same state file must not notify again for the
	// same tag (Notified is persisted).
	h2 := newHarness(t, "v0.34.0", selfupdate.ModeNotify)
	h2.m.StatePath = h.m.StatePath
	h2.m.ResultPath = h.m.ResultPath
	h2.fetcher.set(ready("v0.35.0"), true)
	pollWithin(t, h2.m)
	if got := h2.notified(); len(got) != 0 {
		t.Fatalf("restarted manager notified again: %v", got)
	}
	if s := h.state(t); s.Notified != "v0.35.0" {
		t.Fatalf("state Notified = %q, want v0.35.0", s.Notified)
	}

	// A newer tag notifies once more.
	h.fetcher.set(ready("v0.36.0"), true)
	pollWithin(t, h.m)
	pollWithin(t, h.m)
	if got := h.notified(); got["v0.36.0"] != 1 || got["v0.35.0"] != 1 {
		t.Fatalf("OnNewVersion = %v, want each tag once", got)
	}
}

func TestP1PollStoresAndSendsETag(t *testing.T) {
	h := newHarness(t, "v0.34.0", selfupdate.ModeNotify)
	h.seed(t, selfupdate.State{ETag: `"stored"`})

	rel := ready("v0.35.0")
	rel.ETag = `"fresh"`
	h.fetcher.set(rel, true)
	pollWithin(t, h.m)
	if len(h.fetcher.etags) != 1 || h.fetcher.etags[0] != `"stored"` {
		t.Fatalf("Fetcher etags = %q, want the stored etag", h.fetcher.etags)
	}
	s := h.state(t)
	if s.ETag != `"fresh"` {
		t.Fatalf("state ETag = %q, want the new answer's etag", s.ETag)
	}
	if s.Latest.Tag != "v0.35.0" || !s.Latest.Ready {
		t.Fatalf("state Latest = %+v, want v0.35.0 ready", s.Latest)
	}
	if !s.CheckedAt.Equal(testNow) {
		t.Fatalf("state CheckedAt = %v, want %v", s.CheckedAt, testNow)
	}

	// A 304 keeps etag and Latest and moves CheckedAt only.
	later := testNow.Add(10 * time.Minute)
	h.m.Now = func() time.Time { return later }
	h.fetcher.set(selfupdate.Release{}, false)
	pollWithin(t, h.m)
	if got := h.fetcher.etags[len(h.fetcher.etags)-1]; got != `"fresh"` {
		t.Fatalf("second poll sent etag %q, want %q", got, `"fresh"`)
	}
	s = h.state(t)
	if s.ETag != `"fresh"` || s.Latest.Tag != "v0.35.0" || !s.Latest.Ready {
		t.Fatalf("304 changed the stored answer: %+v", s)
	}
	if !s.CheckedAt.Equal(later) {
		t.Fatalf("304 CheckedAt = %v, want %v", s.CheckedAt, later)
	}
}

func TestP1PollFetchErrorStartsNothing(t *testing.T) {
	h := newHarness(t, "v0.34.0", selfupdate.ModeAuto)
	h.fetcher.err = errors.New("rate limited")
	pollWithin(t, h.m)
	if got := h.runner.started(); len(got) != 0 {
		t.Fatalf("Runner started %v after a failed fetch", got)
	}
}

func TestP1PollAutoSavesStartedAttemptBeforeRunning(t *testing.T) {
	h := newHarness(t, "v0.34.0", selfupdate.ModeAuto)
	h.m.Busy = []func() int{func() int { return 0 }, func() int { return 0 }}
	h.fetcher.set(ready("v0.35.0"), true)

	// A stale Result from an earlier install must be gone before the script runs.
	writeResultFile(t, h.m.ResultPath, "v0.35.0", 0, testNow.Add(-time.Hour))

	var atStart selfupdate.State
	var resultAtStart bool
	h.runner.onStart = func(string) {
		s, err := selfupdate.LoadState(h.m.StatePath)
		if err != nil {
			t.Errorf("load state at start: %v", err)
		}
		atStart = s
		_, err = os.Stat(h.m.ResultPath)
		resultAtStart = err == nil
	}

	st := pollWithin(t, h.m)

	if got := h.runner.started(); len(got) != 1 || got[0] != "v0.35.0" {
		t.Fatalf("Runner started %v, want [v0.35.0]", got)
	}
	if h.runner.scripts[0] != testScript("v0.35.0") {
		t.Fatalf("Runner script = %q, want Manager.Script(tag)", h.runner.scripts[0])
	}
	a := atStart.Attempt
	if a == nil || a.Status != selfupdate.AttemptStarted || a.From != "v0.34.0" || a.To != "v0.35.0" {
		t.Fatalf("attempt saved before start = %+v, want started v0.34.0 -> v0.35.0", a)
	}
	if !a.At.Equal(testNow) {
		t.Fatalf("attempt At = %v, want %v", a.At, testNow)
	}
	if resultAtStart {
		t.Fatal("stale Result file still present when the script started")
	}
	if st.Status != selfupdate.StatusUpdating {
		t.Fatalf("status after apply = %q, want %q", st.Status, selfupdate.StatusUpdating)
	}
	if st.Attempt == nil || st.Attempt.To != "v0.35.0" {
		t.Fatalf("status attempt = %+v, want the started attempt", st.Attempt)
	}
	saved := h.state(t).Attempt
	if saved == nil || saved.Status != selfupdate.AttemptStarted || saved.PID != fakePID {
		t.Fatalf("saved attempt = %+v, want started with the runner's PID %d", saved, fakePID)
	}
}

func TestP1PollAutoDecisions(t *testing.T) {
	type tc struct {
		name       string
		version    string
		latest     selfupdate.Release
		attempt    *selfupdate.Attempt
		busy       []int
		alive      bool // what selfupdate.Alive reports for the attempt's PID
		wantStatus string
		anyStatus  []string // when set, any of these is accepted instead of wantStatus
		wantStart  string   // tag the runner must start; empty for none
		wantSaved  selfupdate.AttemptStatus
	}
	cases := []tc{
		{
			name: "ready newer, nothing busy", version: "v0.34.0", latest: ready("v0.35.0"),
			busy: []int{0, 0, 0}, wantStatus: selfupdate.StatusUpdating, wantStart: "v0.35.0",
		},
		{
			name: "ready newer, no busy counters", version: "v0.34.0", latest: ready("v0.35.0"),
			wantStatus: selfupdate.StatusUpdating, wantStart: "v0.35.0",
		},
		{
			name: "one counter busy defers", version: "v0.34.0", latest: ready("v0.35.0"),
			busy: []int{0, 1, 0}, wantStatus: selfupdate.StatusDeferred,
		},
		{
			name: "several busy defers", version: "v0.34.0", latest: ready("v0.35.0"),
			busy: []int{2, 3}, wantStatus: selfupdate.StatusDeferred,
		},
		{
			name: "started attempt with a live PID is updating", version: "v0.34.0", latest: ready("v0.35.0"),
			attempt:    &selfupdate.Attempt{At: testNow.Add(-time.Minute), From: "v0.34.0", To: "v0.35.0", PID: 77, Status: selfupdate.AttemptStarted},
			alive:      true,
			wantStatus: selfupdate.StatusUpdating,
		},
		{
			name: "started attempt with a live PID past ResultWait stays updating", version: "v0.34.0", latest: ready("v0.35.0"),
			attempt:    &selfupdate.Attempt{At: testNow.Add(-selfupdate.ResultWait - time.Minute), From: "v0.34.0", To: "v0.35.0", PID: 77, Status: selfupdate.AttemptStarted},
			alive:      true,
			wantStatus: selfupdate.StatusUpdating,
		},
		{
			name: "started attempt younger than ResultWait defers even with the PID gone", version: "v0.34.0", latest: ready("v0.35.0"),
			attempt:   &selfupdate.Attempt{At: testNow.Add(-time.Minute), From: "v0.34.0", To: "v0.35.0", PID: 77, Status: selfupdate.AttemptStarted},
			anyStatus: []string{selfupdate.StatusUpdating, selfupdate.StatusDeferred},
		},
		{
			name: "poll settles a started attempt past ResultWait with the PID gone", version: "v0.34.0", latest: ready("v0.35.0"),
			attempt:    &selfupdate.Attempt{At: testNow.Add(-selfupdate.ResultWait - time.Minute), From: "v0.34.0", To: "v0.35.0", PID: 77, Status: selfupdate.AttemptStarted},
			wantStatus: selfupdate.StatusFailed, wantSaved: selfupdate.AttemptFailed,
		},
		{
			name: "failed same tag younger than RetryAfter skips", version: "v0.34.0", latest: ready("v0.35.0"),
			attempt:    &selfupdate.Attempt{At: testNow.Add(-10 * time.Minute), From: "v0.34.0", To: "v0.35.0", Status: selfupdate.AttemptFailed, Exit: 1},
			wantStatus: selfupdate.StatusFailed,
		},
		{
			name: "failed same tag younger than RetryAfter with version already there skips", version: "v0.35.0", latest: ready("v0.35.0"),
			attempt:    &selfupdate.Attempt{At: testNow.Add(-10 * time.Minute), From: "v0.34.0", To: "v0.35.0", Status: selfupdate.AttemptFailed, Exit: 1},
			wantStatus: selfupdate.StatusFailed,
		},
		{
			name: "failed same tag older than RetryAfter re-applies", version: "v0.34.0", latest: ready("v0.35.0"),
			attempt:    &selfupdate.Attempt{At: testNow.Add(-2 * time.Hour), From: "v0.34.0", To: "v0.35.0", Status: selfupdate.AttemptFailed, Exit: 1},
			wantStatus: selfupdate.StatusUpdating, wantStart: "v0.35.0",
		},
		{
			name: "failed lockstep older than RetryAfter re-applies at own version", version: "v0.35.0", latest: ready("v0.35.0"),
			attempt:    &selfupdate.Attempt{At: testNow.Add(-2 * time.Hour), From: "v0.34.0", To: "v0.35.0", Status: selfupdate.AttemptFailed, Exit: 7},
			wantStatus: selfupdate.StatusUpdating, wantStart: "v0.35.0",
		},
		{
			name: "failed older tag is ignored when a newer tag appears", version: "v0.34.0", latest: ready("v0.36.0"),
			attempt:    &selfupdate.Attempt{At: testNow.Add(-10 * time.Minute), From: "v0.34.0", To: "v0.35.0", Status: selfupdate.AttemptFailed, Exit: 1},
			wantStatus: selfupdate.StatusUpdating, wantStart: "v0.36.0",
		},
		{
			name: "ok attempt at current version is current", version: "v0.35.0", latest: ready("v0.35.0"),
			attempt:    &selfupdate.Attempt{At: testNow.Add(-10 * time.Minute), From: "v0.34.0", To: "v0.35.0", Status: selfupdate.AttemptOK},
			wantStatus: selfupdate.StatusCurrent,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, c.version, selfupdate.ModeAuto)
			alive := c.alive
			setAlive(t, func(int) bool { return alive })
			for _, n := range c.busy {
				h.m.Busy = append(h.m.Busy, func() int { return n })
			}
			if c.attempt != nil {
				h.seed(t, selfupdate.State{Attempt: c.attempt})
			}
			h.fetcher.set(c.latest, true)

			st := pollWithin(t, h.m)
			if len(c.anyStatus) > 0 {
				if !slices.Contains(c.anyStatus, st.Status) {
					t.Fatalf("status = %q, want one of %q", st.Status, c.anyStatus)
				}
			} else if st.Status != c.wantStatus {
				t.Fatalf("status = %q, want %q", st.Status, c.wantStatus)
			}
			got := h.runner.started()
			if c.wantStart == "" {
				if len(got) != 0 {
					t.Fatalf("Runner started %v, want nothing", got)
				}
				s := h.state(t)
				if c.attempt == nil && s.Attempt != nil {
					t.Fatalf("attempt saved although nothing started: %+v", s.Attempt)
				}
				wantSaved := c.wantSaved
				if c.attempt != nil && wantSaved == "" {
					wantSaved = c.attempt.Status
				}
				if c.attempt != nil && (s.Attempt == nil || s.Attempt.Status != wantSaved || s.Attempt.To != c.attempt.To) {
					t.Fatalf("stored attempt = %+v, want status %q to %s", s.Attempt, wantSaved, c.attempt.To)
				}
				return
			}
			if len(got) != 1 || got[0] != c.wantStart {
				t.Fatalf("Runner started %v, want [%s]", got, c.wantStart)
			}
			s := h.state(t)
			if s.Attempt == nil || s.Attempt.Status != selfupdate.AttemptStarted || s.Attempt.To != c.wantStart || s.Attempt.From != c.version {
				t.Fatalf("stored attempt = %+v, want started %s -> %s", s.Attempt, c.version, c.wantStart)
			}
			if s.Attempt.PID != fakePID {
				t.Fatalf("stored attempt PID = %d, want the runner's %d", s.Attempt.PID, fakePID)
			}
		})
	}
}

func TestP1PollMaxDeferHonoursOnlyFirstCounter(t *testing.T) {
	t.Run("stuck later counter is overridden", func(t *testing.T) {
		h := newHarness(t, "v0.34.0", selfupdate.ModeAuto)
		h.m.Busy = []func() int{func() int { return 0 }, func() int { return 1 }}
		h.fetcher.set(ready("v0.35.0"), true)
		now := testNow
		h.m.Now = func() time.Time { return now }

		if st := pollWithin(t, h.m); st.Status != selfupdate.StatusDeferred {
			t.Fatalf("first poll = %q, want deferred", st.Status)
		}
		now = testNow.Add(selfupdate.MaxDefer - time.Minute)
		if st := pollWithin(t, h.m); st.Status != selfupdate.StatusDeferred {
			t.Fatalf("poll before MaxDefer = %q, want deferred", st.Status)
		}
		if got := h.runner.started(); len(got) != 0 {
			t.Fatalf("Runner started %v before MaxDefer", got)
		}
		now = testNow.Add(selfupdate.MaxDefer + time.Minute)
		pollWithin(t, h.m)
		if got := h.runner.started(); len(got) != 1 || got[0] != "v0.35.0" {
			t.Fatalf("Runner started %v after MaxDefer, want [v0.35.0]", got)
		}
	})
	t.Run("first counter is always honoured", func(t *testing.T) {
		h := newHarness(t, "v0.34.0", selfupdate.ModeAuto)
		h.m.Busy = []func() int{func() int { return 1 }, func() int { return 0 }}
		h.fetcher.set(ready("v0.35.0"), true)
		now := testNow
		h.m.Now = func() time.Time { return now }

		pollWithin(t, h.m)
		now = testNow.Add(selfupdate.MaxDefer + time.Hour)
		if st := pollWithin(t, h.m); st.Status != selfupdate.StatusDeferred {
			t.Fatalf("poll after MaxDefer with Busy[0] = 1 is %q, want deferred", st.Status)
		}
		if got := h.runner.started(); len(got) != 0 {
			t.Fatalf("Runner started %v while Busy[0] was non-zero", got)
		}
	})
}

func TestP1PollSettlesStartedAttempt(t *testing.T) {
	h := newHarness(t, "v0.35.0", selfupdate.ModeAuto)
	h.seed(t, selfupdate.State{Attempt: &selfupdate.Attempt{
		At: testNow.Add(-20 * time.Second), From: "v0.34.0", To: "v0.35.0", PID: 77, Status: selfupdate.AttemptStarted,
	}})
	writeResultFile(t, h.m.ResultPath, "v0.35.0", 0, testNow.Add(-time.Second))
	h.fetcher.set(ready("v0.35.0"), true)

	st := pollWithin(t, h.m)
	if st.Status != selfupdate.StatusCurrent {
		t.Fatalf("status = %q, want current", st.Status)
	}
	if s := h.state(t); s.Attempt == nil || s.Attempt.Status != selfupdate.AttemptOK {
		t.Fatalf("attempt = %+v, want ok", s.Attempt)
	}
	pollWithin(t, h.m)
	if a := h.appliedAttempts(); len(a) != 1 || a[0].Status != selfupdate.AttemptOK {
		t.Fatalf("OnApplied = %+v, want one ok", a)
	}
	if got := h.runner.started(); len(got) != 0 {
		t.Fatalf("Runner started %v", got)
	}
}

func TestP1RequestAppliesReadyNewerInNotifyAndAuto(t *testing.T) {
	for _, mode := range []selfupdate.Mode{selfupdate.ModeAuto, selfupdate.ModeNotify} {
		t.Run(string(mode), func(t *testing.T) {
			h := newHarness(t, "v0.34.0", mode)
			h.fetcher.set(ready("v0.35.0"), true)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			out := h.m.Request(ctx)
			if !out.Accepted || out.Target != "v0.35.0" {
				t.Fatalf("Request = %+v, want accepted v0.35.0", out)
			}
			if h.fetcher.count() == 0 {
				t.Fatal("Request did not poll")
			}
			if got := h.runner.started(); len(got) != 1 || got[0] != "v0.35.0" {
				t.Fatalf("Runner started %v, want [v0.35.0]", got)
			}
			s := h.state(t)
			if s.Attempt == nil || s.Attempt.Status != selfupdate.AttemptStarted || s.Attempt.To != "v0.35.0" {
				t.Fatalf("stored attempt = %+v, want started -> v0.35.0", s.Attempt)
			}
		})
	}
}

func TestP1RequestRefusals(t *testing.T) {
	cases := []struct {
		name    string
		version string
		mode    selfupdate.Mode
		latest  selfupdate.Release
		busy    int
	}{
		{"mode off", "v0.34.0", selfupdate.ModeOff, ready("v0.35.0"), 0},
		{"dev build", "dev", selfupdate.ModeAuto, ready("v0.35.0"), 0},
		{"nothing newer", "v0.35.0", selfupdate.ModeAuto, ready("v0.35.0"), 0},
		{"not ready", "v0.34.0", selfupdate.ModeAuto, selfupdate.Release{Tag: "v0.35.0"}, 0},
		{"busy", "v0.34.0", selfupdate.ModeAuto, ready("v0.35.0"), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, c.version, c.mode)
			busy := c.busy
			h.m.Busy = []func() int{func() int { return busy }}
			h.fetcher.set(c.latest, true)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			done := make(chan selfupdate.Outcome, 1)
			go func() { done <- h.m.Request(ctx) }()
			var out selfupdate.Outcome
			select {
			case out = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("Request did not return after its context ended")
			}
			if out.Accepted {
				t.Fatalf("Request = %+v, want refused", out)
			}
			if out.Reason == "" {
				t.Fatal("refused Request has no reason")
			}
			if got := h.runner.started(); len(got) != 0 {
				t.Fatalf("Runner started %v, want nothing", got)
			}
			if c.mode == selfupdate.ModeOff && h.fetcher.count() != 0 {
				t.Fatal("Request fetched in mode off")
			}
		})
	}
}

func finishWithin(t *testing.T, m *selfupdate.Manager, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	done := make(chan struct{})
	go func() { m.Finish(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(d + 5*time.Second):
		t.Fatal("Finish did not return")
	}
}

func TestP1FinishSettlesStartedAttempt(t *testing.T) {
	type result struct {
		tag  string
		exit int
		at   time.Time
	}
	cases := []struct {
		name       string
		version    string
		result     *result
		startedAgo time.Duration
		alive      bool
		wantStatus selfupdate.AttemptStatus
		wantExit   int
	}{
		{
			name: "exit 0 at target is ok", version: "v0.35.0",
			result:     &result{"v0.35.0", 0, testNow.Add(-5 * time.Second)},
			startedAgo: 30 * time.Second, wantStatus: selfupdate.AttemptOK,
		},
		{
			name: "non-zero exit at target is a failed lockstep", version: "v0.35.0",
			result:     &result{"v0.35.0", 3, testNow.Add(-5 * time.Second)},
			startedAgo: 30 * time.Second, wantStatus: selfupdate.AttemptFailed, wantExit: 3,
		},
		{
			name: "non-zero exit on the old version is a failed install", version: "v0.34.0",
			result:     &result{"v0.35.0", 1, testNow.Add(-5 * time.Second)},
			startedAgo: 30 * time.Second, wantStatus: selfupdate.AttemptFailed, wantExit: 1,
		},
		{
			name: "exit 0 on the old version past ResultWait fails", version: "v0.34.0",
			result:     &result{"v0.35.0", 0, testNow.Add(-selfupdate.ResultWait)},
			startedAgo: selfupdate.ResultWait + time.Minute, wantStatus: selfupdate.AttemptFailed,
		},
		{
			name: "no result past ResultWait with the PID gone fails", version: "v0.34.0",
			startedAgo: selfupdate.ResultWait + time.Minute, wantStatus: selfupdate.AttemptFailed,
		},
		{
			name: "result for another tag is ignored", version: "v0.34.0",
			result:     &result{"v0.34.0", 0, testNow.Add(-5 * time.Second)},
			startedAgo: selfupdate.ResultWait + time.Minute, wantStatus: selfupdate.AttemptFailed,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, c.version, selfupdate.ModeAuto)
			alive := c.alive
			setAlive(t, func(int) bool { return alive })
			h.seed(t, selfupdate.State{Attempt: &selfupdate.Attempt{
				At: testNow.Add(-c.startedAgo), From: "v0.34.0", To: "v0.35.0", PID: 77, Status: selfupdate.AttemptStarted,
			}})
			if c.result != nil {
				writeResultFile(t, h.m.ResultPath, c.result.tag, c.result.exit, c.result.at)
			}
			finishWithin(t, h.m, 15*time.Second)

			s := h.state(t)
			if s.Attempt == nil {
				t.Fatal("attempt gone after Finish")
			}
			if s.Attempt.Status != c.wantStatus {
				t.Fatalf("attempt status = %q, want %q", s.Attempt.Status, c.wantStatus)
			}
			if s.Attempt.To != "v0.35.0" || s.Attempt.From != "v0.34.0" {
				t.Fatalf("attempt from/to = %s -> %s, want v0.34.0 -> v0.35.0", s.Attempt.From, s.Attempt.To)
			}
			if c.wantStatus == selfupdate.AttemptFailed {
				if s.Attempt.Error == "" {
					t.Fatal("failed attempt has no error")
				}
				if c.wantExit != 0 && s.Attempt.Exit != c.wantExit {
					t.Fatalf("attempt exit = %d, want %d", s.Attempt.Exit, c.wantExit)
				}
			}
			applied := h.appliedAttempts()
			if len(applied) != 1 {
				t.Fatalf("OnApplied fired %d times, want once", len(applied))
			}
			if applied[0].Status != c.wantStatus || applied[0].To != "v0.35.0" {
				t.Fatalf("OnApplied got %+v, want status %q to v0.35.0", applied[0], c.wantStatus)
			}

			// A second Finish on a settled attempt does nothing.
			finishWithin(t, h.m, 5*time.Second)
			if n := len(h.appliedAttempts()); n != 1 {
				t.Fatalf("OnApplied fired %d times after a second Finish, want once", n)
			}
		})
	}
}

// TestP1FinishIgnoresNotYet covers results that must read as "not yet": the
// attempt stays started while ResultWait has not passed, or while the PID lives.
func TestP1FinishIgnoresNotYet(t *testing.T) {
	cases := []struct {
		name       string
		version    string
		write      func(t *testing.T, path string)
		startedAgo time.Duration
		alive      bool
	}{
		{
			name: "result for another tag", version: "v0.35.0", startedAgo: 30 * time.Second,
			write: func(t *testing.T, p string) { writeResultFile(t, p, "v0.34.0", 0, testNow) },
		},
		{
			name: "result older than the attempt", version: "v0.35.0", startedAgo: 30 * time.Second,
			write: func(t *testing.T, p string) { writeResultFile(t, p, "v0.35.0", 0, testNow.Add(-time.Hour)) },
		},
		{
			name: "unparsable result", version: "v0.35.0", startedAgo: 30 * time.Second,
			write: func(t *testing.T, p string) {
				if err := os.WriteFile(p, []byte(`{"tag":"v0.35.0","exit":`), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "exit 0 but still the old version", version: "v0.34.0", startedAgo: 30 * time.Second,
			write: func(t *testing.T, p string) { writeResultFile(t, p, "v0.35.0", 0, testNow) },
		},
		{
			name: "no result yet", version: "v0.34.0", startedAgo: 30 * time.Second,
			write: func(*testing.T, string) {},
		},
		{
			name: "no result past ResultWait but the PID lives", version: "v0.34.0", startedAgo: selfupdate.ResultWait + time.Minute,
			alive: true, write: func(*testing.T, string) {},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, c.version, selfupdate.ModeAuto)
			alive := c.alive
			setAlive(t, func(int) bool { return alive })
			h.seed(t, selfupdate.State{Attempt: &selfupdate.Attempt{
				At: testNow.Add(-c.startedAgo), From: "v0.34.0", To: "v0.35.0", PID: 77, Status: selfupdate.AttemptStarted,
			}})
			c.write(t, h.m.ResultPath)
			// The clock is frozen, so Finish waits until its context ends.
			finishWithin(t, h.m, 3*time.Second)
			if s := h.state(t); s.Attempt == nil || s.Attempt.Status != selfupdate.AttemptStarted {
				t.Fatalf("attempt = %+v, want still started", s.Attempt)
			}
			if n := len(h.appliedAttempts()); n != 0 {
				t.Fatalf("OnApplied fired %d times", n)
			}
		})
	}
}

func TestP1FinishWaitsForResult(t *testing.T) {
	h := newHarness(t, "v0.35.0", selfupdate.ModeAuto)
	h.seed(t, selfupdate.State{Attempt: &selfupdate.Attempt{
		At: testNow.Add(-10 * time.Second), From: "v0.34.0", To: "v0.35.0", PID: 77, Status: selfupdate.AttemptStarted,
	}})
	late := fmt.Sprintf(`{"tag":"v0.35.0","exit":0,"at":%q}`, testNow.Format(time.RFC3339))
	wrote := make(chan error, 1)
	go func() {
		time.Sleep(500 * time.Millisecond)
		wrote <- os.WriteFile(h.m.ResultPath, []byte(late), 0o600)
	}()
	finishWithin(t, h.m, 15*time.Second)
	if err := <-wrote; err != nil {
		t.Fatal(err)
	}
	s := h.state(t)
	if s.Attempt == nil || s.Attempt.Status != selfupdate.AttemptOK {
		t.Fatalf("attempt = %+v, want ok once the result arrived", s.Attempt)
	}
	if n := len(h.appliedAttempts()); n != 1 {
		t.Fatalf("OnApplied fired %d times, want once", n)
	}
}

func TestP1FinishWithoutStartedAttemptTouchesNothing(t *testing.T) {
	t.Run("no state file", func(t *testing.T) {
		h := newHarness(t, "v0.35.0", selfupdate.ModeAuto)
		finishWithin(t, h.m, 5*time.Second)
		if _, err := os.Stat(h.m.StatePath); !os.IsNotExist(err) {
			t.Fatalf("Finish created the state file (err %v)", err)
		}
		if n := len(h.appliedAttempts()); n != 0 {
			t.Fatalf("OnApplied fired %d times", n)
		}
	})
	for _, status := range []selfupdate.AttemptStatus{selfupdate.AttemptOK, selfupdate.AttemptFailed} {
		t.Run(string(status), func(t *testing.T) {
			h := newHarness(t, "v0.35.0", selfupdate.ModeAuto)
			h.seed(t, selfupdate.State{
				CheckedAt: testNow.Add(-time.Hour),
				Attempt:   &selfupdate.Attempt{At: testNow.Add(-time.Hour), From: "v0.34.0", To: "v0.35.0", Status: status},
			})
			writeResultFile(t, h.m.ResultPath, "v0.35.0", 9, testNow)
			before, err := os.ReadFile(h.m.StatePath)
			if err != nil {
				t.Fatal(err)
			}
			finishWithin(t, h.m, 5*time.Second)
			after, err := os.ReadFile(h.m.StatePath)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatalf("Finish rewrote the state:\nbefore %s\nafter  %s", before, after)
			}
			if n := len(h.appliedAttempts()); n != 0 {
				t.Fatalf("OnApplied fired %d times", n)
			}
		})
	}
}

func TestP1StateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host-update.json")

	s, err := selfupdate.LoadState(path)
	if err != nil {
		t.Fatalf("LoadState on a missing file: %v", err)
	}
	if s.Attempt != nil || s.ETag != "" || s.Latest.Tag != "" || !s.CheckedAt.IsZero() || s.Notified != "" {
		t.Fatalf("missing file gave %+v, want a zero State", s)
	}

	want := selfupdate.State{
		CheckedAt: testNow,
		ETag:      `W/"abc"`,
		Latest:    selfupdate.Release{Tag: "v0.35.0", Ready: true, ETag: `W/"abc"`, PublishedAt: testNow.Add(-time.Hour)},
		Notified:  "v0.35.0",
		Attempt: &selfupdate.Attempt{
			At: testNow.Add(-time.Minute), From: "v0.34.0", To: "v0.35.0", PID: 1234,
			Status: selfupdate.AttemptFailed, Exit: 4, Error: "gateway lockstep failed (exit 4): boom",
		},
	}
	if err := want.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := selfupdate.LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if !got.CheckedAt.Equal(want.CheckedAt) || got.ETag != want.ETag || got.Notified != want.Notified {
		t.Fatalf("round trip header = %+v, want %+v", got, want)
	}
	if got.Latest.Tag != want.Latest.Tag || got.Latest.Ready != want.Latest.Ready || got.Latest.ETag != want.Latest.ETag ||
		!got.Latest.PublishedAt.Equal(want.Latest.PublishedAt) {
		t.Fatalf("round trip Latest = %+v, want %+v", got.Latest, want.Latest)
	}
	a, w := got.Attempt, want.Attempt
	if a == nil || !a.At.Equal(w.At) || a.From != w.From || a.To != w.To || a.PID != w.PID || a.Status != w.Status || a.Exit != w.Exit || a.Error != w.Error {
		t.Fatalf("round trip Attempt = %+v, want %+v", a, w)
	}

	// Save is atomic: only the state file is left in the directory.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("directory holds %v, want only %s", names, filepath.Base(path))
	}

	// Saving over an existing file replaces it.
	want.Attempt = nil
	want.Notified = ""
	if err := want.Save(path); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	got, err = selfupdate.LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Attempt != nil || got.Notified != "" {
		t.Fatalf("second Save did not replace the file: %+v", got)
	}
}

func TestP1StateReadResult(t *testing.T) {
	dir := t.TempDir()

	if _, ok, err := selfupdate.ReadResult(filepath.Join(dir, "absent.json")); ok || err != nil {
		t.Fatalf("absent result: ok=%v err=%v, want false nil", ok, err)
	}

	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := selfupdate.ReadResult(bad); ok {
		t.Fatal("unparsable result reported ok")
	}

	good := filepath.Join(dir, "good.json")
	writeResultFile(t, good, "v0.35.0", 2, testNow)
	r, ok, err := selfupdate.ReadResult(good)
	if !ok || err != nil {
		t.Fatalf("good result: ok=%v err=%v", ok, err)
	}
	if r.Tag != "v0.35.0" || r.Exit != 2 || !r.At.Equal(testNow) {
		t.Fatalf("ReadResult = %+v", r)
	}
}

func TestP1PolicyDefaults(t *testing.T) {
	if selfupdate.DefaultCheckEvery != 10*time.Minute || selfupdate.MinCheckEvery != time.Minute ||
		selfupdate.DefaultRetryAfter != time.Hour || selfupdate.DefaultFirstProbeDelay != 2*time.Minute ||
		selfupdate.DefaultDeferRetry != 30*time.Second || selfupdate.ResultWait != 3*time.Minute ||
		selfupdate.MaxDefer != 30*time.Minute {
		t.Fatal("policy constants differ from the contract")
	}
	if selfupdate.ModeOff != "off" || selfupdate.ModeNotify != "notify" || selfupdate.ModeAuto != "auto" {
		t.Fatal("mode values differ from the contract")
	}
}

// TestP1StateFileJSONNames pins the on-disk names the serve probe passes to the
// laptop verbatim (SELFUPDATE=<host-update.json>): attempt.status, to, at, pid.
func TestP1StateFileJSONNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host-update.json")
	st := selfupdate.State{Attempt: &selfupdate.Attempt{
		At: testNow, From: "v0.34.0", To: "v0.35.0", PID: 99, Status: selfupdate.AttemptStarted,
	}}
	if err := st.Save(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Attempt map[string]any `json:"attempt"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("state file is not JSON: %v\n%s", err, raw)
	}
	a := doc.Attempt
	if a == nil {
		t.Fatalf("state file has no \"attempt\" object:\n%s", raw)
	}
	if a["status"] != "started" || a["to"] != "v0.35.0" || a["from"] != "v0.34.0" {
		t.Fatalf("attempt JSON = %v, want status/from/to keys", a)
	}
	if at, _ := a["at"].(string); at == "" {
		t.Fatalf("attempt JSON has no \"at\" string: %v", a)
	}
	if pid, _ := a["pid"].(float64); pid != 99 {
		t.Fatalf("attempt JSON pid = %v, want 99", a["pid"])
	}
}
