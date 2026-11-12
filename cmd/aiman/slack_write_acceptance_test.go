package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Acceptance tests for slack-parity piece E (CLI). Do not edit.

func TestSlackWriteCommands(t *testing.T) {
	f, _ := newSlackFake(t, map[string]string{
		"chat.postMessage":            `{"ok":true,"channel":"C1","ts":"5.0"}`,
		"chat.scheduleMessage":        `{"ok":true,"channel":"C1","scheduled_message_id":"Q1","post_at":"1900000000"}`,
		"chat.scheduledMessages.list": `{"ok":true,"scheduled_messages":[]}`,
		"chat.deleteScheduledMessage": `{"ok":true}`,
		"conversations.create":        `{"ok":true,"channel":{"id":"C8","name":"qa-nov"}}`,
		"conversations.open":          `{"ok":true,"channel":{"id":"D3"}}`,
	})
	if p := runSlackJSON(t, "send", "--channel", "C1", "--text", "hi", "--broadcast", "--thread", "1.0", "--no-unfurl"); p["ts"] != "5.0" {
		t.Fatalf("send %v", p)
	}
	if s := runSlackJSON(t, "schedule", "--channel", "C1", "--at", "+2h", "--text", "later"); s["id"] != "Q1" {
		t.Fatalf("schedule %v", s)
	}
	runSlackJSON(t, "schedule", "list", "--channel", "C1")
	runSlackJSON(t, "schedule", "cancel", "Q1", "--channel", "C1")
	if ch := runSlackJSON(t, "create-channel", "qa-nov"); ch["id"] != "C8" {
		t.Fatalf("create-channel %v", ch)
	}
	if dm := runSlackJSON(t, "dm", "U1"); dm["channel"] != "D3" {
		t.Fatalf("dm %v", dm)
	}
	if got := len(f.seen()); got != 6 {
		t.Fatalf("calls %v", f.seen())
	}
}

func TestSlackScheduleAtParsing(t *testing.T) {
	newSlackFake(t, map[string]string{})
	var out, errOut bytes.Buffer
	if err := runSlack(context.Background(), []string{"schedule", "--channel", "C1", "--at", "tomorrowish", "--text", "x"}, strings.NewReader(""), &out, &errOut); err == nil || !strings.Contains(errOut.String(), "slack_params") {
		t.Fatalf("bad --at: %v %q", err, errOut.String())
	}
}

func TestSlackWriteCommandsWritesOff(t *testing.T) {
	f, home := newSlackFake(t, map[string]string{})
	_ = os.MkdirAll(filepath.Join(home, ".aiman"), 0o700)
	if err := os.WriteFile(filepath.Join(home, ".aiman", "config.yaml"), []byte("slack:\n  writes: \"off\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"schedule", "--channel", "C1", "--at", "+2h", "--text", "x"},
		{"schedule", "cancel", "Q1", "--channel", "C1"},
		{"create-channel", "qa-nov"},
		{"dm", "U1"},
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
