package selfupdate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type countFetcher struct {
	mu    sync.Mutex
	calls int
	rel   Release
}

func (f *countFetcher) Latest(context.Context, string) (Release, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.rel, true, nil
}

func (f *countFetcher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type nopRunner struct{}

func (nopRunner) Start(string, string) (int, error) { return 0, nil }

func TestPolicyNormalises(t *testing.T) {
	for _, c := range []struct {
		in   Policy
		mode Mode
		ev   time.Duration
		ra   time.Duration
	}{
		{Policy{}, ModeAuto, DefaultCheckEvery, DefaultRetryAfter},
		{Policy{Mode: "weekly", CheckEvery: time.Second}, ModeNotify, MinCheckEvery, DefaultRetryAfter},
		{Policy{Mode: ModeOff, CheckEvery: time.Hour, RetryAfter: time.Minute}, ModeOff, time.Hour, time.Minute},
	} {
		if got := c.in.mode(); got != c.mode {
			t.Errorf("%+v mode = %q, want %q", c.in, got, c.mode)
		}
		if got := c.in.checkEvery(); got != c.ev {
			t.Errorf("%+v checkEvery = %v, want %v", c.in, got, c.ev)
		}
		if got := c.in.retryAfter(); got != c.ra {
			t.Errorf("%+v retryAfter = %v, want %v", c.in, got, c.ra)
		}
	}
}

func TestOrDefault(t *testing.T) {
	if orDefault(0, time.Second) != time.Second || orDefault(time.Minute, time.Second) != time.Minute {
		t.Fatal("orDefault")
	}
}

func TestRunRetriesDeferralWithoutFetching(t *testing.T) {
	dir := t.TempDir()
	f := &countFetcher{rel: Release{Tag: "v0.35.0", Ready: true}}
	m := &Manager{
		Version: "v0.34.0", Fetcher: f, Runner: nopRunner{},
		StatePath: filepath.Join(dir, "s.json"), ResultPath: filepath.Join(dir, "r.json"),
		Policy:          Policy{Mode: ModeAuto, CheckEvery: time.Hour},
		Busy:            []func() int{func() int { return 1 }},
		FirstProbeDelay: time.Millisecond, DeferRetry: 10 * time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	m.Run(ctx)
	if n := f.count(); n != 1 {
		t.Fatalf("fetched %d times, want once: deferred retries reuse the stored release", n)
	}
	if st := m.Status(); st.Status != StatusDeferred {
		t.Fatalf("status = %q, want deferred", st.Status)
	}
}

func TestRunReturnsAtOnceWhenOffOrDev(t *testing.T) {
	for _, m := range []*Manager{
		{Version: "v0.34.0", Policy: Policy{Mode: ModeOff}},
		{Version: "dev", Policy: Policy{Mode: ModeAuto}},
	} {
		done := make(chan struct{})
		go func() { m.Run(context.Background()); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("Run(%q, %q) did not return", m.Version, m.Policy.Mode)
		}
	}
}

func TestSettleFailureCarriesLastLogLine(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "host-update.log")
	if err := os.WriteFile(logPath, []byte("downloading\naiman: checksum mismatch\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	m := &Manager{
		Version: "v0.34.0", StatePath: filepath.Join(dir, "s.json"), ResultPath: filepath.Join(dir, "r.json"),
		LogPath: logPath, Now: func() time.Time { return now },
	}
	s := State{Attempt: &Attempt{At: now.Add(-time.Minute), From: "v0.34.0", To: "v0.35.0", Status: AttemptStarted}}
	res := `{"tag":"v0.35.0","exit":2,"at":"` + now.Format(time.RFC3339) + `"}`
	if err := os.WriteFile(m.ResultPath, []byte(res), 0o600); err != nil {
		t.Fatal(err)
	}
	a := m.settleLocked(&s)
	if a == nil || a.Status != AttemptFailed || a.Exit != 2 {
		t.Fatalf("settled = %+v, want failed exit 2", a)
	}
	if want := "install failed (exit 2): aiman: checksum mismatch"; a.Error != want {
		t.Fatalf("error = %q, want %q", a.Error, want)
	}
}

func TestSettleAcceptsResultInTheAttemptsSecond(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 11, 8, 10, 0, 0, 700_000_000, time.UTC)
	m := &Manager{
		Version: "v0.35.0", StatePath: filepath.Join(dir, "s.json"), ResultPath: filepath.Join(dir, "r.json"),
		Now: func() time.Time { return at.Add(time.Second) },
	}
	s := State{Attempt: &Attempt{At: at, From: "v0.34.0", To: "v0.35.0", Status: AttemptStarted}}
	res := `{"tag":"v0.35.0","exit":0,"at":"2026-11-08T10:00:00Z"}`
	if err := os.WriteFile(m.ResultPath, []byte(res), 0o600); err != nil {
		t.Fatal(err)
	}
	if a := m.settleLocked(&s); a == nil || a.Status != AttemptOK {
		t.Fatalf("settled = %+v, want ok: the wrapper's time has whole seconds", a)
	}
}

func TestLastLineReadsOnlyTheTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	long := strings.Repeat("x", 10_000) + "\n" + strings.Repeat("y", 300) + "\n"
	if err := os.WriteFile(path, []byte(long), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := lastLine(path); got != strings.Repeat("y", 200) {
		t.Fatalf("lastLine = %q (len %d)", got[:10], len(got))
	}
	if lastLine("") != "" || lastLine(filepath.Join(t.TempDir(), "absent")) != "" {
		t.Fatal("lastLine of no file is not empty")
	}
}

func TestRequestRefusalReasons(t *testing.T) {
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	m := &Manager{Version: "v0.35.0", Policy: Policy{Mode: ModeAuto}, Now: func() time.Time { return now }}
	failed := State{Attempt: &Attempt{At: now, To: "v0.36.0", Status: AttemptFailed}}
	for _, c := range []struct {
		v    verdict
		s    State
		want string
	}{
		{verdict{status: StatusCurrent}, State{}, "v0.35.0 is the latest release"},
		{verdict{status: StatusNotReady, tag: "v0.36.0"}, State{}, "v0.36.0 is not ready yet"},
		{verdict{status: StatusUpdating, tag: "v0.36.0"}, State{}, "update to v0.36.0 is running"},
		{verdict{status: StatusFailed, tag: "v0.36.0"}, failed, "install of v0.36.0 failed"},
		{verdict{status: StatusUnknown}, State{}, "unknown"},
	} {
		if got := m.refusal(c.v, c.s); !strings.Contains(got, c.want) {
			t.Errorf("refusal(%s) = %q, want it to contain %q", c.v.status, got, c.want)
		}
	}
}

func TestScriptRunnerRefusesInvalidTag(t *testing.T) {
	r := NewScriptRunner(t.TempDir(), filepath.Join(t.TempDir(), "log"), filepath.Join(t.TempDir(), "r.json"))
	if _, err := r.Start("v1.0.0; id", "true\n"); err == nil {
		t.Fatal("Start accepted an invalid tag")
	}
}

func TestDefaultScriptIsTheServeUpdate(t *testing.T) {
	m := &Manager{}
	if s := m.script("v0.35.0"); !strings.Contains(s, "VERSION=v0.35.0") || !strings.Contains(s, "aiman-serve") {
		t.Fatalf("default script does not install serve at the tag:\n%s", s)
	}
}

func TestAliveSeesThisProcess(t *testing.T) {
	if !Alive(os.Getpid()) {
		t.Fatal("Alive(self) = false")
	}
	if Alive(0) || Alive(-1) {
		t.Fatal("Alive of a non-PID is true")
	}
}
