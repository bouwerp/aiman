package ptyhold

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCursorQueryScannerRepliesToPositionReport(t *testing.T) {
	var s cursorQueryScanner
	got := s.Replies([]byte("hello \x1b[6n there"))
	if !bytes.Equal(got, cursorPositionReply) {
		t.Fatalf("reply %q", got)
	}
	if again := s.Replies([]byte("plain")); again != nil {
		t.Fatalf("plain output replied %q", again)
	}
}

func TestCursorQueryScannerSpansChunks(t *testing.T) {
	var s cursorQueryScanner
	if got := s.Replies([]byte("\x1b[6")); got != nil {
		t.Fatalf("partial replied %q", got)
	}
	got := s.Replies([]byte("n"))
	if !bytes.Equal(got, cursorPositionReply) {
		t.Fatalf("reply %q", got)
	}
}

func TestHolderAnswersCursorPositionQuery(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "ptyc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	replyPath := filepath.Join(root, "reply")
	script := filepath.Join(root, "ask.py")
	body := "import os, select, sys, termios, tty\n" +
		"tty.setraw(0)\n" +
		"termios.tcflush(0, termios.TCIFLUSH)\n" +
		"sys.stdout.buffer.write(b'\\x1b[6n')\n" +
		"sys.stdout.flush()\n" +
		"r, _, _ = select.select([0], [], [], 2)\n" +
		"data = os.read(0, 32) if r else b''\n" +
		"open(sys.argv[1], 'wb').write(data)\n"
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	spec := Spec{
		ID:      "cursor",
		Dir:     root,
		Command: "python3 " + script + " " + replyPath,
		Cols:    80,
		Rows:    24,
	}
	dir := Dir(root, spec.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, RequestFile), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- Run(root, spec.ID) }()

	deadline := time.Now().Add(5 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		if b, rerr := os.ReadFile(replyPath); rerr == nil {
			got = b
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !bytes.Contains(got, cursorPositionReply) {
		exit, _ := os.ReadFile(filepath.Join(dir, ExitFile))
		logb, _ := os.ReadFile(filepath.Join(dir, HolderLogFile))
		t.Fatalf("holder reply %q exit %q log %q", got, exit, logb)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("holder did not exit after the probe")
	}
}

func TestCursorQueryScannerIgnoresOtherCSI(t *testing.T) {
	var s cursorQueryScanner
	if got := s.Replies([]byte("\x1b[6m\x1b[31m\x1b[?25h")); got != nil {
		t.Fatalf("other CSI replied %q", got)
	}
}
