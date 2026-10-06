package slack

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCodeChallengeMatchesSlackVector(t *testing.T) {
	got := CodeChallenge("secretpassword")
	if got != "ldMBaaWcQYtSATMV_IG8mf3wp7A6EW80arYoSW80ntU" {
		t.Fatalf("challenge %s", got)
	}
}

func TestAuthorizeURLIsUserScopeOnly(t *testing.T) {
	raw := AuthorizeURL("123.456", DefaultRedirectURL, "state", CodeChallenge("secretpassword"))
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("scope") != "" {
		t.Fatalf("bot scope %q", q.Get("scope"))
	}
	if !strings.Contains(q.Get("user_scope"), "chat:write") || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("query %s", q.Encode())
	}
	if q.Get("client_secret") != "" {
		t.Fatal("secret in authorize url")
	}
}

func TestExchangeOmitsClientSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("authorization header set")
		}
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		if form.Get("client_secret") != "" || form.Get("code_verifier") == "" {
			t.Errorf("form %s", body)
		}
		_, _ = io.WriteString(w, `{"ok":true,"authed_user":{"id":"U9","access_token":"xoxp-user","scope":"`+strings.Join(UserScopes, ",")+`"},"team":{"name":"Acme"}}`)
	}))
	defer srv.Close()
	APIBase = srv.URL + "/api/"
	t.Cleanup(func() { APIBase = "https://slack.com/api/" })
	creds, err := Exchange(context.Background(), "123.456", "code", "verifier", DefaultRedirectURL)
	if err != nil || creds.AccessToken != "xoxp-user" || creds.UserID != "U9" || creds.Team != "Acme" {
		t.Fatalf("%+v %v", creds.UserID, err)
	}
}

func TestExchangeRejectsBotTokenWithoutEcho(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"access_token":"xoxb-secret-value","authed_user":{"id":"UBOT"}}`)
	}))
	defer srv.Close()
	APIBase = srv.URL + "/api/"
	t.Cleanup(func() { APIBase = "https://slack.com/api/" })
	_, err := Exchange(context.Background(), "123.456", "code", "verifier", DefaultRedirectURL)
	if err == nil || strings.Contains(err.Error(), "xoxb-secret-value") {
		t.Fatalf("err %v", err)
	}
}

func TestCompleteLoginExchangesUserToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"authed_user":{"id":"U9","access_token":"xoxp-user","scope":"`+strings.Join(UserScopes, ",")+`"},"team":{"name":"Acme"}}`)
	}))
	defer srv.Close()
	APIBase = srv.URL + "/api/"
	ListenAddr = "127.0.0.1:0"
	RedirectURL = ""
	t.Cleanup(func() {
		APIBase = "https://slack.com/api/"
		ListenAddr = DefaultListenAddr
		RedirectURL = DefaultRedirectURL
	})
	creds, err := CompleteLogin(context.Background(), "123.456", func(raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		resp, err := http.Get(u.Query().Get("redirect_uri") + "?code=onecode&state=" + url.QueryEscape(u.Query().Get("state")))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		page, _ := io.ReadAll(resp.Body)
		if strings.Contains(string(page), "onecode") {
			t.Fatal("callback page echoed the code")
		}
		return nil
	})
	if err != nil || creds.AccessToken != "xoxp-user" || creds.ClientID != "123.456" {
		t.Fatalf("%s %v", creds.UserID, err)
	}
}

func TestExchangeRejectsMissingUserScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"authed_user":{"id":"U9","access_token":"xoxp-user","scope":"chat:write"},"team":{"name":"Acme"}}`)
	}))
	defer srv.Close()
	APIBase = srv.URL + "/api/"
	t.Cleanup(func() { APIBase = "https://slack.com/api/" })
	_, err := Exchange(context.Background(), "123.456", "code", "verifier", DefaultRedirectURL)
	if err == nil || !strings.Contains(err.Error(), "channels:history") || strings.Contains(err.Error(), "xoxp-user") {
		t.Fatalf("err %v", err)
	}
}
