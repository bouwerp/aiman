//go:build acceptance

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The CLI side of serve load guard P5 (docs/specs/serve-load-guard.md,
// Behaviour 6): `aiman host dump` finds serve's pid, sends SIGUSR1 and prints
// the dump file.
//
// Contract pinned here:
//   - func serveMainPID(run func(args ...string) (string, error), pidFile string) (int, error)
//     runs `systemctl --user show -p MainPID --value aiman-serve` through run;
//     a pid above 0 wins; an empty or 0 answer, or an error, falls back to the
//     pid file; with neither the error names both.
//   - func newestDumpSince(dir string, since time.Time) (string, bool) returns
//     the newest serve-dump-*.txt in dir modified at or after since.

func TestServeMainPIDFromSystemctl(t *testing.T) {
	var got []string
	run := func(args ...string) (string, error) {
		got = args
		return "1234\n", nil
	}
	pid, err := serveMainPID(run, filepath.Join(t.TempDir(), "serve.pid"))
	if err != nil || pid != 1234 {
		t.Fatalf("serveMainPID = %d, %v; want 1234", pid, err)
	}
	want := "systemctl --user show -p MainPID --value aiman-serve"
	if strings.Join(got, " ") != want {
		t.Errorf("ran %q, want %q", strings.Join(got, " "), want)
	}
}

func TestServeMainPIDFallsBackToPidFile(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "serve.pid")
	if err := os.WriteFile(pidFile, []byte("777\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, answer := range []string{"0\n", "", "\n"} {
		pid, err := serveMainPID(func(...string) (string, error) { return answer, nil }, pidFile)
		if err != nil || pid != 777 {
			t.Errorf("answer %q: serveMainPID = %d, %v; want 777 from the pid file", answer, pid, err)
		}
	}
	pid, err := serveMainPID(func(...string) (string, error) { return "", errors.New("no systemd") }, pidFile)
	if err != nil || pid != 777 {
		t.Errorf("systemctl error: serveMainPID = %d, %v; want 777 from the pid file", pid, err)
	}
}

func TestServeMainPIDWithNeitherSaysSo(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "serve.pid")
	_, err := serveMainPID(func(...string) (string, error) { return "0\n", nil }, pidFile)
	if err == nil {
		t.Fatal("want an error with no unit pid and no pid file")
	}
	msg := err.Error()
	if !strings.Contains(msg, "aiman-serve") || !strings.Contains(msg, "serve.pid") {
		t.Errorf("error %q should name the unit and the pid file", msg)
	}
}

func TestNewestDumpSince(t *testing.T) {
	dir := t.TempDir()
	since := time.Now()
	if _, ok := newestDumpSince(dir, since); ok {
		t.Fatal("found a dump in an empty directory")
	}
	old := filepath.Join(dir, "serve-dump-1.txt")
	if err := os.WriteFile(old, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(old, since.Add(-time.Hour), since.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, ok := newestDumpSince(dir, since); ok {
		t.Fatal("an old dump counted as new")
	}
	for _, name := range []string{"serve-dump-2.txt", "other.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := newestDumpSince(dir, since)
	if !ok || got != filepath.Join(dir, "serve-dump-2.txt") {
		t.Fatalf("newestDumpSince = %q, %v; want serve-dump-2.txt", got, ok)
	}
}
