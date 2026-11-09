package main

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckServePID(t *testing.T) {
	const self = 100
	cmdlines := map[int]string{
		200: "/home/u/.local/bin/aiman\x00serve\x00",
		201: "aiman\x00serve\x00",
		300: "/usr/bin/sleep\x00600\x00",
		301: "/home/u/.local/bin/aiman\x00pty\x00hold\x00",
		302: "/home/u/bin/notaiman\x00serve\x00",
		303: "/home/u/.local/bin/aiman\x00",
	}
	read := func(pid int) ([]byte, error) {
		c, ok := cmdlines[pid]
		if !ok {
			return nil, errors.New("no such process")
		}
		return []byte(c), nil
	}
	for _, tc := range []struct {
		pid     int
		goos    string
		wantErr string
	}{
		{200, "linux", ""},
		{201, "linux", ""},
		{self, "linux", "this process"},
		{self, "darwin", "this process"},
		{300, "linux", "not aiman serve"},
		{301, "linux", "not aiman serve"},
		{302, "linux", "not aiman serve"},
		{303, "linux", "not aiman serve"},
		{999, "linux", "stale"},
		{300, "darwin", ""},
	} {
		err := checkServePID(tc.pid, "/h/.aiman/serve.pid", self, tc.goos, read)
		if tc.wantErr == "" {
			if err != nil {
				t.Errorf("pid %d on %s: %v, want nil", tc.pid, tc.goos, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("pid %d on %s: %v, want an error containing %q", tc.pid, tc.goos, err, tc.wantErr)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, "/h/.aiman/serve.pid") || !strings.Contains(msg, "pid ") {
			t.Errorf("pid %d: %q should name the pid and the file", tc.pid, msg)
		}
	}
}

func TestFindServePIDSource(t *testing.T) {
	_, src, err := findServePID(func(...string) (string, error) { return "42\n", nil }, "/nope/serve.pid")
	if err != nil || src != serveUnitSource {
		t.Fatalf("source = %q, %v; want the unit", src, err)
	}
}
