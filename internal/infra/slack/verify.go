package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ErrConfigToken and ErrAppID are refused inputs for ExportApp. Neither value
// is a secret; the configuration token itself stays out of the error text.
var (
	ErrConfigToken = errors.New("slack configuration token is empty")
	ErrAppID       = errors.New("slack app id is empty")
)

// SettingsGap is a difference between an existing Slack app and the manifest
// this CLI logs in with. The client id cannot be used to read those settings,
// so the diff comes from apps.manifest.export or from the scopes Slack grants.
type SettingsGap struct {
	MissingScopes   []string
	PKCEOff         bool
	RedirectMissing bool
}

func (g SettingsGap) Error() string {
	parts := make([]string, 0, 3)
	if len(g.MissingScopes) > 0 {
		parts = append(parts, "missing user scope "+strings.Join(g.MissingScopes, ","))
	}
	if g.PKCEOff {
		parts = append(parts, "pkce is off")
	}
	if g.RedirectMissing {
		parts = append(parts, "redirect "+expectedRedirect()+" is not registered")
	}
	if len(parts) == 0 {
		return "slack app settings"
	}
	return "slack app settings: " + strings.Join(parts, "; ")
}

func expectedRedirect() string {
	if RedirectURL != "" {
		return RedirectURL
	}
	return DefaultRedirectURL
}

func missingScopes(granted []string) []string {
	have := make(map[string]bool, len(granted))
	for _, scope := range granted {
		have[scope] = true
	}
	var missing []string
	for _, scope := range UserScopes {
		if !have[scope] {
			missing = append(missing, scope)
		}
	}
	return missing
}

func splitScopes(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func scopeGap(raw string) error {
	missing := missingScopes(splitScopes(raw))
	if len(missing) == 0 {
		return nil
	}
	return SettingsGap{MissingScopes: missing}
}

// ExportApp reads an existing app with a configuration token and reports when
// its redirect, PKCE flag, or user scopes would make auth login fail.
// The token is not stored and is not included in errors.
func ExportApp(ctx context.Context, configToken, appID string) error {
	configToken = strings.TrimSpace(configToken)
	if configToken == "" || strings.ContainsAny(configToken, "\r\n \t") {
		return ErrConfigToken
	}
	if !validAppID(appID) {
		return ErrAppID
	}
	form := url.Values{}
	form.Set("app_id", appID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiJoin("apps.manifest.export"), strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("slack apps.manifest.export: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+configToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("slack apps.manifest.export: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("slack apps.manifest.export: %w", err)
	}
	return gapFromManifest(raw)
}

func validAppID(id string) bool {
	if len(id) < 2 || id[0] != 'A' {
		return false
	}
	for _, r := range id[1:] {
		if (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func gapFromManifest(raw []byte) error {
	var out struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Manifest struct {
			OAuth struct {
				Redirects []string `json:"redirect_urls"`
				Scopes    struct {
					User []string `json:"user"`
				} `json:"scopes"`
				PKCE bool `json:"pkce_enabled"`
			} `json:"oauth_config"`
		} `json:"manifest"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("slack apps.manifest.export: bad response")
	}
	if !out.OK {
		code := out.Error
		if code == "" {
			code = "bad response"
		}
		return &apiFailure{Method: "apps.manifest.export", Code: code}
	}
	gap := SettingsGap{
		MissingScopes:   missingScopes(out.Manifest.OAuth.Scopes.User),
		PKCEOff:         !out.Manifest.OAuth.PKCE,
		RedirectMissing: !hasRedirect(out.Manifest.OAuth.Redirects),
	}
	if len(gap.MissingScopes) == 0 && !gap.PKCEOff && !gap.RedirectMissing {
		return nil
	}
	return gap
}

func hasRedirect(urls []string) bool {
	want := expectedRedirect()
	for _, raw := range urls {
		if raw == want {
			return true
		}
	}
	return false
}
