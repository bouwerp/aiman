package main

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/debuglog"
)

func TestParseGlobalFlagsDebugBare(t *testing.T) {
	got, err := parseGlobalFlags([]string{"--debug", "session", "list"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Debug || got.DebugPath != "" {
		t.Fatalf("got %+v", got)
	}
	if !reflect.DeepEqual(got.Rest, []string{"session", "list"}) {
		t.Fatalf("rest = %v", got.Rest)
	}
}

func TestParseGlobalFlagsDebugEqualsPath(t *testing.T) {
	got, err := parseGlobalFlags([]string{"session", "--debug=/tmp/out.log", "list"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Debug || got.DebugPath != "/tmp/out.log" {
		t.Fatalf("got %+v", got)
	}
	if !reflect.DeepEqual(got.Rest, []string{"session", "list"}) {
		t.Fatalf("rest = %v", got.Rest)
	}
}

func TestParseGlobalFlagsDebugEmptyPathErrors(t *testing.T) {
	_, err := parseGlobalFlags([]string{"--debug="})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestParseGlobalFlagsLeavesCommands(t *testing.T) {
	got, err := parseGlobalFlags([]string{"--version"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Debug {
		t.Fatal("debug should be off")
	}
	if !reflect.DeepEqual(got.Rest, []string{"--version"}) {
		t.Fatalf("rest = %v", got.Rest)
	}
}

func TestParseGlobalFlagsNoArgs(t *testing.T) {
	got, err := parseGlobalFlags(nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Debug || len(got.Rest) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestParseGlobalFlagsRejectsDebugEqualsBlank(t *testing.T) {
	_, err := parseGlobalFlags([]string{"--debug=   "})
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, errUsage) {
		t.Fatal("blank path should not be usage")
	}
}

func TestStartDebugLogWritesBanner(t *testing.T) {
	p := filepath.Join(t.TempDir(), "debug.log")
	prev := log.Writer()
	if err := startDebugLog(p, []string{"aiman", "--debug", "version"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		debuglog.Close()
		log.SetOutput(prev)
	})

	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if !strings.Contains(got, "aiman debug") {
		t.Fatalf("missing banner: %q", got)
	}
	if !strings.Contains(got, "--debug") {
		t.Fatalf("missing original args: %q", got)
	}
}
