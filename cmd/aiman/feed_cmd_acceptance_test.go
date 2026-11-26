//go:build feedaccept || feedaccept_p9

package main

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/server"
)

// Acceptance tests for event-feed piece P9 (docs/specs/event-feed.md §9).

const feedTestAction = `{"id":12,"batch_id":37,"kind":"slack_draft","rationale":"Ana asks about the settler change","priority":"high",` +
	`"answers":[811],"options":[` +
	`{"label":"Not yet","params":{"channel":"C1","thread_ts":"1700.1","text":"Not yet."}},` +
	`{"label":"No","params":{"channel":"C1","thread_ts":"1700.1","text":"No, it does not."}}],` +
	`"trace":[],"state":"proposed","created_at":"2026-11-26T10:04:00Z","expires_at":"2026-11-27T10:04:00Z"}`

var feedTestAnswers = map[string]string{
	"feed.status":  `{"type":"feed_status","enabled":true,"collector":{"state":"running","health":[]},"high_water":810,"pending":2,"open_actions":4,"high_actions":1,"batches_today":3,"settings":{}}`,
	"feed.actions": `{"type":"feed_actions","actions":[` + feedTestAction + `]}`,
	"feed.events":  `{"type":"feed_events","events":[]}`,
	"feed.memory":  `{"type":"feed_memory","bullets":[],"mutes":[]}`,
	"feed.act":     `{"type":"feed_action","action":` + strings.Replace(feedTestAction, `"proposed"`, `"done"`, 1) + `}`,
}

type feedReq struct {
	Method string
	Params map[string]any
}

// feedServe is a fake serve on a unix socket that records every request.
type feedServe struct {
	mu   sync.Mutex
	reqs []feedReq
}

func startFeedServe(t *testing.T) *feedServe {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "feedcmd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	f := &feedServe{}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	t.Setenv("AIMAN_SOCKET_PATH", sock)
	return f
}

func (f *feedServe) serve(c net.Conn) {
	defer c.Close()
	line, _ := bufio.NewReader(c).ReadBytes('\n')
	var req server.Request
	_ = json.Unmarshal(line, &req)
	var p map[string]any
	_ = json.Unmarshal(req.Params, &p)
	f.mu.Lock()
	f.reqs = append(f.reqs, feedReq{Method: req.Method, Params: p})
	f.mu.Unlock()
	answer, ok := feedTestAnswers[req.Method]
	if !ok {
		_, _ = c.Write([]byte(`{"id":"` + req.ID + `","error":{"code":"invalid_params","message":"unknown method ` + req.Method + `"}}` + "\n"))
		return
	}
	_, _ = c.Write([]byte(`{"id":"` + req.ID + `","result":` + answer + "}\n"))
}

// last returns the newest request for method.
func (f *feedServe) last(t *testing.T, method string) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.reqs) - 1; i >= 0; i-- {
		if f.reqs[i].Method == method {
			return f.reqs[i].Params
		}
	}
	t.Fatalf("no %s request; saw %+v", method, f.reqs)
	return nil
}

func (f *feedServe) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.reqs)
}

func runFeedArgs(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var err error
	out, _ := captureStdIO(t, func() { err = runWithArgs(t, t.TempDir(), append([]string{"feed"}, args...)...) })
	return out, err
}

func TestFeedCmdReadsMapToRPCs(t *testing.T) {
	cases := []struct {
		args   []string
		method string
		want   map[string]any
	}{
		{[]string{"status"}, "feed.status", map[string]any{}},
		{[]string{"memory"}, "feed.memory", map[string]any{}},
		{[]string{"actions", "--state", "proposed,snoozed", "--limit", "5"}, "feed.actions",
			map[string]any{"state": []any{"proposed", "snoozed"}, "limit": float64(5)}},
		{[]string{"events", "--batch", "37", "--digest"}, "feed.events",
			map[string]any{"batch": float64(37), "digest": true}},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			f := startFeedServe(t)
			if _, err := runFeedArgs(t, c.args...); err != nil {
				t.Fatalf("run: %v", err)
			}
			got := f.last(t, c.method)
			if len(got) != len(c.want) {
				t.Fatalf("params %v, want %v", got, c.want)
			}
			for k, v := range c.want {
				gb, _ := json.Marshal(got[k])
				wb, _ := json.Marshal(v)
				if string(gb) != string(wb) {
					t.Errorf("%s = %s, want %s", k, gb, wb)
				}
			}
		})
	}
}

func TestFeedCmdActionsPrintsIDAndRationale(t *testing.T) {
	startFeedServe(t)
	out, err := runFeedArgs(t, "actions")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "12") || !strings.Contains(out, "Ana asks about the settler change") {
		t.Fatalf("actions output %q lacks the id or rationale", out)
	}
}

// --option is the 1-based number the TUI and the mockups show; the wire index
// is 0-based (options[option], spec §11).
func TestFeedCmdApproveSendsZeroBasedOption(t *testing.T) {
	f := startFeedServe(t)
	if _, err := runFeedArgs(t, "act", "12", "approve", "--option", "2"); err != nil {
		t.Fatal(err)
	}
	p := f.last(t, "feed.act")
	if p["id"] != float64(12) || p["op"] != "approve" || p["option"] != float64(1) {
		t.Fatalf("feed.act params %v, want id 12, op approve, option 1", p)
	}
	if _, ok := p["params"]; ok {
		t.Fatalf("an unedited approve sent params: %v", p)
	}
}

// --text edits the chosen option's text and keeps its other params, so the
// executor still knows the channel and thread.
func TestFeedCmdApproveWithTextSendsEditedParams(t *testing.T) {
	f := startFeedServe(t)
	if _, err := runFeedArgs(t, "act", "12", "approve", "--option", "1", "--text", "Not yet, after the fork test."); err != nil {
		t.Fatal(err)
	}
	p := f.last(t, "feed.act")
	if p["option"] != float64(0) {
		t.Fatalf("option %v, want 0", p["option"])
	}
	params, ok := p["params"].(map[string]any)
	if !ok {
		t.Fatalf("params %v is not an object", p["params"])
	}
	if params["text"] != "Not yet, after the fork test." || params["channel"] != "C1" || params["thread_ts"] != "1700.1" {
		t.Fatalf("edited params %v", params)
	}
}

func TestFeedCmdDismiss(t *testing.T) {
	f := startFeedServe(t)
	if _, err := runFeedArgs(t, "act", "12", "dismiss", "--reason", "later", "--until", "2026-11-27T09:00:00Z", "--note", "after standup"); err != nil {
		t.Fatal(err)
	}
	p := f.last(t, "feed.act")
	if p["id"] != float64(12) || p["op"] != "dismiss" || p["reason"] != "later" || p["note"] != "after standup" {
		t.Fatalf("feed.act params %v", p)
	}
	until, err := time.Parse(time.RFC3339, p["until"].(string))
	if err != nil || !until.Equal(time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("until %v (%v)", p["until"], err)
	}

	if _, err := runFeedArgs(t, "act", "12", "dismiss"); err != nil {
		t.Fatal(err)
	}
	if p := f.last(t, "feed.act"); p["reason"] != "not_relevant" {
		t.Fatalf("plain dismiss reason %v, want not_relevant", p["reason"])
	}
}

func TestFeedCmdRefusesBadUsageWithoutCalling(t *testing.T) {
	for _, args := range [][]string{
		{"bogus"},
		{"act"},
		{"act", "x", "approve"},
		{"act", "12", "launch"},
		{"act", "12", "approve", "--option", "0"},
		{"act", "12", "dismiss", "--reason", "because"},
	} {
		f := startFeedServe(t)
		if _, err := runFeedArgs(t, args...); err == nil {
			t.Errorf("%v: no error", args)
		}
		if n := f.count(); n != 0 {
			t.Errorf("%v: sent %d requests", args, n)
		}
	}
}
