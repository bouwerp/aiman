package awslogin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
)

type nopWriter struct{ calls atomic.Int32 }

func (w *nopWriter) WriteHostCredentials(string, *awsdelegation.SessionCredentials) error {
	w.calls.Add(1)
	return nil
}

// fakeOIDC answers register and device authorization; token is the test's.
func fakeOIDC(t *testing.T, registers *atomic.Int32, token http.HandlerFunc) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) {
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/client/register", func(w http.ResponseWriter, _ *http.Request) {
		registers.Add(1)
		writeJSON(w, map[string]any{"clientId": "id", "clientSecret": "secret", "clientSecretExpiresAt": time.Now().Add(90 * 24 * time.Hour).Unix()})
	})
	mux.HandleFunc("/device_authorization", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"deviceCode": "dc", "userCode": "U", "verificationUriComplete": "https://x/?u=U", "expiresIn": 600, "interval": 1})
	})
	mux.HandleFunc("/token", token)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func immediate(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Now()
	return ch
}

func newTestManager(t *testing.T, srv *httptest.Server, tweak func(*Options)) (*Manager, string) {
	t.Helper()
	home := t.TempDir()
	base := func(string) string { return srv.URL }
	o := Options{Home: home, Endpoints: Endpoints{OIDC: base, Portal: base}, Writer: &nopWriter{}, After: immediate, HTTP: srv.Client()}
	if tweak != nil {
		tweak(&o)
	}
	m := New(o)
	t.Cleanup(m.Close)
	return m, home
}

func testDelegation() Delegation {
	return Delegation{Profile: "default", StartURL: "https://s/start", SSORegion: "eu-west-1", AccountID: "1"}
}

func waitEnded(t *testing.T, m *Manager, id string) Login {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if l, ok := m.Status(id); ok && ended(l.State) {
			return l
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("login %s never ended", id)
	return Login{}
}

func tokenAnswer(w http.ResponseWriter, _ *http.Request) {
	_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "tok"})
}

func TestErrorCode(t *testing.T) {
	cases := []struct {
		name, header, body, want string
	}{
		{"header with namespace url", "SlowDownException:http://internal.amazon.com/coral/x/", "", codeSlowDown},
		{"header wins over body", "AccessDeniedException", `{"__type":"Other"}`, codeAccessDenied},
		{"__type with shape prefix", "", `{"__type":"com.amazon#ExpiredTokenException"}`, codeExpiredToken},
		{"oauth error", "", `{"error":"authorization_pending"}`, codePending},
		{"body not json", "", "<html>", codeUnknown},
		{"secret-looking value refused", "", `{"__type":"PLANTED SECRET value"}`, codeUnknown},
		{"oversized name refused", "", `{"error":"` + string(make([]byte, 80)) + `"}`, codeUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := http.Header{}
			if c.header != "" {
				h.Set("x-amzn-ErrorType", c.header)
			}
			if got := errorCode(h, []byte(c.body)); got != c.want {
				t.Fatalf("errorCode = %q, want %q", got, c.want)
			}
		})
	}
}

func TestStartRefusesAMalformedRegion(t *testing.T) {
	m := New(Options{Home: t.TempDir(), Writer: &nopWriter{}})
	defer m.Close()
	d := testDelegation()
	d.SSORegion = "evil.example.com/x"
	if _, err := m.Start(t.Context(), d); !errors.Is(err, ErrNoSSO) {
		t.Fatalf("err = %v, want ErrNoSSO", err)
	}
}

func TestPortalRedirectKeepsTheBearerToken(t *testing.T) {
	var followed atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed.Add(1) }))
	defer other.Close()
	var registers atomic.Int32
	srv := fakeOIDC(t, &registers, tokenAnswer)
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer portal.Close()
	writer := &nopWriter{}
	m, _ := newTestManager(t, srv, func(o *Options) {
		o.Writer = writer
		o.Endpoints.Portal = func(string) string { return portal.URL }
	})
	l, err := m.Start(t.Context(), testDelegation())
	if err != nil {
		t.Fatal(err)
	}
	end := waitEnded(t, m, l.ID)
	if end.State != StateFailed || end.Failure != FailureOther {
		t.Fatalf("end = %s/%s, want failed/other", end.State, end.Failure)
	}
	if followed.Load() != 0 {
		t.Fatal("the redirect was followed, which would carry the bearer token")
	}
	if writer.calls.Load() != 0 {
		t.Fatal("writer called")
	}
}

func TestCloseEndsARunningLogin(t *testing.T) {
	var registers atomic.Int32
	srv := fakeOIDC(t, &registers, tokenAnswer)
	never := func(time.Duration) <-chan time.Time { return make(chan time.Time) }
	m, _ := newTestManager(t, srv, func(o *Options) { o.After = never })
	l, err := m.Start(t.Context(), testDelegation())
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	got, ok := m.Status(l.ID)
	if !ok || got.State != StateFailed || got.Failure != FailureOther {
		t.Fatalf("after Close = %+v, %v; want failed/other", got, ok)
	}
	if _, err := m.Start(t.Context(), testDelegation()); err == nil {
		t.Fatal("Start after Close succeeded")
	}
}

func TestInvalidClientDuringPollDropsTheRegistration(t *testing.T) {
	var registers atomic.Int32
	var invalid atomic.Bool
	invalid.Store(true)
	srv := fakeOIDC(t, &registers, func(w http.ResponseWriter, r *http.Request) {
		if invalid.Load() {
			w.Header().Set("x-amzn-ErrorType", codeInvalidClient)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		tokenAnswer(w, r)
	})
	portalOK := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"roleCredentials": map[string]any{"accessKeyId": "a", "secretAccessKey": "b", "sessionToken": "c", "expiration": 1}})
	}))
	defer portalOK.Close()
	m, _ := newTestManager(t, srv, func(o *Options) { o.Endpoints.Portal = func(string) string { return portalOK.URL } })

	l, err := m.Start(t.Context(), testDelegation())
	if err != nil {
		t.Fatal(err)
	}
	if end := waitEnded(t, m, l.ID); end.State != StateFailed || end.Failure != FailureOther {
		t.Fatalf("end = %s/%s, want failed/other", end.State, end.Failure)
	}
	invalid.Store(false)
	l, err = m.Start(t.Context(), testDelegation())
	if err != nil {
		t.Fatal(err)
	}
	if end := waitEnded(t, m, l.ID); end.State != StateDone {
		t.Fatalf("second login = %s/%s, want done", end.State, end.Failure)
	}
	if n := registers.Load(); n != 2 {
		t.Fatalf("RegisterClient called %d times, want 2", n)
	}
}

func TestCorruptRegistrationFileIsReplacedWithMode0600(t *testing.T) {
	var registers atomic.Int32
	srv := fakeOIDC(t, &registers, tokenAnswer)
	m, home := newTestManager(t, srv, func(o *Options) {
		o.After = func(time.Duration) <-chan time.Time { return make(chan time.Time) }
	})
	path := filepath.Join(home, ".aiman", "aws-sso-client.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start(t.Context(), testDelegation()); err != nil {
		t.Fatal(err)
	}
	if n := registers.Load(); n != 1 {
		t.Fatalf("RegisterClient called %d times, want 1", n)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", st.Mode().Perm())
	}
	if got := readRegistrations(path); len(got.Clients) != 1 || got.Clients[0].ClientID != "id" {
		t.Fatalf("file = %+v", got)
	}
}
