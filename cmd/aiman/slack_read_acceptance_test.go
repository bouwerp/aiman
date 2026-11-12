package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/slack"
)

// Acceptance tests for slack-parity piece A (CLI reads and the writes
// switch). Do not edit.

// slackFake points the CLI at a fake Slack API and an empty HOME, and
// records which methods were called.
type slackFake struct {
	mu      sync.Mutex
	methods []string
	answers map[string]string
}

func newSlackFake(t *testing.T, answers map[string]string) (*slackFake, string) {
	t.Helper()
	f := &slackFake{answers: answers}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := strings.TrimPrefix(r.URL.Path, "/api/")
		f.mu.Lock()
		f.methods = append(f.methods, m)
		f.mu.Unlock()
		body, ok := f.answers[m]
		if !ok {
			t.Errorf("unexpected Slack method %s", m)
			body = `{"ok":false,"error":"unknown_method"}`
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	prev := slack.APIBase
	slack.APIBase = srv.URL + "/api/"
	t.Cleanup(func() { slack.APIBase = prev })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AIMAN_SLACK_TOKEN_FILE", filepath.Join(home, "tok"))
	t.Setenv("AIMAN_SLACK_USER_TOKEN", "xoxp-cli")
	return f, home
}

func (f *slackFake) seen() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.methods...)
}

func runSlackJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	var out, errOut bytes.Buffer
	if err := runSlack(context.Background(), args, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatalf("aiman slack %v: %v %s", args, err, errOut.String())
	}
	var m map[string]any
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("aiman slack %v printed %q, want a JSON object", args, out.String())
	}
	return m
}

func TestSlackReadHistoryAndThreadPage(t *testing.T) {
	newSlackFake(t, map[string]string{
		"conversations.history": `{"ok":true,"messages":[{"user":"U1","text":"hi","ts":"2.0"}],"response_metadata":{"next_cursor":"h2"}}`,
		"conversations.replies": `{"ok":true,"messages":[{"user":"U1","text":"root","ts":"1.0"}],"response_metadata":{"next_cursor":"r2"}}`,
	})
	h := runSlackJSON(t, "history", "--channel", "C1", "--oldest", "1.0", "--latest", "3.0", "--limit", "5", "--cursor", "h1")
	if msgs, _ := h["messages"].([]any); len(msgs) != 1 || h["next_cursor"] != "h2" {
		t.Fatalf("history %v", h)
	}
	r := runSlackJSON(t, "thread", "--channel", "C1", "--ts", "1.0", "--cursor", "r1")
	if msgs, _ := r["messages"].([]any); len(msgs) != 1 || r["next_cursor"] != "r2" {
		t.Fatalf("thread %v", r)
	}
}

func TestSlackReadChannelsMembersUserReactions(t *testing.T) {
	f, _ := newSlackFake(t, map[string]string{
		"users.conversations":   `{"ok":true,"channels":[{"id":"C1","name":"eng"},{"id":"C2","name":"ops"}]}`,
		"conversations.members": `{"ok":true,"members":["U1"]}`,
		"users.info":            `{"ok":true,"user":{"id":"U1","name":"a","real_name":"A"}}`,
		"users.profile.get":     `{"ok":true,"profile":{"email":"a@x.io"}}`,
		"auth.test":             `{"ok":true,"user_id":"U1","user":"a","team":"T"}`,
		"reactions.get":         `{"ok":true,"message":{"reactions":[{"name":"eyes","count":1,"users":["U1"]}]}}`,
	})
	ch := runSlackJSON(t, "channels", "--mine", "--prefix", "en")
	if list, _ := ch["channels"].([]any); len(list) != 1 {
		t.Fatalf("channels %v", ch)
	}
	mem := runSlackJSON(t, "members", "--channel", "C1")
	if list, _ := mem["members"].([]any); len(list) != 1 {
		t.Fatalf("members %v", mem)
	}
	me := runSlackJSON(t, "user")
	if me["id"] != "U1" || me["email"] != "a@x.io" {
		t.Fatalf("user (self) %v", me)
	}
	other := runSlackJSON(t, "user", "U1")
	if other["email"] != "a@x.io" {
		t.Fatalf("user U1 %v", other)
	}
	re := runSlackJSON(t, "reactions", "--channel", "C1", "--ts", "1.0")
	if list, _ := re["reactions"].([]any); len(list) != 1 {
		t.Fatalf("reactions %v", re)
	}
	for _, m := range f.seen() {
		if m == "conversations.list" {
			t.Fatal("channels --mine called conversations.list")
		}
	}
}

func TestSlackWritesOffRefusesBeforeAnyCall(t *testing.T) {
	f, home := newSlackFake(t, map[string]string{
		"conversations.history": `{"ok":true,"messages":[]}`,
	})
	if err := os.MkdirAll(filepath.Join(home, ".aiman"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".aiman", "config.yaml"), []byte("slack:\n  writes: \"off\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"send", "--channel", "C1", "--text", "hi"},
		{"react", "--channel", "C1", "--ts", "1.0", "--emoji", "eyes"},
	} {
		var out, errOut bytes.Buffer
		err := runSlack(context.Background(), args, strings.NewReader(""), &out, &errOut)
		if err == nil || !strings.Contains(errOut.String(), "slack_writes_off") {
			t.Fatalf("%v with writes off: err %v, stderr %q", args, err, errOut.String())
		}
	}
	if got := f.seen(); len(got) != 0 {
		t.Fatalf("writes off still called Slack: %v", got)
	}
	runSlackJSON(t, "history", "--channel", "C1")
	if got := f.seen(); len(got) != 1 {
		t.Fatalf("reads must still work with writes off: %v", got)
	}
}

func TestSlackWritesOnByDefault(t *testing.T) {
	f, _ := newSlackFake(t, map[string]string{
		"reactions.add": `{"ok":true}`,
	})
	runSlackJSON(t, "react", "--channel", "C1", "--ts", "1.0", "--emoji", "eyes")
	if got := f.seen(); len(got) != 1 || got[0] != "reactions.add" {
		t.Fatalf("react with no config: %v", got)
	}
}
