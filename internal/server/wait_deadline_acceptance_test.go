package server

import (
	"encoding/json"
	"testing"
	"time"
)

// Acceptance tests for t90: the client deadline of a waiting call follows the
// wait it asks for. Do not edit.

func TestCallDeadlineForWaitingCalls(t *testing.T) {
	const margin = 15 * time.Second
	for _, tc := range []struct {
		name   string
		method string
		params string
		want   time.Duration
	}{
		{"wait with no timeout uses the server default", "session.wait", `{"id":"x"}`, DefaultWaitTimeout + margin},
		{"wait with no params", "session.wait", ``, DefaultWaitTimeout + margin},
		{"wait past 30s", "session.wait", `{"id":"x","timeout_ms":60000}`, 60*time.Second + margin},
		{"short wait keeps the 30s floor", "session.wait", `{"id":"x","timeout_ms":1000}`, 30 * time.Second},
		{"prompt without wait is a plain call", "session.prompt", `{"id":"x","text":"hi","timeout_ms":60000}`, 30 * time.Second},
		{"prompt with wait past 30s", "session.prompt", `{"id":"x","text":"hi","wait":true,"timeout_ms":90000}`, 90*time.Second + margin},
		{"prompt with wait and no timeout", "session.prompt", `{"id":"x","text":"hi","wait":true}`, DefaultWaitTimeout + margin},
		{"other methods keep callDeadline", "session.create", `{"timeout_ms":1}`, callDeadline("session.create")},
		{"unparseable params fall back", "session.wait", `not json`, DefaultWaitTimeout + margin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := callDeadlineFor(tc.method, json.RawMessage(tc.params)); got != tc.want {
				t.Fatalf("callDeadlineFor(%s, %s) = %v, want %v", tc.method, tc.params, got, tc.want)
			}
		})
	}
}

// timeout_ms 0 asks serve to wait with no limit, so the client must not cut it
// at any ordinary deadline.
func TestCallDeadlineForUnboundedWait(t *testing.T) {
	if got := callDeadlineFor("session.wait", json.RawMessage(`{"id":"x","timeout_ms":0}`)); got < 24*time.Hour {
		t.Fatalf("unbounded wait deadline = %v, want at least 24h", got)
	}
}

func TestDefaultWaitTimeoutIsTheServersDefault(t *testing.T) {
	if DefaultWaitTimeout != 120*time.Second {
		t.Fatalf("DefaultWaitTimeout = %v, want 2m (waitState's default)", DefaultWaitTimeout)
	}
}
