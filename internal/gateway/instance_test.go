//go:build !windows

package gateway

import (
	"context"
	"testing"
	"time"
)

func TestLockInstanceAllowsOneGateway(t *testing.T) {
	dir := t.TempDir()
	release, err := LockInstance(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}

	short, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if _, err := LockInstance(short, dir); err == nil {
		t.Fatal("second gateway got the lock while the first holds it")
	}

	release()
	again, err := LockInstance(context.Background(), dir)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	again()
}

func TestLockInstanceWaitsForARestartingGateway(t *testing.T) {
	dir := t.TempDir()
	release, err := LockInstance(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(200*time.Millisecond, release)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	next, err := LockInstance(ctx, dir)
	if err != nil {
		t.Fatalf("new gateway did not get the lock after the old one exited: %v", err)
	}
	next()
}
