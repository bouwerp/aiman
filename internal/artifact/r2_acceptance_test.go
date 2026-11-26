package artifact

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/harness/awssig"
)

// Acceptance tests for docs/specs/artifacts.md piece A (R2 client). Do not edit.

func TestR2SignsPathStyleAndConditions(t *testing.T) {
	type seen struct{ method, path, auth, sha, inm, im, ct, body string }
	var reqs []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		reqs = append(reqs, seen{r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"), r.Header.Get("X-Amz-Content-Sha256"),
			r.Header.Get("If-None-Match"), r.Header.Get("If-Match"), r.Header.Get("Content-Type"), string(body)})
		switch {
		case r.Method == http.MethodPut && r.Header.Get("If-Match") == `"stale"`:
			w.WriteHeader(http.StatusPreconditionFailed)
		case r.Method == http.MethodPut:
			w.Header().Set("ETag", `"abc"`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/missing"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet:
			w.Header().Set("ETag", `"m1"`)
			io.WriteString(w, `{"latest":1}`)
		}
	}))
	defer srv.Close()
	r2 := NewR2(srv.URL, "aiman-artifacts", awssig.Credentials{AccessKeyID: "KEY", SecretAccessKey: "SECRET"}, srv.Client())
	ctx := context.Background()

	etag, err := r2.Put(ctx, "a/x/v/1/index.html", []byte("<p>"), "text/html; charset=utf-8", Cond{IfNoneMatch: true})
	if err != nil || etag != `"abc"` {
		t.Fatalf("put %q %v", etag, err)
	}
	p := reqs[0]
	if p.method != http.MethodPut || p.path != "/aiman-artifacts/a/x/v/1/index.html" || p.body != "<p>" || p.inm != "*" || p.ct != "text/html; charset=utf-8" {
		t.Fatalf("put request %+v", p)
	}
	if !strings.HasPrefix(p.auth, "AWS4-HMAC-SHA256 Credential=KEY/") || !strings.Contains(p.auth, "/auto/s3/aws4_request") {
		t.Fatalf("authorization %q", p.auth)
	}
	if p.sha != awssig.PayloadHash([]byte("<p>")) {
		t.Fatalf("x-amz-content-sha256 %q", p.sha)
	}
	if _, err := r2.Put(ctx, "a/x/meta.json", []byte("{}"), "application/json", Cond{IfMatch: `"stale"`}); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("412: %v", err)
	}
	if reqs[1].im != `"stale"` {
		t.Fatalf("if-match %q", reqs[1].im)
	}
	data, etag, err := r2.Get(ctx, "a/x/meta.json")
	if err != nil || string(data) != `{"latest":1}` || etag != `"m1"` {
		t.Fatalf("get %q %q %v", data, etag, err)
	}
	if _, _, err := r2.Get(ctx, "a/x/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("404: %v", err)
	}
}
