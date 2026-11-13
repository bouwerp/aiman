package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/slack"
)

func TestRunSlackManifest(t *testing.T) {
	var out, errOut bytes.Buffer
	if err := runSlack(context.Background(), nil, strings.NewReader(""), &out, &errOut); err == nil {
		t.Fatal("expected usage")
	}
	out.Reset()
	errOut.Reset()
	if err := runSlack(context.Background(), []string{"manifest"}, strings.NewReader(""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"name": "Aiman"`) {
		t.Fatalf("manifest %s", out.String())
	}
}

func TestRunSlackAuthAppDoesNotEchoClientID(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AIMAN_SLACK_TOKEN_FILE", filepath.Join(dir, "tok"))
	t.Setenv("AIMAN_SLACK_USER_TOKEN", "")
	var out, errOut bytes.Buffer
	err := runSlack(context.Background(), []string{"auth", "app"}, strings.NewReader("123.456\n"), &out, &errOut)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String()+errOut.String(), "123.456") {
		t.Fatalf("client id leaked: %s %s", out.String(), errOut.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "tok"))
	if err != nil || !strings.Contains(string(got), "123.456") {
		t.Fatalf("file %q %v", got, err)
	}
	err = runSlack(context.Background(), []string{"auth", "app", "--client-id", "999.000"}, strings.NewReader(""), &out, &errOut)
	if err == nil {
		t.Fatal("argv client id must be refused")
	}
}

func TestRunSlackLoginDoesNotEchoToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"authed_user":{"id":"U9","access_token":"xoxp-from-login","scope":"`+strings.Join(slack.UserScopes, ",")+`"},"team":{"name":"Acme"}}`)
	}))
	defer srv.Close()
	slack.APIBase = srv.URL + "/api/"
	slack.ListenAddr = "127.0.0.1:0"
	slack.RedirectURL = ""
	prevOpen := slack.BrowserOpen
	slack.BrowserOpen = func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		resp, err := http.Get(u.Query().Get("redirect_uri") + "?code=onecode&state=" + url.QueryEscape(u.Query().Get("state")))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		return nil
	}
	t.Cleanup(func() {
		slack.APIBase = "https://slack.com/api/"
		slack.ListenAddr = slack.DefaultListenAddr
		slack.RedirectURL = slack.DefaultRedirectURL
		slack.BrowserOpen = prevOpen
	})
	dir := t.TempDir()
	t.Setenv("AIMAN_SLACK_TOKEN_FILE", filepath.Join(dir, "tok"))
	t.Setenv("AIMAN_SLACK_USER_TOKEN", "")
	var out, errOut bytes.Buffer
	if err := runSlack(context.Background(), []string{"auth", "app"}, strings.NewReader("123.456\n"), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	err := runSlack(context.Background(), []string{"auth", "login", "--timeout", "5s"}, strings.NewReader(""), &out, &errOut)
	if err != nil {
		t.Fatalf("%v %s", err, errOut.String())
	}
	if strings.Contains(out.String()+errOut.String(), "xoxp-from-login") {
		t.Fatal("token leaked")
	}
	if !strings.Contains(out.String(), `"user_id": "U9"`) {
		t.Fatalf("out %s", out.String())
	}
	got, err := os.ReadFile(filepath.Join(dir, "tok"))
	if err != nil || !strings.Contains(string(got), "xoxp-from-login") {
		t.Fatalf("file %q %v", got, err)
	}
}

func TestRunSlackAuthCheckReportsGapsWithoutToken(t *testing.T) {
	const secret = "xoxe.xoxp-config-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+secret {
			t.Error("bearer missing")
		}
		_, _ = io.WriteString(w, `{
			"ok": true,
			"manifest": {"oauth_config": {"redirect_urls": [], "scopes": {"user": ["chat:write"]}, "pkce_enabled": false}}
		}`)
	}))
	defer srv.Close()
	slack.APIBase = srv.URL + "/api/"
	t.Cleanup(func() { slack.APIBase = "https://slack.com/api/" })
	var out, errOut bytes.Buffer
	err := runSlack(context.Background(), []string{"auth", "check", "--app", "A0123456789"}, strings.NewReader(secret+"\n"), &out, &errOut)
	if err == nil {
		t.Fatal("expected settings gap")
	}
	combined := out.String() + errOut.String()
	if strings.Contains(combined, secret) {
		t.Fatal("configuration token leaked")
	}
	if !strings.Contains(errOut.String(), `"code":"slack_app_settings"`) || !strings.Contains(errOut.String(), "pkce is off") {
		t.Fatalf("stderr %s", errOut.String())
	}
	errOut.Reset()
	err = runSlack(context.Background(), []string{"auth", "check"}, strings.NewReader(secret), &out, &errOut)
	if err == nil || !strings.Contains(errOut.String(), `"code":"slack_params"`) || strings.Contains(errOut.String(), secret) {
		t.Fatalf("app id stderr %s err %v", errOut.String(), err)
	}
}

func TestRunSlackSendResolvesName(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/conversations.list":
			_, _ = io.WriteString(w, `{"ok":true,"channels":[{"id":"C9","name":"engineering"}]}`)
		case "/api/chat.postMessage":
			var req map[string]string
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req["channel"] != "C9" || req["markdown_text"] != "shipped" {
				t.Errorf("post %+v", req)
			}
			_, _ = io.WriteString(w, `{"ok":true,"channel":"C9","ts":"50.1"}`)
		default:
			t.Errorf("path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	slack.APIBase = srv.URL + "/api/"
	t.Cleanup(func() { slack.APIBase = "https://slack.com/api/" })
	t.Setenv("AIMAN_SLACK_USER_TOKEN", "xoxp-test")
	var out, errOut bytes.Buffer
	err := runSlack(context.Background(), []string{"send", "--channel", "engineering", "--text", "shipped"}, strings.NewReader(""), &out, &errOut)
	if err != nil {
		t.Fatalf("%v %s", err, errOut.String())
	}
	if !strings.Contains(out.String(), `"ts": "50.1"`) {
		t.Fatalf("out %s", out.String())
	}
}

func TestRunSlackWaitTimeoutIsNotAnAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth.test":
			_, _ = io.WriteString(w, `{"ok":true,"user_id":"UBOT","bot_id":"B1"}`)
		case "/api/conversations.replies":
			_, _ = io.WriteString(w, `{"ok":true,"messages":[{"type":"message","user":"UBOT","bot_id":"B1","text":"ping","ts":"1.0"}]}`)
		default:
			t.Errorf("path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	slack.APIBase = srv.URL + "/api/"
	t.Cleanup(func() { slack.APIBase = "https://slack.com/api/" })
	t.Setenv("AIMAN_SLACK_USER_TOKEN", "xoxp-test")
	var out, errOut bytes.Buffer
	err := runSlack(context.Background(), []string{
		"wait", "--channel", "C9", "--thread", "1.0", "--timeout", "30ms",
	}, strings.NewReader(""), &out, &errOut)
	if err == nil {
		t.Fatal("expected timeout")
	}
	if !strings.Contains(errOut.String(), `"code":"slack_timeout"`) {
		t.Fatalf("stderr %s", errOut.String())
	}
	if strings.Contains(errOut.String(), `"code":"slack_api"`) {
		t.Fatalf("timeout classified as api error: %s", errOut.String())
	}
}

func TestRunSlackAuthAppReturnsAfterOneLine(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("AIMAN_SLACK_TOKEN_FILE", filepath.Join(dir, "tok"))
	t.Setenv("AIMAN_SLACK_USER_TOKEN", "")
	pr, pw := io.Pipe()
	defer func() { _ = pw.Close() }()
	go func() { _, _ = io.WriteString(pw, "123.456\n") }()
	done := make(chan error, 1)
	go func() {
		var out, errOut bytes.Buffer
		done <- runSlack(context.Background(), []string{"auth", "app"}, pr, &out, &errOut)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("auth app waited for end of input after the first line")
	}
	got, err := os.ReadFile(filepath.Join(dir, "tok"))
	if err != nil || !strings.Contains(string(got), "123.456") {
		t.Fatalf("file %q %v", got, err)
	}
}

func TestSlackCommandTableMarksWrites(t *testing.T) {
	for name, want := range map[string]bool{"send": true, "react": true, "wait": false, "history": false, "user": false, "auth": false} {
		cmd, ok := slackCommands[name]
		if !ok || cmd.isWrite(nil) != want {
			t.Errorf("%s: registered %v, want write %v", name, ok, want)
		}
	}
	mixed := slackCommand{write: slackWriteSubcommands("create", "edit", "")}
	for args, want := range map[string]bool{"read X": false, "create --title t": true, "edit X": true, "": true, "--at +2h": true, "list": false} {
		if got := mixed.isWrite(strings.Fields(args)); got != want {
			t.Errorf("subcommand args %q: write %v, want %v", args, got, want)
		}
	}
	if (slackCommand{write: slackNeverWrite}).isWrite([]string{"x"}) {
		t.Error("slackNeverWrite reported a write")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate registration must panic")
		}
	}()
	registerSlack("send", slackCommand{})
}

func TestSlackFlagsBoolsAndEquals(t *testing.T) {
	flags, rest := slackFlags([]string{"--mine", "eng", "--prefix=en", "--limit", "5"}, "mine")
	if flags["mine"] != "1" || flags["prefix"] != "en" || flags["limit"] != "5" || len(rest) != 1 || rest[0] != "eng" {
		t.Fatalf("flags %v rest %v", flags, rest)
	}
}

func TestSlackLinkArgsConflictAndFill(t *testing.T) {
	const link = "https://ws.slack.com/archives/C012/p1794563498596289"
	if _, err := slackLinkArgs("react", []string{"--channel", "C999", "--ts", link}); err == nil {
		t.Fatal("conflicting --channel accepted")
	}
	got, err := slackLinkArgs("react", []string{"--ts=" + link, "--emoji", "eyes"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "--ts 1794563498.596289 --emoji eyes --channel C012"; strings.Join(got, " ") != want {
		t.Fatalf("got %q want %q", strings.Join(got, " "), want)
	}
	if _, err := slackLinkArgs("thread", []string{"--ts", "https://ws.slack.com/archives/C012"}); err == nil {
		t.Fatal("channel-only link accepted for --ts")
	}
}

func TestSlackHelpSkipsFlagValues(t *testing.T) {
	if slackWantsHelp([]string{"--text", "-h"}) {
		t.Fatal("--text -h read as help")
	}
	if !slackWantsHelp([]string{"--raw", "-h"}) || !slackWantsHelp([]string{"--channel", "C1", "--help"}) {
		t.Fatal("help not seen")
	}
}
