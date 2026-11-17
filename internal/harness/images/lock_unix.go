//go:build !windows

package images

import (
	"fmt"
	"os"
	"syscall"
)

// lockFile takes an exclusive flock on path, waiting for another holder, and
// returns its release.
func lockFile(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: the store's own lock file
	if err != nil {
		return nil, fmt.Errorf("open image store lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil { //nolint:gosec // G115: a file descriptor fits an int
		_ = f.Close()
		return nil, fmt.Errorf("image store lock: %w", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:gosec // G115: a file descriptor fits an int
		_ = f.Close()
	}, nil
}
