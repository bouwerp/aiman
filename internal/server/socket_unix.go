//go:build unix

package server

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

func (l *Listener) Close() error {
	var first error
	if l.UnixListener != nil {
		first = l.UnixListener.Close()
	}
	if l.lock != nil {
		_ = syscall.Flock(int(l.lock.Fd()), syscall.LOCK_UN)
		_ = l.lock.Close()
	}
	return first
}

func Listen(dir string) (*Listener, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating socket dir: %w", err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, lockFile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening socket lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("%w: %v", ErrAlreadyRunning, err)
	}
	path := SocketPath(dir)
	_ = os.Remove(path)
	addr, err := net.ResolveUnixAddr("unix", path)
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	ul, err := net.ListenUnix("unix", addr)
	if err != nil {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
		return nil, fmt.Errorf("listen unix: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ul.Close()
		_ = lock.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}
	return &Listener{UnixListener: ul, lock: lock, path: path}, nil
}
