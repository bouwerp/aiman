package main

import "testing"

func TestNewerThanRunning(t *testing.T) {
	old := version
	defer func() { version = old }()
	version = "v0.67.1"
	for tag, want := range map[string]bool{"v0.68.0": true, "v0.67.1": false, "v0.60.0": false, "junk": false} {
		if got := newerThanRunning(tag); got != want {
			t.Errorf("newerThanRunning(%q) = %v, want %v", tag, got, want)
		}
	}
	version = "dev"
	if !newerThanRunning("v0.1.0") {
		t.Error("a dev build must take any release")
	}
}
