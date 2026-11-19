package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// refreshMargin is how long before its expiry a rotating token is renewed.
const refreshMargin = 5 * time.Minute

// ErrTokenExpired is a rotating token that cannot be renewed: the file has no
// refresh token or no client id, or Slack refused the refresh token.
var ErrTokenExpired = errors.New("the Slack user token expired and cannot be renewed; run aiman slack auth login")

// TokenSource hands a Client its user token. stale is the token Slack has
// just refused as expired, or empty for a normal call.
type TokenSource interface {
	Token(ctx context.Context, stale string) (string, error)
}

// FileTokens reads the token from the credentials file and renews a rotating
// one with its refresh token. Slack rotates the user token of a PKCE app
// every 12 hours and each refresh token works once, so every renewal runs
// under a lock on the file, which all agents on the host share.
type FileTokens struct {
	Path string
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// HTTP sends the refresh; nil is http.DefaultClient.
	HTTP *http.Client
}

func (f *FileTokens) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// Token returns the stored token, renewed first when it expires within
// refreshMargin or when it is stale. A token another process renewed since
// stale was read is returned as it is.
func (f *FileTokens) Token(ctx context.Context, stale string) (string, error) {
	creds, err := Load(f.Path)
	if err != nil {
		return ReadToken(f.Path)
	}
	if stale == "" && !f.due(creds) {
		return ReadToken(f.Path)
	}
	unlock, err := lockCredentials(f.Path + ".lock")
	if err != nil {
		return "", fmt.Errorf("lock slack credentials: %w", err)
	}
	defer unlock()
	if creds, err = Load(f.Path); err != nil {
		return "", err
	}
	if creds.AccessToken != "" && creds.AccessToken != stale && !f.due(creds) {
		return creds.AccessToken, requireUserToken(creds.AccessToken)
	}
	if creds.RefreshToken == "" || creds.ClientID == "" {
		if stale != "" || creds.ExpiresAt.IsZero() {
			return "", ErrTokenExpired
		}
		// Not renewable, but not refused yet: use it until Slack says so.
		return creds.AccessToken, requireUserToken(creds.AccessToken)
	}
	next, err := Refresh(ctx, f.HTTP, creds.ClientID, creds.RefreshToken, f.now())
	if err != nil {
		return "", err
	}
	creds.AccessToken, creds.RefreshToken, creds.ExpiresAt = next.AccessToken, next.RefreshToken, next.ExpiresAt
	if err := Store(f.Path, creds); err != nil {
		return "", err
	}
	return creds.AccessToken, nil
}

// due reports a rotating token that expires within refreshMargin.
func (f *FileTokens) due(c Credentials) bool {
	return !c.ExpiresAt.IsZero() && !f.now().Add(refreshMargin).Before(c.ExpiresAt)
}

// Refresh trades a refresh token for a new user token and refresh token.
// client_secret is omitted because the app is a PKCE public client. A
// refused refresh token is ErrTokenExpired.
func Refresh(ctx context.Context, hc *http.Client, clientID, refreshToken string, now time.Time) (Credentials, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiJoin("oauth.v2.access"), strings.NewReader(form.Encode()))
	if err != nil {
		return Credentials{}, fmt.Errorf("slack token refresh: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return Credentials{}, fmt.Errorf("slack token refresh: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Credentials{}, fmt.Errorf("slack token refresh: %w", err)
	}
	var out accessAnswer
	if err := json.Unmarshal(raw, &out); err != nil {
		return Credentials{}, errors.New("slack token refresh: bad response")
	}
	if !out.OK {
		switch out.Error {
		case "invalid_refresh_token", "invalid_grant", "token_revoked", "token_expired":
			return Credentials{}, ErrTokenExpired
		case "":
			return Credentials{}, errors.New("slack token refresh: bad response")
		}
		return Credentials{}, &apiFailure{Method: "oauth.v2.access", Code: out.Error}
	}
	c := out.credentials(now)
	if err := requireUserToken(c.AccessToken); err != nil {
		return Credentials{}, err
	}
	if c.RefreshToken == "" {
		return Credentials{}, errors.New("slack token refresh: no refresh token in the answer")
	}
	return c, nil
}

// accessAnswer is oauth.v2.access's answer. A user token, its refresh token
// and its lifetime come under authed_user from a login; a refresh answers
// them at the top level.
type accessAnswer struct {
	OK           bool   `json:"ok"`
	Error        string `json:"error"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	AuthedUser   struct {
		ID           string `json:"id"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	} `json:"authed_user"`
	Team struct {
		Name string `json:"name"`
		ID   string `json:"id"`
	} `json:"team"`
}

// credentials picks the user token, its refresh token and expiry, the user
// and the team. A token with no lifetime has no ExpiresAt.
func (a accessAnswer) credentials(now time.Time) Credentials {
	c := Credentials{AccessToken: a.AuthedUser.AccessToken, RefreshToken: a.AuthedUser.RefreshToken, UserID: a.AuthedUser.ID}
	expires := a.AuthedUser.ExpiresIn
	if c.AccessToken == "" {
		c.AccessToken, c.RefreshToken, expires = a.AccessToken, a.RefreshToken, a.ExpiresIn
	}
	if expires > 0 {
		c.ExpiresAt = now.Add(time.Duration(expires) * time.Second).UTC()
	}
	c.Team = a.Team.Name
	if c.Team == "" {
		c.Team = a.Team.ID
	}
	return c
}

// staticTokens is a token that never changes (AIMAN_SLACK_USER_TOKEN).
type staticTokens string

func (s staticTokens) Token(_ context.Context, stale string) (string, error) {
	if stale != "" {
		return "", ErrTokenExpired
	}
	return string(s), nil
}

// Tokens is the token source for the credentials file at path:
// AIMAN_SLACK_USER_TOKEN when it is set, which is never renewed, else the
// file with renewal.
func Tokens(path string) TokenSource {
	if v := strings.TrimSpace(os.Getenv("AIMAN_SLACK_USER_TOKEN")); v != "" {
		return staticTokens(v)
	}
	return &FileTokens{Path: path}
}
