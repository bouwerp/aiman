package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// TokenSource returns a bearer token for the Cloudflare API.
type TokenSource func(ctx context.Context) (string, error)

// cfRefreshMargin is how long before expiry a token is renewed.
const cfRefreshMargin = 2 * time.Minute

// CFOAuthToken reads oauth_token and expiration_time from the cf CLI's
// config file (~/.config/cloudflare/config/default.json). Within 2 minutes
// of expiry (or past it) it calls refresh (serve: runs `cf auth whoami`,
// which renews the token) and reads the file again. It never logs the token.
func CFOAuthToken(path string, refresh func(ctx context.Context) error) TokenSource {
	return func(ctx context.Context) (string, error) {
		tok, exp, err := readCFToken(path)
		if err == nil && time.Until(exp) > cfRefreshMargin {
			return tok, nil
		}
		if refresh == nil {
			if err != nil {
				return "", err
			}
			return "", fmt.Errorf("cf oauth token in %s expires at %s and cannot be renewed", path, exp.Format(time.RFC3339))
		}
		if rerr := refresh(ctx); rerr != nil {
			return "", fmt.Errorf("renew cf oauth token: %w", rerr)
		}
		tok, exp, err = readCFToken(path)
		if err != nil {
			return "", err
		}
		if !time.Now().Before(exp) {
			return "", fmt.Errorf("cf oauth token in %s expired at %s; run cf auth login", path, exp.Format(time.RFC3339))
		}
		return tok, nil
	}
}

// readCFToken reads the token and its expiry. Errors name the file, never
// the token.
func readCFToken(path string) (string, time.Time, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("cf oauth config: %w", err)
	}
	var f struct {
		Token      string `json:"oauth_token"`
		Expiration string `json:"expiration_time"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return "", time.Time{}, fmt.Errorf("cf oauth config %s is not JSON", path)
	}
	if strings.TrimSpace(f.Token) == "" {
		return "", time.Time{}, errors.New("cf oauth config " + path + " has no oauth_token; run cf auth login")
	}
	exp, err := time.Parse(time.RFC3339Nano, f.Expiration)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("cf oauth config %s: expiration_time %q is not RFC 3339", path, f.Expiration)
	}
	return f.Token, exp, nil
}
