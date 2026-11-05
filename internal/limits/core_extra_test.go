package limits

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const codexPremiumLine = `{"timestamp":"2026-11-05T19:00:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{},"rate_limits":{"limit_id":"premium","plan_type":"plus","primary":null,"secondary":null}}}`

// Codex writes premium lines with null windows after real ones.
func TestCodexSkipsLinesWithoutWindows(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Home, ".codex/sessions/2026/11/05/rollout.jsonl"), codexLimitsLine+"\n"+codexPremiumLine+"\n")
	r := NewCodexSource(env).Read(context.Background())
	if r.State != StateOK || len(r.Windows) != 2 {
		t.Fatalf("reading %+v", r)
	}
}

// A session just started has no limits yet; the previous one still answers, with
// its own time.
func TestCodexFallsBackToAnOlderTranscript(t *testing.T) {
	env := testEnv(t)
	older := filepath.Join(env.Home, ".codex/sessions/2026/11/04/rollout.jsonl")
	writeFile(t, older, codexLimitsLine+"\n")
	writeFile(t, filepath.Join(env.Home, ".codex/sessions/2026/11/05/rollout.jsonl"), `{"type":"session_meta"}`+"\n")
	r := NewCodexSource(env).Read(context.Background())
	if r.State != StateOK || !r.ObservedAt.Equal(time.Date(2026, 11, 5, 18, 51, 0, 0, time.UTC)) {
		t.Fatalf("reading %+v", r)
	}
}

// A long session started on an earlier day can be the one written last.
func TestCodexPrefersTheLastWrittenTranscript(t *testing.T) {
	env := testEnv(t)
	yesterday := filepath.Join(env.Home, ".codex/sessions/2026/11/04/rollout.jsonl")
	today := filepath.Join(env.Home, ".codex/sessions/2026/11/05/rollout.jsonl")
	writeFile(t, yesterday, codexLimitsLine+"\n")
	writeFile(t, today, strings.ReplaceAll(codexLimitsLine, "30.0", "99.0")+"\n")
	if err := os.Chtimes(today, testNow.Add(-time.Hour), testNow.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(yesterday, testNow, testNow); err != nil {
		t.Fatal(err)
	}
	r := NewCodexSource(env).Read(context.Background())
	if len(r.Windows) != 2 || r.Windows[1].UsedPercent != 30 {
		t.Fatalf("reading %+v", r)
	}
}

// A percent the provider did not send must not read as zero.
func TestMuseMissingPercentIsNotZero(t *testing.T) {
	out := `{"allowance":{"asOfMs":1,"tier":"t","weekly":{"resetsAtMs":1794182400000},"window":null},"schemaVersion":1}`
	r := NewMuseSource(museEnv(t, out, nil)).Read(context.Background())
	if r.State != StateUnknown || len(r.Windows) != 0 {
		t.Fatalf("reading %+v", r)
	}
}

func TestMuseTimeoutAndNonZeroExitAreUnknown(t *testing.T) {
	if r := NewMuseSource(museEnv(t, "", context.DeadlineExceeded)).Read(context.Background()); r.State != StateUnknown {
		t.Fatalf("timeout: %+v", r)
	}
	env := testEnv(t)
	env.Run = func(context.Context, string, ...string) (string, int, error) { return "secret body", 3, nil }
	r := NewMuseSource(env).Read(context.Background())
	if r.State != StateUnknown || strings.Contains(r.Reason, "secret") {
		t.Fatalf("exit: %+v", r)
	}
}

func TestManagerUnavailableDropsOldNumbers(t *testing.T) {
	cur := Reading{Provider: "a", State: StateOK, Windows: []Window{{Label: "week", UsedPercent: 19}}}
	f := &fakeSource{name: "a", age: time.Hour, read: func() Reading { return cur }}
	m := NewManager([]Source{f}, func() time.Time { return testNow })
	m.Snapshot(false)
	settle(t, m)
	cur = Reading{Provider: "a", State: StateUnavailable}
	m.Snapshot(true)
	if r := settle(t, m).Readings[0]; r.State != StateUnavailable || len(r.Windows) != 0 {
		t.Fatalf("reading %+v", r)
	}
}

type ctxSource struct{ fakeSource }

func (s *ctxSource) Read(ctx context.Context) Reading {
	s.runs.Add(1)
	<-ctx.Done()
	return Reading{Provider: s.name, State: StateUnknown, Reason: "stopped"}
}

func TestManagerCloseStopsRunsAndRefusesNewOnes(t *testing.T) {
	s := &ctxSource{fakeSource{name: "a", age: time.Hour}}
	m := NewManager([]Source{s}, func() time.Time { return testNow })
	m.Snapshot(false)
	done := make(chan struct{})
	go func() { m.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not stop the running source")
	}
	if snap := m.Snapshot(true); snap.Refreshing {
		t.Fatalf("started a run after Close: %+v", snap)
	}
}

func TestDefaultSourcesListsEveryProvider(t *testing.T) {
	want := map[string]bool{"codex": true, "claude": true, "grok": true, "openrouter": true, "deepseek": true, "muse": true, "copilot": true, "opencode": true}
	for _, s := range DefaultSources(testEnv(t), false) {
		if !want[s.Name()] {
			t.Errorf("unexpected or repeated source %q", s.Name())
		}
		delete(want, s.Name())
	}
	if len(want) != 0 {
		t.Errorf("missing sources %v", want)
	}
}

func TestRunCommandContract(t *testing.T) {
	ctx := context.Background()
	if _, _, err := runCommand(ctx, "aiman-no-such-tool"); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("missing tool: %v", err)
	}
	out, exit, err := runCommand(ctx, "sh", "-c", "echo hi; exit 3")
	if err != nil || exit != 3 || out != "hi\n" {
		t.Fatalf("out %q exit %d err %v", out, exit, err)
	}
	short, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, _, err := runCommand(short, "sleep", "30"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout: %v", err)
	}
}

func TestResolveCommandPrefersTheUserPrefix(t *testing.T) {
	home := t.TempDir()
	if got := resolveCommand(home, "muse"); got != "muse" {
		t.Fatalf("got %q", got)
	}
	path := filepath.Join(home, ".npm-global/bin/muse")
	writeFile(t, path, "#!/bin/sh\n")
	if got := resolveCommand(home, "muse"); got != "muse" {
		t.Fatalf("a file that cannot run was chosen: %q", got)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := resolveCommand(home, "muse"); got != path {
		t.Fatalf("got %q", got)
	}
}
