package main

import (
	"path/filepath"
	"testing"

	"github.com/bouwerp/aiman/internal/agenthook"
)

func TestTakeFlagsFromStdinIsBoolean(t *testing.T) {
	flags, rest := takeFlags([]string{"--from-stdin", "--id", "n1", "ignored"})
	if flags["from-stdin"] != "1" {
		t.Fatalf("%v", flags)
	}
	if flags["id"] != "n1" {
		t.Fatalf("id %q", flags["id"])
	}
	if len(rest) != 1 || rest[0] != "ignored" {
		t.Fatalf("rest %v", rest)
	}
}

func TestReportNativeToServeWhenDown(t *testing.T) {
	err := reportNativeToServe(filepath.Join(t.TempDir(), "aiman.sock"), "s1", agenthook.Report{Native: agenthook.Native{ID: "n"}})
	if err != nil {
		t.Fatalf("missing serve must be silent: %v", err)
	}
}
