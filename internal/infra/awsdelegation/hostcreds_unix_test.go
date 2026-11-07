//go:build !windows

package awsdelegation

import (
	"errors"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestHostCredsRefuseFIFO fails rather than hangs if either path opens a FIFO.
func TestHostCredsRefuseFIFO(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "credentials")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan [2]error, 1)
	go func() {
		_, readErr := ReadHostCredentialProfiles(fifo)
		done <- [2]error{readErr, WriteHostCredentials(fifo, filepath.Join(dir, "lock"), "phone", validCreds())}
	}()
	select {
	case errs := <-done:
		for i, err := range errs {
			if !errors.Is(err, ErrUnavailable) {
				t.Errorf("call %d: %v, want ErrUnavailable", i, err)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked on a FIFO")
	}
}
