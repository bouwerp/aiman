package agentjob

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Credential modes of an agent container.
const (
	// CredentialAPIKey is an API key from serve's environment (or Codex's
	// auth.json), passed in the container's env file.
	CredentialAPIKey = "api_key"
	// CredentialOAuthCopy is a copy of the agent's OAuth file with the
	// refresh token removed, mounted read-only.
	CredentialOAuthCopy = "oauth_copy" //nolint:gosec // G101: a mode name, not a credential
)

// Credential is what an agent container gets to reach its provider.
type Credential struct {
	Mode string
	// Env holds API keys; it goes into the 0600 env file only.
	Env map[string]string
	// File is the credential file copy, or nil.
	File []byte
	// Expires is when an OAuth access token ends; zero for a key.
	Expires time.Time
}

// CredentialReader reads the host's agent credentials without changing them.
type CredentialReader struct {
	Home   string
	Getenv func(string) string
	Now    func() time.Time
}

// HostCredentials is CredentialReader{Home: home}.For: serve's environment
// and the agents' own credential files under home.
func HostCredentials(home string) func(agentName string, minValid time.Duration) (Credential, error) {
	return CredentialReader{Home: home}.For
}

// For prepares agentName's credential for a run that needs it valid for
// minValid. An API key wins. Otherwise the OAuth file is copied without its
// refresh token, so the container can never rotate the host's token, and an
// access token that ends within minValid is refused (the host's own agent
// refreshes it on its next use). No error names a credential's value.
func (h CredentialReader) For(agentName string, minValid time.Duration) (Credential, error) {
	getenv, now := h.Getenv, h.Now
	if getenv == nil {
		getenv = os.Getenv
	}
	if now == nil {
		now = time.Now
	}
	switch agentName {
	case "claude":
		if k := strings.TrimSpace(getenv("ANTHROPIC_API_KEY")); k != "" {
			return Credential{Mode: CredentialAPIKey, Env: map[string]string{"ANTHROPIC_API_KEY": k}}, nil
		}
		return claudeOAuth(filepath.Join(h.Home, ".claude", ".credentials.json"), now(), minValid)
	case "codex":
		home := getenv("CODEX_HOME")
		if home == "" {
			home = filepath.Join(h.Home, ".codex")
		}
		return codexAuth(filepath.Join(home, "auth.json"), strings.TrimSpace(getenv("OPENAI_API_KEY")), now(), minValid)
	}
	return Credential{}, fmt.Errorf("no credential reader for %s", agentName)
}

func checkValid(expires, now time.Time, minValid time.Duration) error {
	if left := expires.Sub(now); left < minValid {
		return fmt.Errorf("the OAuth access token ends in %s, under the %s a review needs; the host's agent refreshes it on its next use",
			left.Round(time.Minute), minValid)
	}
	return nil
}

// claudeOAuth copies claudeAiOauth from .credentials.json without
// refreshToken; other entries (MCP server tokens) are left out.
func claudeOAuth(path string, now time.Time, minValid time.Duration) (Credential, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: the agent's own credential file
	if err != nil {
		return Credential{}, errors.New("no Claude credentials (set ANTHROPIC_API_KEY for serve, or log in to claude)")
	}
	var f struct {
		OAuth map[string]json.RawMessage `json:"claudeAiOauth"`
	}
	if json.Unmarshal(b, &f) != nil || f.OAuth == nil {
		return Credential{}, errors.New("unreadable Claude credentials")
	}
	var token string
	var expiresMS int64
	if json.Unmarshal(f.OAuth["accessToken"], &token) != nil || token == "" ||
		json.Unmarshal(f.OAuth["expiresAt"], &expiresMS) != nil || expiresMS == 0 {
		return Credential{}, errors.New("the Claude credentials hold no access token with an expiry")
	}
	expires := time.UnixMilli(expiresMS)
	if err := checkValid(expires, now, minValid); err != nil {
		return Credential{}, fmt.Errorf("claude: %w", err)
	}
	keep := map[string]json.RawMessage{}
	for k, v := range f.OAuth {
		if !strings.HasPrefix(k, "refreshToken") {
			keep[k] = v
		}
	}
	out, err := json.Marshal(map[string]any{"claudeAiOauth": keep})
	if err != nil {
		return Credential{}, err
	}
	return Credential{Mode: CredentialOAuthCopy, File: out, Expires: expires}, nil
}

// codexAuth uses an API key when one is set, else copies the ChatGPT tokens
// without the refresh token and with last_refresh set to now, so Codex has
// no reason to refresh.
func codexAuth(path, envKey string, now time.Time, minValid time.Duration) (Credential, error) {
	if envKey != "" {
		return Credential{Mode: CredentialAPIKey, Env: map[string]string{"CODEX_API_KEY": envKey, "OPENAI_API_KEY": envKey}}, nil
	}
	b, err := os.ReadFile(path) //nolint:gosec // G304: the agent's own credential file
	if err != nil {
		return Credential{}, errors.New("no Codex credentials (set OPENAI_API_KEY for serve, or log in to codex)")
	}
	var f struct {
		APIKey   *string                    `json:"OPENAI_API_KEY"`
		AuthMode string                     `json:"auth_mode"`
		Tokens   map[string]json.RawMessage `json:"tokens"`
	}
	if json.Unmarshal(b, &f) != nil {
		return Credential{}, errors.New("unreadable Codex credentials")
	}
	if f.APIKey != nil && strings.TrimSpace(*f.APIKey) != "" {
		k := strings.TrimSpace(*f.APIKey)
		return Credential{Mode: CredentialAPIKey, Env: map[string]string{"CODEX_API_KEY": k, "OPENAI_API_KEY": k}}, nil
	}
	var access string
	if json.Unmarshal(f.Tokens["access_token"], &access) != nil || access == "" {
		return Credential{}, errors.New("the Codex credentials hold no access token")
	}
	expires, err := jwtExpiry(access)
	if err != nil {
		return Credential{}, fmt.Errorf("codex: %w", err)
	}
	if err := checkValid(expires, now, minValid); err != nil {
		return Credential{}, fmt.Errorf("codex: %w", err)
	}
	tokens := map[string]json.RawMessage{"refresh_token": json.RawMessage(`""`)}
	for _, k := range []string{"id_token", "access_token", "account_id"} {
		if v, ok := f.Tokens[k]; ok {
			tokens[k] = v
		}
	}
	mode := f.AuthMode
	if mode == "" {
		mode = "chatgpt"
	}
	out, err := json.Marshal(map[string]any{"auth_mode": mode, "OPENAI_API_KEY": nil, "tokens": tokens,
		"last_refresh": now.UTC().Format(time.RFC3339Nano)})
	if err != nil {
		return Credential{}, err
	}
	return Credential{Mode: CredentialOAuthCopy, File: out, Expires: expires}, nil
}

// jwtExpiry reads a JWT's exp claim without checking its signature.
func jwtExpiry(token string) (time.Time, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, errors.New("the access token is not a JWT")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, errors.New("the access token's claims are unreadable")
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(body, &claims) != nil || claims.Exp == 0 {
		return time.Time{}, errors.New("the access token has no expiry")
	}
	return time.Unix(claims.Exp, 0), nil
}
