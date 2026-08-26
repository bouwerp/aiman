package main

import (
	"os"
	"path/filepath"
	"testing"
)

// touch creates the path so os.Stat finds it. socketPath only stats, so a
// plain file stands in for a bound socket — and avoids macOS's ~104-byte
// socket path limit, which t.TempDir() paths routinely exceed.
func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A stale AIMAN_SOCKET_PATH must not defeat the CLI.
//
// Older builds injected the *creating* machine's path into remote sessions, so
// an agent on a remote was told the socket lived under the laptop's /Users/…
// home. Trusting that unconditionally meant every call failed with
// server_not_running while the real server was healthy — and a running agent
// cannot be re-environed without restarting it, so the CLI has to self-heal.
func TestSocketPathFallsBackWhenEnvPathIsMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	real := filepath.Join(home, ".aiman", "aiman.sock")
	touch(t, real)

	t.Setenv("AIMAN_SOCKET_PATH", "/Users/someone-else/.aiman/aiman.sock")
	got, err := socketPath()
	if err != nil {
		t.Fatalf("socketPath: %v", err)
	}
	if got != real {
		t.Fatalf("got %q, want the local socket %q", got, real)
	}
}

// A valid override is still respected: this must not become "ignore the env".
func TestSocketPathHonoursAnExistingEnvPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	custom := filepath.Join(home, "custom.sock")
	touch(t, custom)

	t.Setenv("AIMAN_SOCKET_PATH", custom)
	got, err := socketPath()
	if err != nil {
		t.Fatalf("socketPath: %v", err)
	}
	if got != custom {
		t.Fatalf("got %q, want the explicit override %q", got, custom)
	}
}

// With nothing better to point at, keep the requested path so the resulting
// error names what was actually asked for.
func TestSocketPathKeepsEnvPathWhenNoFallbackExists(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	want := "/nonexistent/aiman.sock"
	t.Setenv("AIMAN_SOCKET_PATH", want)
	got, err := socketPath()
	if err != nil {
		t.Fatalf("socketPath: %v", err)
	}
	if got != want {
		t.Fatalf("got %q, want %q so the error is diagnosable", got, want)
	}
}
