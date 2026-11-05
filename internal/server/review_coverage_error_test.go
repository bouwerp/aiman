package server

import (
	"errors"
	"strings"
	"testing"
)

func TestCoverageRunError(t *testing.T) {
	missing := errors.New("open cover.out: no such file")
	if got := coverageRunError(0, "ignored", missing); !errors.Is(got, missing) {
		t.Fatalf("exit 0 keeps the read error: %v", got)
	}
	got := coverageRunError(1, "cache: permission denied\n", missing)
	if !strings.Contains(got.Error(), "exited 1") || !strings.Contains(got.Error(), "cache: permission denied") {
		t.Fatalf("exit 1 carries the output: %v", got)
	}
	long := strings.Repeat("x", coverageTailBytes) + "the end"
	if got := coverageRunError(2, long, missing).Error(); !strings.HasSuffix(got, "the end") || len(got) > coverageTailBytes+100 {
		t.Fatalf("tail: %d bytes", len(got))
	}
}
