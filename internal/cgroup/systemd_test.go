package cgroup

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestScopeName(t *testing.T) {
	for in, want := range map[string]string{
		"ac155c15-6ea7-4188-9c9d-faa18becd95c": "aiman-session-ac155c15-6ea7-4188-9c9d-faa18becd95c.scope",
		"a b/c;$(x)":                           "aiman-session-a_b_c___x_.scope",
	} {
		if got := ScopeName(in); got != want {
			t.Errorf("ScopeName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := ScopeName(strings.Repeat("x", 400)); len(got) > 255 {
		t.Errorf("ScopeName of a long id is %d bytes", len(got))
	}
}

func TestRunArgv(t *testing.T) {
	got := RunArgv("s1", Limits{MemoryHigh: 6 << 30, MemoryMax: 10 << 30, MemorySwapMax: 1 << 30, CPUWeight: 100, IOWeight: 100, TasksMax: 4096})
	want := []string{
		"systemd-run", "--user", "--scope", "--quiet", "--collect",
		"--unit=aiman-session-s1.scope", "--slice=aiman-sessions.slice",
		"--description=aiman session s1",
		"-p", "MemoryHigh=6442450944", "-p", "MemoryMax=10737418240", "-p", "MemorySwapMax=1073741824",
		"-p", "CPUWeight=100", "-p", "IOWeight=100", "-p", "TasksMax=4096",
		"--",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RunArgv =\n%v\nwant\n%v", got, want)
	}
}

func TestProbeRefusals(t *testing.T) {
	found := func(string) (string, error) { return "/usr/bin/x", nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }
	ok := func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	noBus := func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Failed to connect to bus: No medium found\n"), errors.New("exit 1")
	}
	ctx := context.Background()
	if _, err := probe(ctx, "darwin", found, ok); !errors.Is(err, ErrUnavailable) {
		t.Errorf("darwin: %v", err)
	}
	if _, err := probe(ctx, "linux", missing, ok); !errors.Is(err, ErrUnavailable) {
		t.Errorf("no systemd-run: %v", err)
	}
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if _, err := probe(ctx, "linux", found, noBus); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "No medium found") {
		t.Errorf("no bus: %v", err)
	}
	s, err := probe(ctx, "linux", found, ok)
	if err != nil || s == nil || !strings.HasPrefix(s.Root, fsRoot) {
		t.Errorf("available: %+v, %v", s, err)
	}
}

func TestManagerRoot(t *testing.T) {
	cases := map[string]string{
		"0::/user.slice/user-1004.slice/user@1004.service/app.slice/aiman-serve.service\n": "/sys/fs/cgroup/user.slice/user-1004.slice/user@1004.service",
		"0::/user.slice/user-1004.slice/session-3.scope\n":                                 "/sys/fs/cgroup/user.slice/user-1004.slice/user@1004.service",
		"": "/sys/fs/cgroup/user.slice/user-1004.slice/user@1004.service",
	}
	for in, want := range cases {
		if got := managerRoot(in, 1004); got != want {
			t.Errorf("managerRoot(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSetLimitsAndEnsureSlice(t *testing.T) {
	var calls [][]string
	s := NewSystemd("/x", func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		return nil, nil
	})
	ctx := context.Background()
	if err := s.SetLimits(ctx, "s1", Limits{MemoryHigh: 12 << 30}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLimits(ctx, "s1", Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureSlice(ctx, 20<<30); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureSlice(ctx, 0); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"systemctl", "--user", "set-property", "--runtime", "aiman-session-s1.scope", "MemoryHigh=12884901888"},
		{"systemctl", "--user", "set-property", "aiman-sessions.slice", "MemoryHigh=21474836480"},
		{"systemctl", "--user", "set-property", "aiman-sessions.slice", "MemoryHigh=infinity"},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls =\n%v\nwant\n%v", calls, want)
	}
	if got := s.ScopeDir("s1"); got != "/x/aiman.slice/aiman-sessions.slice/aiman-session-s1.scope" {
		t.Fatalf("ScopeDir = %s", got)
	}
}

func TestDefaultSliceMemoryHigh(t *testing.T) {
	for in, want := range map[int64]int64{
		24 << 30: 20 << 30, 0: 0, Infinity: 0, 3 << 30: 1 << 30, 1 << 30: 1 << 29,
	} {
		if got := DefaultSliceMemoryHigh(in); got != want {
			t.Errorf("DefaultSliceMemoryHigh(%d) = %d, want %d", in, got, want)
		}
	}
}
