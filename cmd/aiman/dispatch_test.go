package main

import "testing"

func TestBlockBareTUI(t *testing.T) {
	t.Parallel()
	if !blockBareTUI("1", true) {
		t.Fatal("AIMAN_ENV=1 must block TUI")
	}
	if !blockBareTUI("", false) {
		t.Fatal("non-tty must block TUI")
	}
	if blockBareTUI("", true) {
		t.Fatal("interactive tty should start TUI")
	}
}
