package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlackDraftCommand(t *testing.T) {
	f, _ := newSlackFake(t, map[string]string{"drafts.create": `{"ok":true,"draft":{"id":"Dr1"}}`})
	if d := runSlackJSON(t, "draft", "--channel", "C1", "--text", "hi", "--thread", "1.0"); d["id"] != "Dr1" || d["channel"] != "C1" {
		t.Fatalf("draft %v", d)
	}
	if got := len(f.seen()); got != 1 {
		t.Fatalf("calls %v", f.seen())
	}
	var out, errOut bytes.Buffer
	if err := runSlack(context.Background(), []string{"draft", "--channel", "C1", "--text", "x", "--broadcast"}, strings.NewReader(""), &out, &errOut); err == nil || !strings.Contains(errOut.String(), "slack_params") {
		t.Fatalf("broadcast without thread: %v %q", err, errOut.String())
	}
}

func TestSlackDraftWritesOff(t *testing.T) {
	f, home := newSlackFake(t, map[string]string{})
	_ = os.MkdirAll(filepath.Join(home, ".aiman"), 0o700)
	if err := os.WriteFile(filepath.Join(home, ".aiman", "config.yaml"), []byte("slack:\n  writes: \"off\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := runSlack(context.Background(), []string{"draft", "--channel", "C1", "--text", "x"}, strings.NewReader(""), &out, &errOut); err == nil || !strings.Contains(errOut.String(), "slack_writes_off") {
		t.Fatalf("writes off: %v %q", err, errOut.String())
	}
	if len(f.seen()) != 0 {
		t.Fatalf("calls %v", f.seen())
	}
}
