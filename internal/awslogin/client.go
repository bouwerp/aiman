package awslogin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
)

const (
	clientName  = "aiman-serve"
	clientType  = "public"
	deviceGrant = "urn:ietf:params:oauth:grant-type:device_code"

	maxResponseBytes = 1 << 20

	codeTransport = "transport"
	codeParse     = "parse"
	codeUnknown   = "unknown"

	codePending       = "AuthorizationPendingException"
	codeSlowDown      = "SlowDownException"
	codeAccessDenied  = "AccessDeniedException"
	codeExpiredToken  = "ExpiredTokenException"
	codeInvalidClient = "InvalidClientException"
	codeInvalidReq    = "InvalidRequestException"
)

// oauthCodes maps the OAuth error field Identity Center sends in OIDC error
// bodies to its exception names.
var oauthCodes = map[string]string{
	"authorization_pending": codePending,
	"slow_down":             codeSlowDown,
	"access_denied":         codeAccessDenied,
	"expired_token":         codeExpiredToken,
	"invalid_client":        codeInvalidClient,
	"invalid_request":       codeInvalidReq,
}

// safeCode rejects anything that does not look like an error name, because the
// code reaches logs and error strings and comes from a response.
var safeCode = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)

// apiError is a failed call: the operation, status and error code only. Response
// bodies are parsed for the code and discarded, and transport errors are not
// wrapped, so its text cannot carry a secret.
type apiError struct {
	op     string
	status int
	code   string
	// oauth is the body's OAuth error code when the header named another
	// type; normalized like code.
	oauth string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("awslogin: %s: %s (HTTP %d)", e.op, e.code, e.status)
}

func apiCode(err error) string {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.code
	}
	return ""
}

func apiStatus(err error) int {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae.status
	}
	return 0
}

// noRedirectClient copies base (or a default with a 30s timeout) and stops it
// following redirects: Go forwards a custom header like the SSO bearer token to
// any redirect target.
func noRedirectClient(base *http.Client) *http.Client {
	c := http.Client{Timeout: httpTimeout}
	if base != nil {
		c = *base
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

func (m *Manager) oidcURL(region, path string) string {
	if m.o.Endpoints.OIDC != nil {
		return m.o.Endpoints.OIDC(region) + path
	}
	return "https://oidc." + region + ".amazonaws.com" + path
}

func (m *Manager) portalURL(region, path string) string {
	if m.o.Endpoints.Portal != nil {
		return m.o.Endpoints.Portal(region) + path
	}
	return "https://portal.sso." + region + ".amazonaws.com" + path
}

func (m *Manager) postJSON(ctx context.Context, op, target string, in, out any) error {
	return postJSON(ctx, m.http, op, target, in, out)
}

func (m *Manager) do(req *http.Request, op string, out any) error {
	return doJSON(m.http, req, op, out)
}

// postJSON posts in as JSON to target with client and decodes the answer into out.
func postJSON(ctx context.Context, client *http.Client, op, target string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return &apiError{op: op, code: codeParse}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return &apiError{op: op, code: codeTransport}
	}
	req.Header.Set("Content-Type", "application/json")
	return doJSON(client, req, op, out)
}

// doJSON sends req and decodes a 2xx JSON answer into out. Any failure is an
// apiError carrying the operation, status and error code only.
func doJSON(client *http.Client, req *http.Request, op string, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return &apiError{op: op, code: codeTransport}
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return &apiError{op: op, status: resp.StatusCode, code: codeTransport}
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &apiError{op: op, status: resp.StatusCode, code: errorCode(resp.Header, body), oauth: oauthErrorCode(body)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &apiError{op: op, status: resp.StatusCode, code: codeParse}
	}
	return nil
}

// oauthErrorCode is the body's OAuth error field mapped like errorCode, or
// empty when the body has none.
func oauthErrorCode(body []byte) string {
	var parsed struct {
		OAuth string `json:"error"`
	}
	if json.Unmarshal(body, &parsed) != nil || parsed.OAuth == "" {
		return ""
	}
	return normalizeCode(parsed.OAuth)
}

// errorCode reads the error name from x-amzn-ErrorType, then the body's __type,
// then its OAuth error.
func errorCode(h http.Header, body []byte) string {
	var parsed struct {
		Type  string `json:"__type"`
		OAuth string `json:"error"`
	}
	_ = json.Unmarshal(body, &parsed) // a body that is not JSON just has no code
	for _, raw := range []string{h.Get("x-amzn-ErrorType"), parsed.Type, parsed.OAuth} {
		if raw != "" {
			return normalizeCode(raw)
		}
	}
	return codeUnknown
}

// normalizeCode strips the forms AWS decorates a name with:
// "Name:http://..." and "namespace#Name".
func normalizeCode(raw string) string {
	code, _, _ := strings.Cut(raw, ":")
	if i := strings.LastIndexByte(code, '#'); i >= 0 {
		code = code[i+1:]
	}
	if mapped, ok := oauthCodes[code]; ok {
		return mapped
	}
	if !safeCode.MatchString(code) {
		return codeUnknown
	}
	return code
}

func (m *Manager) registerClient(ctx context.Context, d Delegation) (registration, error) {
	const op = "RegisterClient"
	var out struct {
		ClientID     string `json:"clientId"`
		ClientSecret string `json:"clientSecret"`
		ExpiresAt    int64  `json:"clientSecretExpiresAt"`
	}
	in := map[string]string{"clientName": clientName, "clientType": clientType}
	if err := m.postJSON(ctx, op, m.oidcURL(d.SSORegion, "/client/register"), in, &out); err != nil {
		return registration{}, err
	}
	if out.ClientID == "" || out.ClientSecret == "" {
		return registration{}, &apiError{op: op, status: http.StatusOK, code: codeParse}
	}
	return registration{
		ClientID:     out.ClientID,
		ClientSecret: out.ClientSecret,
		ExpiresAt:    time.Unix(out.ExpiresAt, 0).UTC(),
		Region:       d.SSORegion,
		StartURL:     d.StartURL,
	}, nil
}

type deviceAuthorization struct {
	DeviceCode              string `json:"deviceCode"`
	UserCode                string `json:"userCode"`
	VerificationURIComplete string `json:"verificationUriComplete"`
	ExpiresIn               int    `json:"expiresIn"`
	Interval                int    `json:"interval"`
}

func (m *Manager) startDeviceAuthorization(ctx context.Context, d Delegation, reg registration) (deviceAuthorization, error) {
	const op = "StartDeviceAuthorization"
	var out deviceAuthorization
	in := map[string]string{"clientId": reg.ClientID, "clientSecret": reg.ClientSecret, "startUrl": d.StartURL}
	if err := m.postJSON(ctx, op, m.oidcURL(d.SSORegion, "/device_authorization"), in, &out); err != nil {
		return deviceAuthorization{}, err
	}
	if out.DeviceCode == "" || out.UserCode == "" || out.VerificationURIComplete == "" {
		return deviceAuthorization{}, &apiError{op: op, status: http.StatusOK, code: codeParse}
	}
	return out, nil
}

// createToken decodes only the access token, so a refresh token in the response
// is never held.
func (m *Manager) createToken(ctx context.Context, d Delegation, reg registration, deviceCode string) (string, error) {
	const op = "CreateToken"
	var out struct {
		AccessToken string `json:"accessToken"`
	}
	in := map[string]string{"clientId": reg.ClientID, "clientSecret": reg.ClientSecret, "grantType": deviceGrant, "deviceCode": deviceCode}
	if err := m.postJSON(ctx, op, m.oidcURL(d.SSORegion, "/token"), in, &out); err != nil {
		return "", err
	}
	if out.AccessToken == "" {
		return "", &apiError{op: op, status: http.StatusOK, code: codeParse}
	}
	return out.AccessToken, nil
}

func (m *Manager) getRoleCredentials(ctx context.Context, d Delegation, token string) (*awsdelegation.SessionCredentials, error) {
	const op = "GetRoleCredentials"
	q := url.Values{"account_id": {d.AccountID}, "role_name": {d.RoleName}}
	target := m.portalURL(d.SSORegion, "/federation/credentials") + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, &apiError{op: op, code: codeTransport}
	}
	req.Header.Set("x-amz-sso_bearer_token", token)
	var out struct {
		RoleCredentials struct {
			AccessKeyID     string `json:"accessKeyId"`
			SecretAccessKey string `json:"secretAccessKey"`
			SessionToken    string `json:"sessionToken"`
			Expiration      int64  `json:"expiration"`
		} `json:"roleCredentials"`
	}
	if err := m.do(req, op, &out); err != nil {
		return nil, err
	}
	rc := out.RoleCredentials
	if rc.AccessKeyID == "" || rc.SecretAccessKey == "" || rc.SessionToken == "" || rc.Expiration <= 0 {
		return nil, &apiError{op: op, status: http.StatusOK, code: codeParse}
	}
	return &awsdelegation.SessionCredentials{
		AccessKeyID:     rc.AccessKeyID,
		SecretAccessKey: rc.SecretAccessKey,
		SessionToken:    rc.SessionToken,
		Expiration:      time.UnixMilli(rc.Expiration).UTC(),
	}, nil
}
