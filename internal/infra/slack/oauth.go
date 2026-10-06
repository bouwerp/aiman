package slack

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

// UserScopes are the user token scopes the manifest requests. A user token
// with chat:write posts as that member. There is no bot scope, so Slack
// does not create an Aiman bot user.
var UserScopes = []string{
	"chat:write",
	"channels:history",
	"groups:history",
	"im:history",
	"mpim:history",
	"channels:read",
	"groups:read",
	"im:read",
	"mpim:read",
	"users:read",
	"reactions:write",
}

// DefaultRedirectURL is the loopback URL baked into Manifest. Slack redirects
// the browser here after the member approves the user scopes.
const DefaultRedirectURL = "http://127.0.0.1:43127/slack/callback"

// DefaultListenAddr is the loopback socket that receives that redirect.
const DefaultListenAddr = "127.0.0.1:43127"

// RedirectURL and ListenAddr match the manifest. Tests point ListenAddr at
// an ephemeral port and clear RedirectURL so the callback host is that port.
var (
	RedirectURL = DefaultRedirectURL
	ListenAddr  = DefaultListenAddr
)

// AuthorizeBase is the user-scope consent URL. Tests do not call Slack.
var AuthorizeBase = "https://slack.com/oauth/v2/authorize"

// BrowserOpen shows the consent URL. The CLI also prints it. Tests replace
// this so a login does not launch a browser.
var BrowserOpen = openBrowser

func openBrowser(rawURL string) error {
	if !strings.HasPrefix(rawURL, AuthorizeBase+"?") {
		return fmt.Errorf("refusing to open a non-Slack URL")
	}
	// The URL is the consent page this process just built.
	cmd := exec.Command("xdg-open", rawURL) //nolint:gosec // G204: Slack consent URL built above
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// CodeChallenge is the S256 PKCE challenge. Slack compares it with the
// verifier sent to oauth.v2.access, so the CLI never stores a client secret.
func CodeChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func newVerifier() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("slack oauth: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// AuthorizeURL is the page the member opens to grant user scopes.
func AuthorizeURL(clientID, redirect, state, challenge string) string {
	q := url.Values{}
	q.Set("client_id", clientID)
	q.Set("user_scope", strings.Join(UserScopes, ","))
	q.Set("redirect_uri", redirect)
	q.Set("state", state)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	return AuthorizeBase + "?" + q.Encode()
}

// Exchange trades the one-time code for a user token. client_secret is omitted
// because the app is a PKCE public client.
func Exchange(ctx context.Context, clientID, code, verifier, redirect string) (Credentials, error) {
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", redirect)
	form.Set("grant_type", "authorization_code")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiJoin("oauth.v2.access"), strings.NewReader(form.Encode()))
	if err != nil {
		return Credentials{}, fmt.Errorf("slack oauth.v2.access: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Credentials{}, fmt.Errorf("slack oauth.v2.access: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Credentials{}, fmt.Errorf("slack oauth.v2.access: %w", err)
	}
	return userTokenFromAccess(raw)
}

func apiJoin(method string) string {
	base := APIBase
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base + method
}

func userTokenFromAccess(raw []byte) (Credentials, error) {
	var out struct {
		OK           bool   `json:"ok"`
		Error        string `json:"error"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		AuthedUser   struct {
			ID          string `json:"id"`
			AccessToken string `json:"access_token"`
		} `json:"authed_user"`
		Team struct {
			Name string `json:"name"`
			ID   string `json:"id"`
		} `json:"team"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Credentials{}, fmt.Errorf("slack oauth.v2.access: bad response")
	}
	if !out.OK {
		code := out.Error
		if code == "" {
			code = "bad response"
		}
		return Credentials{}, &apiFailure{Method: "oauth.v2.access", Code: code}
	}
	token := out.AuthedUser.AccessToken
	if token == "" {
		token = out.AccessToken
	}
	if err := requireUserToken(token); err != nil {
		return Credentials{}, err
	}
	team := out.Team.Name
	if team == "" {
		team = out.Team.ID
	}
	return Credentials{
		AccessToken:  token,
		RefreshToken: out.RefreshToken,
		UserID:       out.AuthedUser.ID,
		Team:         team,
	}, nil
}

// CompleteLogin listens on the loopback redirect until the member approves,
// then exchanges the code. open receives the consent URL after the listener
// is bound. The user token stays in the returned credentials.
func CompleteLogin(ctx context.Context, clientID string, open func(string) error) (Credentials, error) {
	clientID = strings.TrimSpace(clientID)
	if err := requireClientID(clientID); err != nil {
		return Credentials{}, err
	}
	verifier, err := newVerifier()
	if err != nil {
		return Credentials{}, err
	}
	state, err := newVerifier()
	if err != nil {
		return Credentials{}, err
	}
	ln, err := listenLoopback(ListenAddr)
	if err != nil {
		return Credentials{}, err
	}
	redirect := redirectFor(ln)
	code, err := waitForCode(ctx, ln, state, AuthorizeURL(clientID, redirect, state, CodeChallenge(verifier)), open)
	if err != nil {
		return Credentials{}, err
	}
	creds, err := Exchange(ctx, clientID, code, verifier, redirect)
	if err != nil {
		return Credentials{}, err
	}
	creds.ClientID = clientID
	return creds, nil
}

func redirectFor(ln net.Listener) string {
	if RedirectURL != "" {
		return RedirectURL
	}
	return "http://" + ln.Addr().String() + "/slack/callback"
}

func listenLoopback(addr string) (net.Listener, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("slack oauth listen: %w", err)
	}
	host, _, err := net.SplitHostPort(ln.Addr().String())
	if err != nil || (host != "127.0.0.1" && host != "::1") {
		if closeErr := ln.Close(); closeErr != nil {
			return nil, fmt.Errorf("slack oauth must listen on loopback: %w", closeErr)
		}
		return nil, fmt.Errorf("slack oauth must listen on loopback")
	}
	return ln, nil
}

func waitForCode(ctx context.Context, ln net.Listener, state, authURL string, open func(string) error) (string, error) {
	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	srv := &http.Server{
		Handler:           callbackHandler(state, codeCh, errCh),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	if open != nil {
		if err := open(authURL); err != nil {
			return "", err
		}
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-errCh:
		return "", err
	case code := <-codeCh:
		return code, nil
	}
}

func reportOAuth(errCh chan error, err error) {
	select {
	case errCh <- err:
	default:
	}
}

func callbackHandler(state string, codeCh chan string, errCh chan error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/slack/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		if msg := q.Get("error"); msg != "" {
			reportOAuth(errCh, fmt.Errorf("slack oauth: %s", msg))
			http.Error(w, "Slack did not approve Aiman.", http.StatusBadRequest)
			return
		}
		if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			reportOAuth(errCh, fmt.Errorf("slack oauth: state mismatch"))
			http.Error(w, "Slack login state did not match.", http.StatusBadRequest)
			return
		}
		code := q.Get("code")
		if code == "" {
			reportOAuth(errCh, fmt.Errorf("slack oauth: missing code"))
			http.Error(w, "Slack login did not return a code.", http.StatusBadRequest)
			return
		}
		select {
		case codeCh <- code:
		default:
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, "<p>Aiman is approved. You can close this tab.</p>")
	})
}
