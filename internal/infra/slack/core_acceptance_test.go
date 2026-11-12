package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// Acceptance tests for slack-parity piece A (core and reads). Do not edit.

// fakeCall is one request the fake Slack API saw.
type fakeCall struct {
	Method      string
	HTTPMethod  string
	ContentType string
	Params      map[string]string
}

// fakeSlack answers each Slack method with the JSON its handler returns and
// records every call. A method with no handler fails the test.
type fakeSlack struct {
	t        *testing.T
	mu       sync.Mutex
	calls    []fakeCall
	handlers map[string]func(p map[string]string) (int, http.Header, string)
}

func newFakeSlack(t *testing.T) (*fakeSlack, *Client) {
	t.Helper()
	f := &fakeSlack{t: t, handlers: map[string]func(map[string]string) (int, http.Header, string){}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := &Client{Token: "xoxp-test", Base: srv.URL + "/api/", HTTP: srv.Client()}
	return f, c
}

func (f *fakeSlack) on(method, body string) {
	f.handlers[method] = func(map[string]string) (int, http.Header, string) { return 200, nil, body }
}

func (f *fakeSlack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := strings.TrimPrefix(r.URL.Path, "/api/")
	p := requestParams(r)
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{Method: method, HTTPMethod: r.Method, ContentType: r.Header.Get("Content-Type"), Params: p})
	h := f.handlers[method]
	f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer xoxp-test" {
		f.t.Errorf("%s: authorization %q", method, r.Header.Get("Authorization"))
	}
	if h == nil {
		f.t.Errorf("unexpected Slack method %s %v", method, p)
		_, _ = io.WriteString(w, `{"ok":false,"error":"unknown_method"}`)
		return
	}
	code, hdr, body := h(p)
	for k, v := range hdr {
		w.Header()[k] = v
	}
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}

func (f *fakeSlack) called(method string) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []fakeCall
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

// requestParams reads parameters from a JSON body, a form body or the query.
func requestParams(r *http.Request) map[string]string {
	p := map[string]string{}
	for k, v := range r.URL.Query() {
		p[k] = v[0]
	}
	body, _ := io.ReadAll(r.Body)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		for k, v := range m {
			switch x := v.(type) {
			case string:
				p[k] = x
			default:
				b, _ := json.Marshal(x)
				p[k] = string(b)
			}
		}
		return p
	}
	form, _ := url.ParseQuery(string(body))
	for k, v := range form {
		p[k] = v[0]
	}
	return p
}

func TestPageCursorFromResponseMetadata(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("conversations.history", `{"ok":true,"messages":[{"user":"U1","text":"a","ts":"2.0"},{"user":"U2","text":"b","ts":"1.0"}],"response_metadata":{"next_cursor":"dXNlcjpVMDYx"}}`)
	page, err := c.HistoryPage(context.Background(), HistoryQuery{Channel: "C1", Oldest: "0.5", Latest: "3.0", Limit: 2, Cursor: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].Text != "a" || page.NextCursor != "dXNlcjpVMDYx" {
		t.Fatalf("page %+v", page)
	}
	got := f.called("conversations.history")[0].Params
	for k, want := range map[string]string{"channel": "C1", "oldest": "0.5", "latest": "3.0", "limit": "2", "cursor": "abc"} {
		if got[k] != want {
			t.Errorf("history %s = %q, want %q (all %v)", k, got[k], want, got)
		}
	}
	b, _ := json.Marshal(page)
	if !strings.Contains(string(b), `"items"`) || !strings.Contains(string(b), `"next_cursor":"dXNlcjpVMDYx"`) {
		t.Fatalf("Page JSON %s", b)
	}
}

func TestRetryHonoursRetryAfterTwice(t *testing.T) {
	f, c := newFakeSlack(t)
	var slept []time.Duration
	c.Sleep = func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	n := 0
	f.handlers["conversations.replies"] = func(map[string]string) (int, http.Header, string) {
		n++
		if n <= 2 {
			return 429, http.Header{"Retry-After": {"3"}}, `{"ok":false,"error":"ratelimited"}`
		}
		return 200, nil, `{"ok":true,"messages":[{"user":"U1","text":"x","ts":"1.0"}]}`
	}
	page, err := c.RepliesPage(context.Background(), RepliesQuery{Channel: "C1", TS: "1.0"})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("replies %+v %v", page, err)
	}
	if len(slept) != 2 || slept[0] != 3*time.Second || slept[1] != 3*time.Second {
		t.Fatalf("slept %v, want two 3s waits", slept)
	}
}

func TestRetryGivesUpAfterTwoRetries(t *testing.T) {
	f, c := newFakeSlack(t)
	c.Sleep = func(context.Context, time.Duration) error { return nil }
	f.handlers["conversations.history"] = func(map[string]string) (int, http.Header, string) {
		return 429, http.Header{"Retry-After": {"1"}}, `{"ok":false,"error":"ratelimited"}`
	}
	_, err := c.HistoryPage(context.Background(), HistoryQuery{Channel: "C1"})
	if err == nil || !strings.Contains(err.Error(), "ratelimited") {
		t.Fatalf("err %v, want ratelimited", err)
	}
	if got := len(f.called("conversations.history")); got != 3 {
		t.Fatalf("%d attempts, want 3", got)
	}
}

func TestCallFormAndGet(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("files.getUploadURLExternal", `{"ok":true,"upload_url":"u","file_id":"F1"}`)
	f.on("users.profile.get", `{"ok":true,"profile":{"email":"a@b.c"}}`)
	var up struct {
		FileID string `json:"file_id"`
	}
	if err := c.callForm(context.Background(), "files.getUploadURLExternal", url.Values{"filename": {"a.txt"}, "length": {"3"}}, &up); err != nil || up.FileID != "F1" {
		t.Fatalf("callForm %+v %v", up, err)
	}
	fc := f.called("files.getUploadURLExternal")[0]
	if fc.HTTPMethod != http.MethodPost || !strings.HasPrefix(fc.ContentType, "application/x-www-form-urlencoded") || fc.Params["length"] != "3" {
		t.Fatalf("callForm request %+v", fc)
	}
	var prof map[string]any
	if err := c.get(context.Background(), "users.profile.get", url.Values{"user": {"U1"}}, &prof); err != nil {
		t.Fatal(err)
	}
	gc := f.called("users.profile.get")[0]
	if gc.HTTPMethod != http.MethodGet || gc.Params["user"] != "U1" {
		t.Fatalf("get request %+v", gc)
	}
}

func TestResponsesOverOneMiBAreRead(t *testing.T) {
	f, c := newFakeSlack(t)
	big := strings.Repeat("x", 2<<20)
	f.on("conversations.history", `{"ok":true,"messages":[{"user":"U1","text":"`+big+`","ts":"1.0"}]}`)
	page, err := c.HistoryPage(context.Background(), HistoryQuery{Channel: "C1"})
	if err != nil || len(page.Items) != 1 || len(page.Items[0].Text) != len(big) {
		t.Fatalf("big history: %v", err)
	}
}

func TestReadsHistoryOfAUserOpensTheDM(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("conversations.open", `{"ok":true,"channel":{"id":"D9"}}`)
	f.on("conversations.history", `{"ok":true,"messages":[]}`)
	if _, err := c.HistoryPage(context.Background(), HistoryQuery{Channel: "U123"}); err != nil {
		t.Fatal(err)
	}
	if got := f.called("conversations.open")[0].Params["users"]; got != "U123" {
		t.Fatalf("open users %q", got)
	}
	if got := f.called("conversations.history")[0].Params["channel"]; got != "D9" {
		t.Fatalf("history channel %q, want the DM", got)
	}
}

func TestReadsRepliesPage(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("conversations.replies", `{"ok":true,"messages":[{"user":"U1","text":"root","ts":"1.0"}],"response_metadata":{"next_cursor":"n2"}}`)
	page, err := c.RepliesPage(context.Background(), RepliesQuery{Channel: "C1", TS: "1.0", Cursor: "n1", Limit: 50})
	if err != nil || page.NextCursor != "n2" {
		t.Fatalf("%+v %v", page, err)
	}
	p := f.called("conversations.replies")[0].Params
	if p["ts"] != "1.0" || p["cursor"] != "n1" || p["limit"] != "50" {
		t.Fatalf("replies params %v", p)
	}
}

func TestReadsChannelsMineAndPrefix(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("users.conversations", `{"ok":true,"channels":[{"id":"C1","name":"eng-core"},{"id":"C2","name":"random"},{"id":"C3","name":"ENG-ops"}],"response_metadata":{"next_cursor":"m"}}`)
	page, err := c.ChannelsPage(context.Background(), ChannelQuery{Mine: true, Prefix: "eng", Types: []string{"public_channel", "private_channel"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[0].ID != "C1" || page.Items[1].ID != "C3" || page.NextCursor != "m" {
		t.Fatalf("channels %+v", page)
	}
	if got := f.called("users.conversations")[0].Params["types"]; got != "public_channel,private_channel" {
		t.Fatalf("types %q", got)
	}
	if len(f.called("conversations.list")) != 0 {
		t.Fatal("--mine must use users.conversations")
	}
	f.on("conversations.list", `{"ok":true,"channels":[{"id":"C5","name":"x"}]}`)
	if page, err := c.ChannelsPage(context.Background(), ChannelQuery{}); err != nil || len(page.Items) != 1 {
		t.Fatalf("all channels %+v %v", page, err)
	}
}

func TestReadsMembersWithUsers(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("conversations.members", `{"ok":true,"members":["U1","U2"],"response_metadata":{"next_cursor":"c2"}}`)
	f.handlers["users.info"] = func(p map[string]string) (int, http.Header, string) {
		return 200, nil, `{"ok":true,"user":{"id":"` + p["user"] + `","name":"n` + p["user"] + `","real_name":"R"}}`
	}
	page, err := c.Members(context.Background(), "C1", "", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Items[1].ID != "U2" || page.Items[1].Name != "nU2" || page.NextCursor != "c2" {
		t.Fatalf("members %+v", page)
	}
}

func TestReadsProfileSelfAndOther(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("auth.test", `{"ok":true,"user_id":"U7","user":"me","team":"T"}`)
	f.handlers["users.info"] = func(p map[string]string) (int, http.Header, string) {
		return 200, nil, `{"ok":true,"user":{"id":"` + p["user"] + `","name":"n","real_name":"Real","tz":"Africa/Johannesburg"}}`
	}
	f.on("users.profile.get", `{"ok":true,"profile":{"email":"me@x.io","title":"Eng","display_name":"Me","status_text":"busy"}}`)
	self, err := c.Profile(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if self.ID != "U7" || self.Email != "me@x.io" || self.Title != "Eng" || self.DisplayName != "Me" || self.StatusText != "busy" || self.TZ != "Africa/Johannesburg" {
		t.Fatalf("self %+v", self)
	}
	pc := f.called("users.profile.get")[0]
	if pc.HTTPMethod != http.MethodGet || pc.Params["user"] != "U7" {
		t.Fatalf("profile.get %+v", pc)
	}
	if other, err := c.Profile(context.Background(), "U2"); err != nil || other.ID != "U2" {
		t.Fatalf("other %+v %v", other, err)
	}
}

func TestReadsReactions(t *testing.T) {
	f, c := newFakeSlack(t)
	f.on("reactions.get", `{"ok":true,"type":"message","message":{"ts":"1.0","reactions":[{"name":"eyes","count":2,"users":["U1","U2"]}]}}`)
	got, err := c.Reactions(context.Background(), "C1", "1.0")
	if err != nil || len(got) != 1 || got[0].Name != "eyes" || got[0].Count != 2 || len(got[0].Users) != 2 {
		t.Fatalf("reactions %+v %v", got, err)
	}
	p := f.called("reactions.get")[0].Params
	if p["channel"] != "C1" || p["timestamp"] != "1.0" {
		t.Fatalf("reactions.get params %v", p)
	}
}
