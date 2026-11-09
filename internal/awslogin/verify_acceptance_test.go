package awslogin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bouwerp/aiman/internal/awslogin"
	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
)

// P3 (docs/specs/aws-permission-set-apply.md, rev 8): VerifyStartURL runs
// RegisterClient and StartDeviceAuthorization in process and reports whether
// the regional OIDC endpoint accepts the URL as an access portal.

type oidcCall struct {
	path string
	form map[string]string
}

// fakeOIDC answers the two calls. register and device are programmed answers;
// a nil answer is success.
type fakeOIDC struct {
	mu       sync.Mutex
	srv      *httptest.Server
	calls    []oidcCall
	register *oidcAnswer
	device   *oidcAnswer
}

type oidcAnswer struct {
	status  int
	errType string // x-amzn-ErrorType header, when set
	body    map[string]any
}

func newFakeOIDC(t *testing.T) *fakeOIDC {
	t.Helper()
	f := &fakeOIDC{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeOIDC) serve(w http.ResponseWriter, r *http.Request) {
	form := map[string]string{}
	var m map[string]any
	_ = json.NewDecoder(r.Body).Decode(&m)
	for k, v := range m {
		if s, ok := v.(string); ok {
			form[k] = s
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, oidcCall{path: r.URL.Path, form: form})
	var ans *oidcAnswer
	switch r.URL.Path {
	case "/client/register":
		ans = f.register
	case "/device_authorization":
		ans = f.device
	}
	f.mu.Unlock()
	if ans != nil {
		if ans.errType != "" {
			w.Header().Set("x-amzn-ErrorType", ans.errType)
		}
		w.WriteHeader(ans.status)
		_ = json.NewEncoder(w).Encode(ans.body)
		return
	}
	switch r.URL.Path {
	case "/client/register":
		_ = json.NewEncoder(w).Encode(map[string]any{"clientId": "client-1", "clientSecret": plantClientSecret, "clientSecretExpiresAt": 1900000000})
	case "/device_authorization":
		_ = json.NewEncoder(w).Encode(map[string]any{"deviceCode": plantDeviceCode, "userCode": testUserCode, "verificationUri": "https://device.sso.eu-west-1.amazonaws.com/", "verificationUriComplete": "https://device.sso.eu-west-1.amazonaws.com/?user_code=" + testUserCode, "expiresIn": 600, "interval": 1})
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeOIDC) verifier(t *testing.T) (awslogin.Verifier, *[]string) {
	t.Helper()
	regions := &[]string{}
	v := awslogin.Verifier{
		HTTP: f.srv.Client(),
		OIDC: func(region string) string {
			*regions = append(*regions, region)
			return f.srv.URL
		},
	}
	return v, regions
}

func TestVerifyStartURLAcceptsAPortal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	f := newFakeOIDC(t)
	v, regions := f.verifier(t)
	if err := v.VerifyStartURL(context.Background(), testRegion, testStartURL); err != nil {
		t.Fatalf("VerifyStartURL: %v", err)
	}
	if len(f.calls) != 2 || f.calls[0].path != "/client/register" || f.calls[1].path != "/device_authorization" {
		t.Fatalf("calls = %+v, want RegisterClient then StartDeviceAuthorization", f.calls)
	}
	reg := f.calls[0].form
	if reg["clientName"] != "aiman-permission-set-apply" || reg["clientType"] != "public" {
		t.Errorf("RegisterClient form = %v, want clientName aiman-permission-set-apply and clientType public", reg)
	}
	dev := f.calls[1].form
	if dev["clientId"] != "client-1" || dev["clientSecret"] != plantClientSecret || dev["startUrl"] != testStartURL {
		t.Errorf("StartDeviceAuthorization form = %v, want the registration and the start URL", dev)
	}
	for _, r := range *regions {
		if r != testRegion {
			t.Errorf("OIDC endpoint asked for region %q, want %q", r, testRegion)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".aiman", "aws-sso-client.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the registration file was written (err %v); verification must keep the client secret in memory only", err)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Errorf("verification wrote under HOME: %v", entries)
	}
}

func TestVerifyStartURLRejection(t *testing.T) {
	t.Run("header error type", func(t *testing.T) {
		f := newFakeOIDC(t)
		f.device = &oidcAnswer{status: http.StatusBadRequest, errType: "InvalidRequestException", body: map[string]any{"error": "invalid_request", "error_description": "Invalid start URL"}}
		v, _ := f.verifier(t)
		err := v.VerifyStartURL(context.Background(), testRegion, "https://wrong.awsapps.com/start")
		if !errors.Is(err, awslogin.ErrStartURLRejected) {
			t.Fatalf("err = %v, want ErrStartURLRejected", err)
		}
		if !errors.Is(err, awsdelegation.ErrStartURLRejected) {
			t.Errorf("awslogin.ErrStartURLRejected must be awsdelegation.ErrStartURLRejected, so Apply can match it without importing awslogin")
		}
	})
	t.Run("body only", func(t *testing.T) {
		f := newFakeOIDC(t)
		f.device = &oidcAnswer{status: http.StatusBadRequest, body: map[string]any{"error": "invalid_request"}}
		v, _ := f.verifier(t)
		if err := v.VerifyStartURL(context.Background(), testRegion, "https://wrong.awsapps.com/start"); !errors.Is(err, awslogin.ErrStartURLRejected) {
			t.Fatalf("err = %v, want ErrStartURLRejected from the OAuth body code", err)
		}
	})
	t.Run("register-client errors are not a rejection", func(t *testing.T) {
		f := newFakeOIDC(t)
		f.register = &oidcAnswer{status: http.StatusBadRequest, errType: "InvalidRequestException", body: map[string]any{"error": "invalid_request"}}
		v, _ := f.verifier(t)
		err := v.VerifyStartURL(context.Background(), testRegion, testStartURL)
		if err == nil || errors.Is(err, awslogin.ErrStartURLRejected) {
			t.Fatalf("err = %v, want a failure that is not ErrStartURLRejected (RegisterClient takes no start URL)", err)
		}
		if len(f.calls) != 1 {
			t.Errorf("calls = %+v, want StartDeviceAuthorization skipped after a failed registration", f.calls)
		}
	})
	t.Run("other failures are not a rejection and leak nothing", func(t *testing.T) {
		f := newFakeOIDC(t)
		f.device = &oidcAnswer{status: http.StatusInternalServerError, errType: "InternalServerException", body: map[string]any{"error": "server_error", "secret": plantClientSecret}}
		v, _ := f.verifier(t)
		err := v.VerifyStartURL(context.Background(), testRegion, testStartURL)
		if err == nil || errors.Is(err, awslogin.ErrStartURLRejected) {
			t.Fatalf("err = %v, want a non-rejection failure", err)
		}
		if strings.Contains(err.Error(), plantClientSecret) || strings.Contains(err.Error(), plantDeviceCode) {
			t.Errorf("err = %v leaks a planted secret", err)
		}
	})
}

func TestVerifyStartURLRefusesABadRegionWithoutACall(t *testing.T) {
	f := newFakeOIDC(t)
	v, _ := f.verifier(t)
	err := v.VerifyStartURL(context.Background(), "evil.example.com/", testStartURL)
	if err == nil || errors.Is(err, awslogin.ErrStartURLRejected) {
		t.Fatalf("err = %v, want a refusal that is not a rejection", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("calls = %+v, want none for a region that is not a region name", f.calls)
	}
}

func TestVerifyStartURLDefaultsBuildTheRegionalHost(t *testing.T) {
	// The package-level function is Verifier{}.VerifyStartURL; a context that
	// is already cancelled must fail without a rejection and without hanging.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := awslogin.VerifyStartURL(ctx, testRegion, testStartURL)
	if err == nil || errors.Is(err, awslogin.ErrStartURLRejected) {
		t.Fatalf("err = %v, want a transport failure under a cancelled context", err)
	}
}
