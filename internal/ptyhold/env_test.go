package ptyhold

import (
	"slices"
	"testing"
)

func TestWithTerminalEnvAddsUTF8WhenMissing(t *testing.T) {
	got := withTerminalEnv([]string{"HOME=/tmp", "PATH=/bin"})
	if !slices.Contains(got, "TERM="+defaultTERM) {
		t.Fatalf("missing TERM: %v", got)
	}
	if !slices.Contains(got, "COLORTERM=truecolor") {
		t.Fatalf("missing COLORTERM: %v", got)
	}
	if !slices.Contains(got, "LANG="+defaultUTF8Locale) {
		t.Fatalf("missing UTF-8 locale: %v", got)
	}
}

func TestWithTerminalEnvKeepsExistingUTF8Locale(t *testing.T) {
	got := withTerminalEnv([]string{"LANG=en_US.UTF-8", "TERM=xterm-256color"})
	if !slices.Contains(got, "LANG=en_US.UTF-8") {
		t.Fatalf("should keep LANG: %v", got)
	}
	if slices.Contains(got, "LANG="+defaultUTF8Locale) {
		t.Fatalf("should not add a second LANG: %v", got)
	}
}

func TestWithTerminalEnvReplacesNonUTF8Locale(t *testing.T) {
	got := withTerminalEnv([]string{"LANG=C", "LC_ALL=POSIX"})
	if slices.Contains(got, "LANG=C") || slices.Contains(got, "LC_ALL=POSIX") {
		t.Fatalf("non-UTF-8 locale should be dropped: %v", got)
	}
	if !slices.Contains(got, "LANG="+defaultUTF8Locale) {
		t.Fatalf("missing replacement locale: %v", got)
	}
}

func TestWithTerminalEnvDropsDumbTERM(t *testing.T) {
	got := withTerminalEnv([]string{"TERM=dumb"})
	if slices.Contains(got, "TERM=dumb") {
		t.Fatal("TERM=dumb should be dropped")
	}
	if !slices.Contains(got, "TERM="+defaultTERM) {
		t.Fatalf("missing replacement TERM: %v", got)
	}
}
