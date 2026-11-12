package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Acceptance tests for slack-parity piece C (CLI). Do not edit.

func TestSlackCanvasCommands(t *testing.T) {
	f, home := newSlackFake(t, map[string]string{
		"canvases.create":          `{"ok":true,"canvas_id":"F1"}`,
		"canvases.getContent":      `{"ok":true,"content":"# Plan\n"}`,
		"canvases.sections.lookup": `{"ok":true,"sections":[{"id":"s1"}]}`,
		"canvases.edit":            `{"ok":true}`,
	})
	md := filepath.Join(home, "plan.md")
	if err := os.WriteFile(md, []byte("# Plan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cr := runSlackJSON(t, "canvas", "create", "--title", "Plan", "--text-file", md)
	if cr["canvas_id"] != "F1" {
		t.Fatalf("create %v", cr)
	}
	rd := runSlackJSON(t, "canvas", "read", "F1")
	if rd["markdown"] != "# Plan\n" {
		t.Fatalf("read %v", rd)
	}
	if secs, _ := rd["sections"].([]any); len(secs) != 1 {
		t.Fatalf("read sections %v", rd)
	}
	runSlackJSON(t, "canvas", "edit", "F1", "--op", "append", "--text-file", md)

	before := len(f.seen())
	var out, errOut bytes.Buffer
	err := runSlack(context.Background(), []string{"canvas", "edit", "F1", "--op", "replace", "--text-file", md}, strings.NewReader(""), &out, &errOut)
	if err == nil || !strings.Contains(errOut.String(), "slack_params") {
		t.Fatalf("replace without --section or --all: %v %q", err, errOut.String())
	}
	if len(f.seen()) != before {
		t.Fatal("refused edit called Slack")
	}
}

func TestSlackCanvasWritesOff(t *testing.T) {
	f, home := newSlackFake(t, map[string]string{})
	if err := os.MkdirAll(filepath.Join(home, ".aiman"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aiman", "config.yaml"), []byte("slack:\n  writes: \"off\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, "a.txt")
	_ = os.WriteFile(file, []byte("x"), 0o600)
	for _, args := range [][]string{
		{"canvas", "create", "--title", "T", "--text", "x"},
		{"canvas", "edit", "F1", "--op", "append", "--text", "x"},
		{"upload", file, "--channel", "C1"},
	} {
		var out, errOut bytes.Buffer
		if err := runSlack(context.Background(), args, strings.NewReader(""), &out, &errOut); err == nil || !strings.Contains(errOut.String(), "slack_writes_off") {
			t.Fatalf("%v: %v %q", args, err, errOut.String())
		}
	}
	if len(f.seen()) != 0 {
		t.Fatalf("writes off called Slack: %v", f.seen())
	}
}
