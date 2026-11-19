package slack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// rotatingSlack answers oauth.v2.access refreshes with a new token pair per call
// and chat.postMessage with token_expired for any token but the newest.
type rotatingSlack struct {
	t         *testing.T
	refreshes atomic.Int32
	mu        sync.Mutex
	current   string
	refused   string // the refresh token Slack no longer takes
}

func (f *rotatingSlack) handler(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/oauth.v2.access"):
		body, _ := io.ReadAll(r.Body)
		form, _ := url.ParseQuery(string(body))
		if form.Get("grant_type") != "refresh_token" || form.Get("client_id") != "123.456" || form.Get("client_secret") != "" {
			f.t.Errorf("refresh form %s", body)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if form.Get("refresh_token") == f.refused {
			_, _ = io.WriteString(w, `{"ok":false,"error":"invalid_refresh_token"}`)
			return
		}
		n := f.refreshes.Add(1)
		f.refused = form.Get("refresh_token")
		f.current = fmt.Sprintf("xoxe.xoxp-new%d", n)
		_, _ = fmt.Fprintf(w, `{"ok":true,"access_token":%q,"refresh_token":"xoxe-r%d","expires_in":43200,"token_type":"user"}`, f.current, n)
	case strings.HasSuffix(r.URL.Path, "/chat.postMessage"):
		f.mu.Lock()
		cur := f.current
		f.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+cur {
			_, _ = io.WriteString(w, `{"ok":false,"error":"token_expired"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true,"channel":"C1","ts":"1.2"}`)
	default:
		f.t.Errorf("unexpected %s", r.URL.Path)
	}
}

func startRotatingSlack(t *testing.T) (*rotatingSlack, *httptest.Server) {
	t.Helper()
	f := &rotatingSlack{t: t, current: "xoxe.xoxp-current"}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	APIBase = srv.URL + "/api/"
	t.Cleanup(func() { APIBase = "https://slack.com/api/" })
	return f, srv
}

func writeCreds(t *testing.T, c Credentials) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "slack-oauth.json")
	if err := Store(path, c); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestLoginKeepsRefreshTokenAndExpiry reads the rotating user token's refresh
// token and lifetime from authed_user, where a login answers them.
func TestLoginKeepsRefreshTokenAndExpiry(t *testing.T) {
	raw := `{"ok":true,"authed_user":{"id":"U9","access_token":"xoxe.xoxp-1","refresh_token":"xoxe-1","expires_in":43200,"scope":"` +
		strings.Join(UserScopes, ",") + `"},"team":{"name":"Acme"}}`
	before := time.Now()
	c, err := userTokenFromAccess([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessToken != "xoxe.xoxp-1" || c.RefreshToken != "xoxe-1" || c.UserID != "U9" || c.Team != "Acme" {
		t.Fatalf("credentials %+v", c)
	}
	if c.ExpiresAt.Before(before.Add(11*time.Hour)) || c.ExpiresAt.After(time.Now().Add(13*time.Hour)) {
		t.Fatalf("expires at %v", c.ExpiresAt)
	}
}

// TestFileTokensRenewsBeforeExpiry renews a token that expires within the
// margin and stores the new pair, keeping the rest of the file.
func TestFileTokensRenewsBeforeExpiry(t *testing.T) {
	f, srv := startRotatingSlack(t)
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	path := writeCreds(t, Credentials{ClientID: "123.456", AccessToken: "xoxe.xoxp-current", RefreshToken: "xoxe-r0",
		ExpiresAt: now.Add(2 * time.Minute), UserID: "U9", Team: "Acme"})
	ft := &FileTokens{Path: path, Now: func() time.Time { return now }, HTTP: srv.Client()}
	tok, err := ft.Token(context.Background(), "")
	if err != nil || tok != "xoxe.xoxp-new1" || f.refreshes.Load() != 1 {
		t.Fatalf("token %q err %v refreshes %d", tok, err, f.refreshes.Load())
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.AccessToken != "xoxe.xoxp-new1" || c.RefreshToken != "xoxe-r1" || c.ClientID != "123.456" || c.UserID != "U9" || c.Team != "Acme" ||
		!c.ExpiresAt.Equal(now.Add(12*time.Hour)) {
		t.Fatalf("stored %+v", c)
	}
	// A token far from expiry is used as it is.
	if tok, err = ft.Token(context.Background(), ""); err != nil || tok != "xoxe.xoxp-new1" || f.refreshes.Load() != 1 {
		t.Fatalf("second token %q err %v refreshes %d", tok, err, f.refreshes.Load())
	}
}

// TestClientRenewsOnTokenExpired sends a call refused with token_expired
// once more with the renewed token.
func TestClientRenewsOnTokenExpired(t *testing.T) {
	f, srv := startRotatingSlack(t)
	f.current = "xoxe.xoxp-elsewhere" // the stored token is already refused
	path := writeCreds(t, Credentials{ClientID: "123.456", AccessToken: "xoxe.xoxp-old", RefreshToken: "xoxe-r0",
		ExpiresAt: time.Now().Add(time.Hour)})
	c := &Client{Tokens: &FileTokens{Path: path, HTTP: srv.Client()}, Base: srv.URL + "/api/", HTTP: srv.Client()}
	if _, err := c.Post(context.Background(), Post{Channel: "C1", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if f.refreshes.Load() != 1 {
		t.Fatalf("refreshes %d", f.refreshes.Load())
	}
}

// TestFileTokensOneRefreshForConcurrentCallers lets one caller renew; the
// others find the new token in the file and send no refresh, because Slack
// takes each refresh token once.
func TestFileTokensOneRefreshForConcurrentCallers(t *testing.T) {
	f, srv := startRotatingSlack(t)
	path := writeCreds(t, Credentials{ClientID: "123.456", AccessToken: "xoxe.xoxp-old", RefreshToken: "xoxe-r0",
		ExpiresAt: time.Now().Add(time.Hour)})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ft := &FileTokens{Path: path, HTTP: srv.Client()}
			tok, err := ft.Token(context.Background(), "xoxe.xoxp-old")
			if err == nil && tok != "xoxe.xoxp-new1" {
				err = fmt.Errorf("token %q", tok)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if f.refreshes.Load() != 1 {
		t.Fatalf("refreshes %d", f.refreshes.Load())
	}
}

// TestFileTokensWithoutRefreshTokenAsksForLogin keeps using a token stored
// without a refresh token until Slack refuses it, then asks for a login.
func TestFileTokensWithoutRefreshTokenAsksForLogin(t *testing.T) {
	_, srv := startRotatingSlack(t)
	path := writeCreds(t, Credentials{ClientID: "123.456", AccessToken: "xoxe.xoxp-old"})
	ft := &FileTokens{Path: path, HTTP: srv.Client()}
	if tok, err := ft.Token(context.Background(), ""); err != nil || tok != "xoxe.xoxp-old" {
		t.Fatalf("token %q err %v", tok, err)
	}
	if _, err := ft.Token(context.Background(), "xoxe.xoxp-old"); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("err %v", err)
	}
}

// TestRefreshRefusedAsksForLogin turns a refused refresh token into
// ErrTokenExpired, which tells the operator to log in again.
func TestRefreshRefusedAsksForLogin(t *testing.T) {
	f, srv := startRotatingSlack(t)
	f.refused = "xoxe-r0"
	path := writeCreds(t, Credentials{ClientID: "123.456", AccessToken: "xoxe.xoxp-old", RefreshToken: "xoxe-r0",
		ExpiresAt: time.Now().Add(time.Hour)})
	ft := &FileTokens{Path: path, HTTP: srv.Client()}
	if _, err := ft.Token(context.Background(), "xoxe.xoxp-old"); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("err %v", err)
	}
}

// TestEnvTokenIsNotRenewed leaves AIMAN_SLACK_USER_TOKEN alone: it is used
// as given and a refusal asks for a login.
func TestEnvTokenIsNotRenewed(t *testing.T) {
	t.Setenv("AIMAN_SLACK_USER_TOKEN", "xoxp-env")
	src := Tokens("/nonexistent")
	if tok, err := src.Token(context.Background(), ""); err != nil || tok != "xoxp-env" {
		t.Fatalf("token %q err %v", tok, err)
	}
	if _, err := src.Token(context.Background(), "xoxp-env"); !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("err %v", err)
	}
}
