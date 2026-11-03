package gateway

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// lockWait bounds how long a store waits for another process (the service or
// the CLI) to finish its read-modify-write.
const lockWait = 5 * time.Second

// writeFileAtomic writes data to a 0600 temp file beside path and renames it
// over path, so a reader sees the old or the new file and never a torn one.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// withFileLock runs fn while holding an exclusive lock on path+".lock". The
// gateway service and the CLI both update the same JSON files.
func withFileLock(path string, fn func() error) error {
	ctx, cancel := context.WithTimeout(context.Background(), lockWait)
	defer cancel()
	release, err := lockFile(ctx, path+".lock")
	if err != nil {
		return fmt.Errorf("lock %s: %w", filepath.Base(path), err)
	}
	defer release()
	return fn()
}
