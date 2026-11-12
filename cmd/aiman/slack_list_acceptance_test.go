package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Acceptance tests for slack-parity piece D (CLI). Do not edit.

const cliListSchema = `{"id":"F1","title":"QA","list_metadata":{"schema":[
 {"id":"ColItem","key":"title","name":"Item","type":"text","is_primary_column":true},
 {"id":"ColStatus","key":"status","name":"Status","type":"select","options":{"choices":[{"value":"done","label":"Done"}]}}]}}`

func TestSlackListCommands(t *testing.T) {
	f, _ := newSlackFake(t, map[string]string{
		"slackLists.items.list":   `{"ok":true,"list":` + cliListSchema + `,"items":[{"id":"Rec1","fields":[{"key":"title","value":"Login fails","column_id":"ColItem"}]}]}`,
		"slackLists.items.info":   `{"ok":true,"list":` + cliListSchema + `,"record":{"id":"Rec1","fields":[]}}`,
		"slackLists.items.update": `{"ok":true}`,
	})
	rd := runSlackJSON(t, "list", "read", "F1", "--format", "json")
	if rows, _ := rd["rows"].([]any); len(rows) != 1 {
		t.Fatalf("list read %v", rd)
	}
	var out, errOut bytes.Buffer
	if err := runSlack(context.Background(), []string{"list", "read", "F1", "--format", "csv"}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatalf("csv: %v %s", err, errOut.String())
	}
	if lines := strings.Split(strings.TrimSpace(out.String()), "\n"); len(lines) != 2 || !strings.Contains(lines[0], "Item") || !strings.Contains(lines[1], "Login fails") {
		t.Fatalf("csv %q", out.String())
	}
	runSlackJSON(t, "list", "set", "F1", "Rec1", "--set", "Status=Done")
	var sawUpdate bool
	for _, m := range f.seen() {
		sawUpdate = sawUpdate || m == "slackLists.items.update"
	}
	if !sawUpdate {
		t.Fatalf("calls %v", f.seen())
	}
}

func TestSlackListWritesOff(t *testing.T) {
	f, home := newSlackFake(t, map[string]string{})
	_ = os.MkdirAll(filepath.Join(home, ".aiman"), 0o700)
	if err := os.WriteFile(filepath.Join(home, ".aiman", "config.yaml"), []byte("slack:\n  writes: \"off\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"list", "set", "F1", "Rec1", "--set", "Status=Done"},
		{"list", "add", "F1", "--set", "Item=x"},
		{"list", "update", "F1", "--name", "y"},
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
