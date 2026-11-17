//go:build unix

package harness

import (
	"context"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A FIFO in the build output must not block the scan: the app container can
// write the tree while serve reads it.
func TestSourcemapsScanSkipsFIFO(t *testing.T) {
	src := writeTree(t, map[string]string{"dist/a.js": "x\n"})
	if err := syscall.Mkfifo(filepath.Join(src, "dist", "b.js"), 0o600); err != nil {
		t.Skip("no mkfifo:", err)
	}
	done := make(chan BrowserSourcemaps, 1)
	go func() { done <- ScanSourcemaps(context.Background(), src, DefaultSourcemapLimits()) }()
	select {
	case got := <-done:
		if got.State != SourcemapsAbsent {
			t.Fatalf("scan = %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the scan blocked on a FIFO")
	}
}
