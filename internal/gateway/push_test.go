package gateway

import (
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestPushRegisterDoesNotHitTheSocket(t *testing.T) {
	called := false
	sock := startEchoUnix(t, func(net.Conn, []byte) { called = true })
	store := mustStore(t)
	h := (&Server{
		Socket: sock,
		Auth:   Auth{Token: "secret", Funnel: true},
		Push:   store,
	}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/rpc", strings.NewReader(
		`{"method":"push.register","params":{"token":"ExponentPushToken[aaa]","device_id":"phone-1"}}`,
	))
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}
	if called {
		t.Fatal("push.register must not be forwarded to serve")
	}
	if !strings.Contains(rec.Body.String(), `"push_registered"`) {
		t.Fatalf("body %s", rec.Body.String())
	}
	devs := store.Devices()
	if len(devs) != 1 || devs[0].DeviceID != "phone-1" {
		t.Fatalf("store %+v", devs)
	}
	if got := devs[0].States; len(got) != 1 || got[0] != "waiting_input" {
		t.Fatalf("default states %v", got)
	}
}

func TestPushRegisterRequiresToken(t *testing.T) {
	h := (&Server{Auth: Auth{Token: "secret", Funnel: true}, Push: mustStore(t)}).Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/rpc", strings.NewReader(`{"method":"push.register","params":{}}`))
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid_params") {
		t.Fatalf("body %s", rec.Body.String())
	}
}

func TestPushUnregisterByDeviceAndToken(t *testing.T) {
	store := mustStore(t)
	if err := store.Register(PushDevice{Token: "ExponentPushToken[a]", DeviceID: "d1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Register(PushDevice{Token: "ExponentPushToken[b]", DeviceID: "d2"}); err != nil {
		t.Fatal(err)
	}
	h := (&Server{Auth: Auth{Token: "secret", Funnel: true}, Push: store}).Handler()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/rpc", strings.NewReader(`{"method":"push.unregister","params":{"device_id":"d1"}}`))
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "push_unregistered") {
		t.Fatalf("code %d body %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/rpc", strings.NewReader(`{"method":"push.unregister","params":{"token":"ExponentPushToken[b]"}}`))
	req.Header.Set("Authorization", "Bearer secret")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if n := len(store.Devices()); n != 0 {
		t.Fatalf("left %d devices", n)
	}
}

func TestPushStoreSurvivesReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway-push.json")
	store, err := OpenPushStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Register(PushDevice{
		Token:    "ExponentPushToken[zzz]",
		DeviceID: "dev",
		States:   []string{"waiting_input", "exited"},
	}); err != nil {
		t.Fatal(err)
	}
	again, err := OpenPushStore(path)
	if err != nil {
		t.Fatal(err)
	}
	devs := again.Devices()
	if len(devs) != 1 || devs[0].DeviceID != "dev" || devs[0].Token != "ExponentPushToken[zzz]" {
		t.Fatalf("%+v", devs)
	}
}

func mustStore(t *testing.T) *PushStore {
	t.Helper()
	s, err := OpenPushStore(filepath.Join(t.TempDir(), "gateway-push.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}
