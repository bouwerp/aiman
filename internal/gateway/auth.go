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

// Auth is the bearer-token plus optional WhoIs check.
type Auth struct {
	Token  string
	WhoIs  WhoIs
	Allow  []string
	Funnel bool
}

func (a Auth) authorize(r *http.Request) error {
	got := bearerToken(r.Header.Get("Authorization"))
	if !TokenEqual(got, a.Token) {
		return errUnauthorized
	}
	if a.Funnel {
		return nil
	}
	if a.WhoIs == nil {
		return errForbidden
	}
	login, err := a.WhoIs(r.Context(), r.RemoteAddr)
	if err != nil {
		return errForbidden
	}
	login = strings.TrimSpace(login)
	if login == "" {
		return errForbidden
	}
	if len(a.Allow) == 0 {
		return nil
	}
	for _, want := range a.Allow {
		if strings.EqualFold(strings.TrimSpace(want), login) {
			return nil
		}
	}
	return errForbidden
}

func bearerToken(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix))
}
