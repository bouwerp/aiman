package server

import (
	"errors"
	"testing"
)

func TestListenRejectsSecondInstance(t *testing.T) {
	dir := t.TempDir()
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
