package slack

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExportAppRejectsMismatchedSettings(t *testing.T) {
	const configToken = "xoxe.xoxp-config-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps.manifest.export" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer "+configToken {
			t.Error("config token was not sent as a bearer")
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), configToken) {
			t.Error("config token was copied into the body")
		}
		_, _ = io.WriteString(w, `{
			"ok": true,
			"manifest": {
				"oauth_config": {
					"redirect_urls": ["https://example.com/callback"],
					"scopes": {"user": ["chat:write"], "bot": ["chat:write"]},
					"pkce_enabled": false
				}
			}
		}`)
	}))
	defer srv.Close()
	APIBase = srv.URL + "/api/"
	t.Cleanup(func() { APIBase = "https://slack.com/api/" })
	err := ExportApp(context.Background(), configToken+"\n", "A0123456789")
	if err == nil {
		t.Fatal("expected a settings gap")
	}
	msg := err.Error()
	for _, want := range []string{"channels:history", "pkce is off", DefaultRedirectURL} {
		if !strings.Contains(msg, want) {
			t.Fatalf("missing %q in %s", want, msg)
		}
	}
	if strings.Contains(msg, configToken) {
		t.Fatalf("token leaked: %s", msg)
	}
}

func TestExportAppAcceptsMatchingManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		users := `"` + strings.Join(UserScopes, `","`) + `"`
		_, _ = io.WriteString(w, `{
			"ok": true,
			"manifest": {
				"oauth_config": {
					"redirect_urls": ["`+DefaultRedirectURL+`"],
					"scopes": {"user": [`+users+`]},
					"pkce_enabled": true
				}
			}
		}`)
	}))
	defer srv.Close()
	APIBase = srv.URL + "/api/"
	t.Cleanup(func() { APIBase = "https://slack.com/api/" })
	if err := ExportApp(context.Background(), "xoxe.xoxp-ok", "A0123456789"); err != nil {
		t.Fatal(err)
	}
}

func TestExportAppDoesNotEchoTokenOnAPIError(t *testing.T) {
	const configToken = "xoxe.xoxp-config-secret"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":false,"error":"invalid_auth"}`)
	}))
	defer srv.Close()
	APIBase = srv.URL + "/api/"
	t.Cleanup(func() { APIBase = "https://slack.com/api/" })
	err := ExportApp(context.Background(), configToken, "A0123456789")
	if err == nil || !strings.Contains(err.Error(), "invalid_auth") || strings.Contains(err.Error(), configToken) {
		t.Fatalf("err %v", err)
	}
}
