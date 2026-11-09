package awslogin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestVerifyStartURLBodyCodeBeatsOtherHeaderType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/client/register" {
			_, _ = w.Write([]byte(`{"clientId":"c","clientSecret":"s"}`))
			return
		}
		w.Header().Set("x-amzn-ErrorType", "ValidationException")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_request"}`))
	}))
	defer srv.Close()
	v := Verifier{HTTP: srv.Client(), OIDC: func(string) string { return srv.URL }}
	if err := v.VerifyStartURL(context.Background(), "eu-west-1", "https://x.awsapps.com/start"); !errors.Is(err, ErrStartURLRejected) {
		t.Fatalf("err = %v, want ErrStartURLRejected from the body code", err)
	}
}
