package server

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestListenRejectsSecondInstance(t *testing.T) {
	dir := shortTempDir(t)
	l1, err := Listen(dir)
	if err != nil {
		t.Fatalf("first Listen: %v", err)
	}
	t.Cleanup(func() { _ = l1.Close() })

	_, err = Listen(dir)
	if !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Listen err = %v, want ErrAlreadyRunning", err)
	}

	if err := l1.Close(); err != nil {
		t.Fatal(err)
	}
	l2, err := Listen(dir)
	if err != nil {
		t.Fatalf("Listen after Close: %v", err)
	}
	_ = l2.Close()
}

func TestListenReplacesStaleSocket(t *testing.T) {
	dir := shortTempDir(t)
	stale := filepath.Join(dir, sockFile)
	if err := os.WriteFile(stale, []byte("leftover"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, err := Listen(dir)
	if err != nil {
		t.Fatalf("Listen with stale sock: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
}
