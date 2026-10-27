package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	slackclient "github.com/bouwerp/aiman/internal/infra/slack"
)

func TestWebhookSinkPostsPlainText(t *testing.T) {
	var gotBody, gotTitle, gotTags, gotSession, gotKind, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		gotTitle = r.Header.Get("Title")
		gotTags = r.Header.Get("Tags")
		gotSession = r.Header.Get("X-Aiman-Session")
		gotKind = r.Header.Get("X-Aiman-Kind")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	w := &WebhookSink{URL: srv.URL}
	n := domain.Notification{Text: "hello world", SessionID: "s1", Kind: domain.NotifyWaitingInput}
	if err := w.Send(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if gotBody != n.Text {
		t.Fatalf("body %q", gotBody)
	}
	if gotTitle != "aiman: "+domain.EventLabel(n.Kind) {
		t.Fatalf("title %q", gotTitle)
	}
	if gotKind != string(n.Kind) {
		t.Fatalf("kind %q", gotKind)
	}
	if !strings.Contains(gotCT, "text/plain") {
		t.Fatalf("ct %q", gotCT)
	}
	if gotSession != "s1" || gotTags != string(n.Kind) {
		t.Fatalf("h %q %q", gotSession, gotTags)
	}
}

func TestWebhookSinkErrorOmitsURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()
	w := &WebhookSink{URL: srv.URL}
	err := w.Send(context.Background(), domain.Notification{Text: "x", Kind: domain.NotifyWaitingInput})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), srv.URL) {
		t.Fatalf("url leaked: %v", err)
	}
}

func TestSlackSinkEscapes(t *testing.T) {
	var posts []map[string]string
	res := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/api/conversations.list":
			res++
			_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C123","name":"alerts","is_member":true}]}`))
		case "/api/chat.postMessage":
			var m map[string]string
			_ = json.Unmarshal(b, &m)
			posts = append(posts, m)
			_, _ = w.Write([]byte(`{"ok":true,"ts":"1.1","channel":"C123"}`))
		default:
			t.Fatalf("path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := &slackclient.Client{Token: "xoxp-test", Base: srv.URL + "/api/", HTTP: srv.Client()}
	s := &SlackSink{Client: c, Channel: "#alerts"}
	if err := s.Send(context.Background(), domain.Notification{Text: "a <b> & c"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), domain.Notification{Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if res != 1 {
		t.Fatalf("resolved %d times", res)
	}
	if len(posts) != 2 {
		t.Fatalf("posts %d", len(posts))
	}
	if posts[0]["text"] != "a &lt;b&gt; &amp; c" {
		t.Fatalf("escaped %q", posts[0]["text"])
	}
	if posts[0]["channel"] != "C123" {
		t.Fatalf("ch %q", posts[0]["channel"])
	}
}
