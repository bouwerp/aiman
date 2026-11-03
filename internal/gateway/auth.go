package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

var (
	errUnauthorized = errors.New("unauthorized")
	errForbidden    = errors.New("forbidden")
)

// WhoIs resolves a tailnet connection's login. Funnel traffic has no identity,
// so this is nil when the gateway is listening publicly.
type WhoIs func(ctx context.Context, remoteAddr string) (login string, err error)

// Auth checks the bearer token (the host token or a paired device's token)
// and, off Funnel, the tailnet identity.
type Auth struct {
	Token   string
	Devices *DeviceStore
	WhoIs   WhoIs
	Allow   []string
	Funnel  bool
}

// principal is who a request authenticated as: the host token, or a device.
type principal struct {
	host   bool
	device Device
}

type principalKey struct{}

func withPrincipal(ctx context.Context, p principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

func principalFrom(ctx context.Context) principal {
	p, _ := ctx.Value(principalKey{}).(principal)
	return p
}

// authorize checks token, then the tailnet identity.
func (a Auth) authorize(r *http.Request, token string) (principal, error) {
	p, ok := a.match(token)
	if !ok {
		return principal{}, errUnauthorized
	}
	if a.Funnel {
		return p, nil
	}
	if _, err := a.tailnetLogin(r); err != nil {
		return principal{}, err
	}
	return p, nil
}

func (a Auth) match(token string) (principal, bool) {
	if token == "" {
		return principal{}, false
	}
	if TokenEqual(token, a.Token) {
		return principal{host: true}, true
	}
	if a.Devices == nil {
		return principal{}, false
	}
	dev, ok := a.Devices.Authenticate(token)
	if !ok {
		return principal{}, false
	}
	return principal{device: dev}, true
}

// tailnetLogin returns the WhoIs login when it passes allow_logins.
func (a Auth) tailnetLogin(r *http.Request) (string, error) {
	if a.WhoIs == nil {
		return "", errForbidden
	}
	login, err := a.WhoIs(r.Context(), r.RemoteAddr)
	if err != nil {
		return "", errForbidden
	}
	login = strings.TrimSpace(login)
	if login == "" {
		return "", errForbidden
	}
	if len(a.Allow) == 0 {
		return login, nil
	}
	for _, want := range a.Allow {
		if strings.EqualFold(strings.TrimSpace(want), login) {
			return login, nil
		}
	}
	return "", errForbidden
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}

// requestToken is the bearer token, or for a WebSocket route with no header,
// the access_token query parameter (browsers and React Native cannot set
// headers on a WebSocket).
func requestToken(r *http.Request, allowQuery bool) string {
	if tok := bearerToken(r.Header.Get("Authorization")); tok != "" {
		return tok
	}
	if allowQuery {
		return r.URL.Query().Get("access_token")
	}
	return ""
}
