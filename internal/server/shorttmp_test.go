package server

import (
	"os"
	"testing"
)

// shortTempDir is t.TempDir() with a short path.
//
// Unix socket paths are capped by the OS (~104 bytes on macOS, 108 on Linux),
// and t.TempDir() embeds the *test name* under a long /var/folders/… base on
// macOS. Longer-named tests therefore produced socket paths over the limit and
// failed with "bind: invalid argument" — which is why this package's failures
// tracked test-name length rather than anything in the code. Keep socket roots
// short so the tests exercise the server instead of the path limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "am")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}
