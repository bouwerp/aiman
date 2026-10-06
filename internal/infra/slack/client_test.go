package slack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostAndHistory(t *testing.T) {
	var sawAuth, sawChannel, sawText, sawThread string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xoxb-test" {
			t.Errorf("auth %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/api/chat.postMessage":
			var req map[string]string
			_ = json.Unmarshal(body, &req)
			sawChannel, sawText, sawThread = req["channel"], req["text"], req["thread_ts"]
			_, _ = io.WriteString(w, `{"ok":true,"ts":"100.1","channel":"C1"}`)
		case "/api/conversations.history":
			_, _ = io.WriteString(w, `{"ok":true,"messages":[{"type":"message","user":"U1","text":"hi","ts":"90.1"}]}`)
		default:
			t.Errorf("path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := &Client{Token: "xoxb-test", Base: srv.URL + "/api/", HTTP: srv.Client()}

	got, err := c.Post(context.Background(), Post{Channel: "C1", Text: "hello", ThreadTS: "90.1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.TS != "100.1" || sawChannel != "C1" || sawText != "hello" || sawThread != "90.1" {
		t.Fatalf("post %+v sent channel=%s text=%s thread=%s", got, sawChannel, sawText, sawThread)
	}
	msgs, err := c.History(context.Background(), "C1", 5)
	if err != nil || len(msgs) != 1 || msgs[0].Text != "hi" {
		t.Fatalf("history %+v %v", msgs, err)
	}
	_ = sawAuth
}

func TestAPIErrorDoesNotEchoToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":false,"error":"channel_not_found"}`)
	}))
	defer srv.Close()
	c := &Client{Token: "xoxb-secret-value", Base: srv.URL + "/api/", HTTP: srv.Client()}
	_, err := c.History(context.Background(), "Cnope", 1)
	if err == nil || !strings.Contains(err.Error(), "channel_not_found") || strings.Contains(err.Error(), "xoxb-secret") {
		t.Fatalf("err %v", err)
	}
}

func TestResolveChannelByName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"channels":[{"id":"C9","name":"engineering","is_member":true}]}`)
	}))
	defer srv.Close()
	c := &Client{Token: "xoxb-test", Base: srv.URL + "/api/", HTTP: srv.Client()}
	id, err := c.ResolveChannel(context.Background(), "#engineering")
	if err != nil || id != "C9" {
		t.Fatalf("id %s err %v", id, err)
	}
	id, err = c.ResolveChannel(context.Background(), "CABC123")
	if err != nil || id != "CABC123" {
		t.Fatalf("passthrough %s %v", id, err)
	}
}

func TestWaitReturnsHumanReply(t *testing.T) {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n == 1 {
			_, _ = io.WriteString(w, `{"ok":true,"messages":[{"user":"UBOT","bot_id":"B1","text":"posted","ts":"10.1"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"messages":[{"user":"U2","text":"ship it","ts":"10.2"},{"user":"UBOT","bot_id":"B1","text":"posted","ts":"10.1"}]}`)
	}))
	defer srv.Close()
	c := &Client{Token: "xoxb-test", Base: srv.URL + "/api/", HTTP: srv.Client(), Poll: time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	msg, err := c.Wait(ctx, "C1", "10.1", "10.1", "UBOT")
	if err != nil || msg.Text != "ship it" || msg.TS != "10.2" {
		t.Fatalf("%+v %v", msg, err)
	}
}

func TestManifestIsUserOAuth(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(Manifest), &doc); err != nil {
		t.Fatal(err)
	}
	info := doc["display_information"].(map[string]any)
	if info["name"] != "Aiman" {
		t.Fatalf("name %v", info["name"])
	}
	oauth := doc["oauth_config"].(map[string]any)
	if oauth["pkce_enabled"] != true {
		t.Fatal("pkce")
	}
	scopes := oauth["scopes"].(map[string]any)
	if _, ok := scopes["bot"]; ok {
		t.Fatal("bot scopes")
	}
	users := scopes["user"].([]any)
	got := map[string]bool{}
	for _, s := range users {
		got[s.(string)] = true
	}
	for _, s := range UserScopes {
		if !got[s] {
			t.Fatalf("missing scope %s", s)
		}
	}
	raw, _ := json.Marshal(oauth["redirect_urls"])
	if !strings.Contains(string(raw), DefaultRedirectURL) {
		t.Fatalf("redirect %s", raw)
	}
}
