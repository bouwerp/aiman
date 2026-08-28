package gateway

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestAuthorizeRequiresBearer(t *testing.T) {
	a := Auth{Token: "secret", Funnel: true}
	r := httptestRequest(t, "")
	if err := a.authorize(r); !errors.Is(err, errUnauthorized) {
		t.Fatalf("got %v", err)
	}
	r.Header.Set("Authorization", "Bearer secret")
	if err := a.authorize(r); err != nil {
		t.Fatalf("funnel+token: %v", err)
	}
}

func TestAuthorizeWhoIsRequiredOnTailnet(t *testing.T) {
	a := Auth{Token: "secret"}
	r := httptestRequest(t, "secret")
	if err := a.authorize(r); !errors.Is(err, errForbidden) {
		t.Fatalf("no WhoIs: %v", err)
	}
	a.WhoIs = func(context.Context, string) (string, error) {
		return "me@example.com", nil
	}
	if err := a.authorize(r); err != nil {
		t.Fatalf("any login: %v", err)
	}
	a.Allow = []string{"other@example.com"}
	if err := a.authorize(r); !errors.Is(err, errForbidden) {
		t.Fatalf("allow miss: %v", err)
	}
	a.Allow = []string{"me@example.com"}
	if err := a.authorize(r); err != nil {
		t.Fatalf("allow hit: %v", err)
	}
}

func TestAuthorizeWhoIsErrorIsForbidden(t *testing.T) {
	a := Auth{
		Token: "secret",
		WhoIs: func(context.Context, string) (string, error) {
			return "", errors.New("no node")
		},
	}
	if err := a.authorize(httptestRequest(t, "secret")); !errors.Is(err, errForbidden) {
		t.Fatalf("got %v", err)
	}
}

func httptestRequest(t *testing.T, token string) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodGet, "http://gw/v1/health", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.RemoteAddr = "100.64.0.2:12345"
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}
