package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/slack"
)

// Acceptance tests for `aiman slack auth check` without --app: a plain
// logged-in check of the stored user token and its granted scopes. Do not edit.

// scopeFake answers auth.test with the given X-OAuth-Scopes header (omitted
// when scopes is nil) and body, and records the methods called.
func scopeFake(t *testing.T, scopes []string, body string) *[]string {
	t.Helper()
	var mu sync.Mutex
	seen := []string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := strings.TrimPrefix(r.URL.Path, "/api/")
		mu.Lock()
		seen = append(seen, m)
		mu.Unlock()
		if m != "auth.test" {
			t.Errorf("unexpected Slack method %s", m)
		}
		if scopes != nil {
			w.Header().Set("X-OAuth-Scopes", strings.Join(scopes, ","))
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
	return &seen
}

const authOK = `{"ok":true,"team":"realfi","user":"pieter","user_id":"U1"}`

func runAuthCheck(t *testing.T) (map[string]any, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := runSlack(context.Background(), []string{"auth", "check"}, strings.NewReader(""), &out, &errOut)
	var m map[string]any
	if err == nil {
		if jerr := json.Unmarshal(out.Bytes(), &m); jerr != nil {
			t.Fatalf("auth check printed %q, want a JSON object", out.String())
		}
	}
	return m, errOut.String(), err
}

func TestSlackAuthCheckWithoutAppPassesWithEveryScope(t *testing.T) {
	seen := scopeFake(t, slack.UserScopes, authOK)
	m, stderr, err := runAuthCheck(t)
	if err != nil {
		t.Fatalf("auth check: %v %s", err, stderr)
	}
	if m["ok"] != true || m["team"] != "realfi" || m["user_id"] != "U1" {
		t.Fatalf("auth check = %v", m)
	}
	if len(*seen) != 1 || (*seen)[0] != "auth.test" {
		t.Fatalf("methods = %v, want only auth.test", *seen)
	}
}

func TestSlackAuthCheckWithoutAppNamesMissingScopes(t *testing.T) {
	granted := []string{}
	for _, s := range slack.UserScopes {
		if s != "canvases:write" {
			granted = append(granted, s)
		}
	}
	scopeFake(t, granted, authOK)
	_, stderr, err := runAuthCheck(t)
	if err == nil {
		t.Fatal("auth check passed with a scope missing")
	}
	if !strings.Contains(stderr, "slack_app_settings") || !strings.Contains(stderr, "canvases:write") {
		t.Fatalf("stderr = %q, want slack_app_settings naming canvases:write", stderr)
	}
	if !strings.Contains(stderr, "aiman slack auth login") {
		t.Fatalf("stderr = %q, want the fix: aiman slack auth login", stderr)
	}
}

// Slack sends no scope header for some tokens; the check still passes on
// auth.test and says the scopes were not checked.
func TestSlackAuthCheckWithoutAppNoScopeHeader(t *testing.T) {
	scopeFake(t, nil, authOK)
	m, stderr, err := runAuthCheck(t)
	if err != nil {
		t.Fatalf("auth check: %v %s", err, stderr)
	}
	if m["ok"] != true || m["scopes_checked"] != false {
		t.Fatalf("auth check = %v, want ok and scopes_checked false", m)
	}
}

func TestSlackAuthCheckWithoutAppBadToken(t *testing.T) {
	scopeFake(t, nil, `{"ok":false,"error":"invalid_auth"}`)
	_, stderr, err := runAuthCheck(t)
	if err == nil || !strings.Contains(stderr, "invalid_auth") {
		t.Fatalf("err %v, stderr %q, want invalid_auth", err, stderr)
	}
}

func TestSlackAuthCheckWithoutAppNoToken(t *testing.T) {
	seen := scopeFake(t, slack.UserScopes, authOK)
	t.Setenv("AIMAN_SLACK_USER_TOKEN", "")
	_, stderr, err := runAuthCheck(t)
	if err == nil || !strings.Contains(stderr, "slack_unconfigured") {
		t.Fatalf("err %v, stderr %q, want slack_unconfigured", err, stderr)
	}
	if len(*seen) != 0 {
		t.Fatalf("called Slack with no token: %v", *seen)
	}
}
