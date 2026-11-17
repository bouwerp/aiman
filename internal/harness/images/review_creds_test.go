package images_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/harness/images"
)

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(v)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func fakeJWT(exp time.Time) string {
	enc := base64.RawURLEncoding.EncodeToString
	return enc([]byte(`{"alg":"none"}`)) + "." + enc([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix()))) + ".sig"
}

func TestCredentialsNeverCarryRefreshToken(t *testing.T) {
	home := t.TempDir()
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	noEnv := func(string) string { return "" }
	h := images.HostCredentials{Home: home, Getenv: noEnv, Now: func() time.Time { return now }}

	writeJSON(t, filepath.Join(home, ".claude", ".credentials.json"), map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "AT-CLAUDE", "refreshToken": "RT-CLAUDE",
			"expiresAt": now.Add(3 * time.Hour).UnixMilli(), "refreshTokenExpiresAt": 1, "scopes": []string{"user:inference"}},
		"mcpOAuth": map[string]any{"x": map[string]string{"accessToken": "MCP-TOKEN"}},
	})
	c, err := h.For("claude", 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if c.Mode != images.CredentialOAuthCopy || !strings.Contains(string(c.File), "AT-CLAUDE") ||
		strings.Contains(string(c.File), "RT-CLAUDE") || strings.Contains(string(c.File), "MCP-TOKEN") || strings.Contains(string(c.File), "refreshToken") {
		t.Fatalf("claude copy = %s", c.File)
	}
	_, err = h.For("claude", 4*time.Hour)
	if err == nil || strings.Contains(err.Error(), "AT-CLAUDE") {
		t.Fatalf("a token ending before the run = %v", err)
	}

	writeJSON(t, filepath.Join(home, ".codex", "auth.json"), map[string]any{
		"auth_mode": "chatgpt", "OPENAI_API_KEY": nil, "last_refresh": "2026-11-01T00:00:00Z",
		"tokens": map[string]any{"id_token": "ID", "access_token": fakeJWT(now.Add(48 * time.Hour)), "refresh_token": "RT-CODEX", "account_id": "acct"},
	})
	c, err = h.For("codex", 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var auth struct {
		LastRefresh string            `json:"last_refresh"`
		Tokens      map[string]string `json:"tokens"`
	}
	if err := json.Unmarshal(c.File, &auth); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(c.File), "RT-CODEX") || auth.Tokens["refresh_token"] != "" || auth.Tokens["account_id"] != "acct" ||
		auth.LastRefresh != now.Format(time.RFC3339Nano) {
		t.Fatalf("codex copy = %s", c.File)
	}

	withKey := images.HostCredentials{Home: home, Now: func() time.Time { return now },
		Getenv: func(k string) string { return map[string]string{"ANTHROPIC_API_KEY": "sk-test"}[k] }}
	c, err = withKey.For("claude", time.Hour)
	if err != nil || c.Mode != images.CredentialAPIKey || c.File != nil || c.Env["ANTHROPIC_API_KEY"] != "sk-test" {
		t.Fatalf("an API key must win: %+v, %v", c.Mode, err)
	}
}
