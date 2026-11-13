package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/slack"
)

// Acceptance tests for the Slack CLI fixes reported 2026-11-13 (files kept on
// reads, per-command help, --raw reads, archive links). Do not edit.

// paramFake answers like newSlackFake and also records each call's params,
// whether form-encoded or JSON.
type paramFake struct {
	mu      sync.Mutex
	calls   []string
	params  []map[string]string
	answers map[string]string
}

func newParamFake(t *testing.T, answers map[string]string) *paramFake {
	t.Helper()
	f := &paramFake{answers: answers}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := strings.TrimPrefix(r.URL.Path, "/api/")
		p := map[string]string{}
		body, _ := io.ReadAll(r.Body)
		if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			var raw map[string]any
			_ = json.Unmarshal(body, &raw)
			for k, v := range raw {
				if s, ok := v.(string); ok {
					p[k] = s
				}
			}
		} else {
			q, _ := url.ParseQuery(string(body))
			for k := range q {
				p[k] = q.Get(k)
			}
		}
		for k := range r.URL.Query() {
			p[k] = r.URL.Query().Get(k)
		}
		f.mu.Lock()
		f.calls = append(f.calls, m)
		f.params = append(f.params, p)
		f.mu.Unlock()
		ans, ok := f.answers[m]
		if !ok {
			t.Errorf("unexpected Slack method %s", m)
			ans = `{"ok":false,"error":"unknown_method"}`
		}
		_, _ = io.WriteString(w, ans)
	}))
	t.Cleanup(srv.Close)
	prev := slack.APIBase
	slack.APIBase = srv.URL + "/api/"
	t.Cleanup(func() { slack.APIBase = prev })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AIMAN_SLACK_TOKEN_FILE", filepath.Join(home, "tok"))
	t.Setenv("AIMAN_SLACK_USER_TOKEN", "xoxp-cli")
	return f
}

func (f *paramFake) last(method string) map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i] == method {
			return f.params[i]
		}
	}
	return nil
}

const richMessage = `{"type":"message","subtype":"file_share","user":"U1","text":"follow the following convention pls","ts":"1794563498.596289",
 "reply_count":2,"reactions":[{"name":"eyes","count":1,"users":["U2"]}],
 "files":[{"id":"F0CONV","name":"convention.png","mimetype":"image/png","permalink":"https://x.slack.com/files/U1/F0CONV/convention.png","url_private":"https://files.slack.com/F0CONV"}],
 "attachments":[{"title":"Remediations","text":"use REM-<n>"}],
 "blocks":[{"type":"rich_text","block_id":"b1"}]}`

func TestSlackReadsKeepFilesAttachmentsAndBlocks(t *testing.T) {
	newParamFake(t, map[string]string{
		"conversations.history": `{"ok":true,"messages":[` + richMessage + `]}`,
		"conversations.replies": `{"ok":true,"messages":[` + richMessage + `]}`,
	})
	for _, args := range [][]string{
		{"history", "--channel", "C1"},
		{"thread", "--channel", "C1", "--ts", "1794563498.596289"},
	} {
		out := runSlackJSON(t, args...)
		msgs, _ := out["messages"].([]any)
		if len(msgs) != 1 {
			t.Fatalf("%v: %v", args, out)
		}
		m := msgs[0].(map[string]any)
		files, _ := m["files"].([]any)
		if m["subtype"] != "file_share" || len(files) != 1 {
			t.Fatalf("%v: message lost subtype or files: %v", args, m)
		}
		f := files[0].(map[string]any)
		for _, k := range []string{"id", "name", "mimetype", "permalink"} {
			if f[k] == nil || f[k] == "" {
				t.Fatalf("%v: file lacks %s: %v", args, k, f)
			}
		}
		if a, _ := m["attachments"].([]any); len(a) != 1 {
			t.Fatalf("%v: attachments dropped: %v", args, m)
		}
		if b, _ := m["blocks"].([]any); len(b) != 1 {
			t.Fatalf("%v: blocks dropped: %v", args, m)
		}
		if m["reply_count"] != float64(2) {
			t.Fatalf("%v: reply_count dropped: %v", args, m)
		}
		if r, _ := m["reactions"].([]any); len(r) != 1 {
			t.Fatalf("%v: reactions dropped: %v", args, m)
		}
	}
}

func TestSlackEveryCommandHasHelp(t *testing.T) {
	f := newParamFake(t, map[string]string{})
	names := make([]string, 0, len(slackCommands))
	for name := range slackCommands {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) < 15 {
		t.Fatalf("only %d commands registered", len(names))
	}
	for _, name := range names {
		for _, flag := range []string{"--help", "-h"} {
			var out, errOut bytes.Buffer
			if err := runSlack(context.Background(), []string{name, flag}, strings.NewReader(""), &out, &errOut); err != nil {
				t.Errorf("%s %s: %v %s", name, flag, err, errOut.String())
				continue
			}
			text := out.String() + errOut.String()
			if !strings.Contains(text, "Usage: aiman slack "+name) {
				t.Errorf("%s %s printed %q, want a usage line", name, flag, text)
			}
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("help called Slack: %v", f.calls)
	}
}

func TestSlackRawReadPrintsSlacksJSON(t *testing.T) {
	answer := `{"ok":true,"messages":[` + richMessage + `],"has_more":false,"pin_count":0}`
	newParamFake(t, map[string]string{"conversations.history": answer})
	var out, errOut bytes.Buffer
	if err := runSlack(context.Background(), []string{"history", "--channel", "C1", "--raw"}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatalf("history --raw: %v %s", err, errOut.String())
	}
	var got, want any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("--raw printed %q", out.String())
	}
	_ = json.Unmarshal([]byte(answer), &want)
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if string(gb) != string(wb) {
		t.Fatalf("--raw = %s\nwant %s", gb, wb)
	}
}

func TestSlackRawIsRefusedOnWrites(t *testing.T) {
	f := newParamFake(t, map[string]string{})
	var out, errOut bytes.Buffer
	err := runSlack(context.Background(), []string{"send", "--channel", "C1", "--text", "x", "--raw"}, strings.NewReader(""), &out, &errOut)
	if err == nil || !strings.Contains(errOut.String(), "slack_params") {
		t.Fatalf("send --raw: %v %q", err, errOut.String())
	}
	if len(f.calls) != 0 {
		t.Fatalf("send --raw called Slack: %v", f.calls)
	}
}

func TestSlackArchiveLinks(t *testing.T) {
	f := newParamFake(t, map[string]string{
		"conversations.replies": `{"ok":true,"messages":[]}`,
		"conversations.history": `{"ok":true,"messages":[]}`,
		"chat.postMessage":      `{"ok":true,"channel":"C0B7EHJRC0Z","ts":"1794566638.918649"}`,
	})
	const link = "https://realfi-hq.slack.com/archives/C0B7EHJRC0Z/p1794563498596289"

	runSlackJSON(t, "thread", "--ts", link)
	if p := f.last("conversations.replies"); p["channel"] != "C0B7EHJRC0Z" || p["ts"] != "1794563498.596289" {
		t.Fatalf("thread --ts <link> sent %v", p)
	}

	runSlackJSON(t, "history", "--channel", link)
	if p := f.last("conversations.history"); p["channel"] != "C0B7EHJRC0Z" {
		t.Fatalf("history --channel <link> sent %v", p)
	}

	// A reply's link names its parent in thread_ts; the reply goes to the parent.
	reply := "https://realfi-hq.slack.com/archives/C0B7EHJRC0Z/p1794564000123456?thread_ts=1794563498.596289&cid=C0B7EHJRC0Z"
	runSlackJSON(t, "send", "--thread", reply, "--text", "ok")
	if p := f.last("chat.postMessage"); p["channel"] != "C0B7EHJRC0Z" || p["thread_ts"] != "1794563498.596289" {
		t.Fatalf("send --thread <reply link> sent %v", p)
	}

	var out, errOut bytes.Buffer
	if err := runSlack(context.Background(), []string{"thread", "--ts", "https://example.com/not-slack"}, strings.NewReader(""), &out, &errOut); err == nil || !strings.Contains(errOut.String(), "slack_params") {
		t.Fatalf("bad link: %v %q", err, errOut.String())
	}
}
