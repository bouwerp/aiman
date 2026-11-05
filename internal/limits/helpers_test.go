package limits

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)

func testEnv(t *testing.T) Env {
	t.Helper()
	return Env{
		Home:   t.TempDir(),
		Now:    func() time.Time { return testNow },
		Getenv: func(string) string { return "" },
		Run: func(context.Context, string, ...string) (string, int, error) {
			t.Fatal("unexpected command")
			return "", 0, nil
		},
		Transport: refuseTransport{t},
	}
}

type refuseTransport struct{ t *testing.T }

func (r refuseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r.t.Errorf("unexpected request to %s", req.URL.Host)
	return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("")), Request: req, Header: http.Header{}}, nil
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
