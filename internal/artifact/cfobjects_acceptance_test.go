package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Acceptance tests for docs/specs/artifacts.md piece A2 (cf OAuth, REST objects). Do not edit.

// fakeREST behaves like R2's REST object API: it ignores conditional headers.
type fakeREST struct {
	mu    sync.Mutex
	objs  map[string]string
	n     int
	puts  int
	auths []string
}

func (f *fakeREST) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auths = append(f.auths, r.Header.Get("Authorization"))
	const pre = "/client/v4/accounts/acc1/r2/buckets/aiman-artifacts/objects"
	if !strings.HasPrefix(r.URL.Path, pre) {
		w.WriteHeader(404)
		return
	}
	key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, pre), "/")
	switch {
	case r.Method == http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		f.n++
		f.puts++
		f.objs[key] = string(b)
		json.NewEncoder(w).Encode(map[string]any{"success": true, "result": map[string]any{"key": key, "etag": fmt.Sprintf("e%d", f.n)}})
		w.Header().Set("ETag", fmt.Sprintf(`"e%d"`, f.n))
	case r.Method == http.MethodGet && key == "":
		var out []any
		for k := range f.objs {
			if strings.HasPrefix(k, r.URL.Query().Get("prefix")) {
				out = append(out, map[string]any{"key": k})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "result": out, "result_info": map[string]any{"cursor": "", "is_truncated": false}})
	case r.Method == http.MethodGet:
		v, ok := f.objs[key]
		if !ok {
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []any{map[string]any{"code": 10007, "message": "The specified key does not exist."}}})
			return
		}
		w.Header().Set("ETag", `"`+etagOf(f, key)+`"`)
		io.WriteString(w, v)
	case r.Method == http.MethodDelete:
		delete(f.objs, key)
		json.NewEncoder(w).Encode(map[string]any{"success": true})
	default:
		w.WriteHeader(405)
	}
}

// etagOf derives a stable tag from the content for GET.
func etagOf(f *fakeREST, key string) string {
	return fmt.Sprintf("h%x", len(f.objs[key])*7919+len(key))
}

func TestCFObjectsEmulatesConditions(t *testing.T) {
	f := &fakeREST{objs: map[string]string{}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	tok := func(context.Context) (string, error) { return "oauth-tok", nil }
	b := NewCFObjects(srv.URL+"/client/v4", "acc1", "aiman-artifacts", tok, srv.Client())
	ctx := context.Background()

	if _, err := b.Put(ctx, "a/x/v/1/index.html", []byte("one"), "text/html", Cond{IfNoneMatch: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Put(ctx, "a/x/v/1/index.html", []byte("two"), "text/html", Cond{IfNoneMatch: true}); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("existing key with IfNoneMatch: %v", err)
	}
	if f.objs["a/x/v/1/index.html"] != "one" || f.puts != 1 {
		t.Fatalf("overwrote: %q after %d puts", f.objs["a/x/v/1/index.html"], f.puts)
	}
	data, etag, err := b.Get(ctx, "a/x/v/1/index.html")
	if err != nil || string(data) != "one" || !strings.HasPrefix(etag, `"`) {
		t.Fatalf("get %q %q %v", data, etag, err)
	}
	if _, err := b.Put(ctx, "a/x/v/1/index.html", []byte("three"), "text/html", Cond{IfMatch: `"stale"`}); !errors.Is(err, ErrPrecondition) {
		t.Fatalf("IfMatch mismatch: %v", err)
	}
	if _, err := b.Put(ctx, "a/x/v/1/index.html", []byte("four"), "text/html", Cond{IfMatch: etag}); err != nil {
		t.Fatalf("IfMatch match: %v", err)
	}
	if _, _, err := b.Get(ctx, "a/x/missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	keys, err := b.List(ctx, "a/x/")
	if err != nil || len(keys) != 1 {
		t.Fatalf("list %v %v", keys, err)
	}
	if err := b.Delete(ctx, keys); err != nil || len(f.objs) != 0 {
		t.Fatalf("delete %v %v", f.objs, err)
	}
	for _, a := range f.auths {
		if a != "Bearer oauth-tok" {
			t.Fatalf("authorization %q", a)
		}
	}
}

func TestCFOAuthTokenRefreshesNearExpiry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "default.json")
	write := func(tok string, exp time.Time) {
		b, _ := json.Marshal(map[string]any{"oauth_token": tok, "expiration_time": exp.UTC().Format(time.RFC3339Nano), "refresh_token": "r"})
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("fresh", time.Now().Add(30*time.Minute))
	refreshed := 0
	src := CFOAuthToken(path, func(context.Context) error {
		refreshed++
		write("renewed", time.Now().Add(time.Hour))
		return nil
	})
	if tok, err := src(context.Background()); err != nil || tok != "fresh" || refreshed != 0 {
		t.Fatalf("fresh: %q %v refreshed=%d", tok, err, refreshed)
	}
	write("old", time.Now().Add(time.Minute))
	if tok, err := src(context.Background()); err != nil || tok != "renewed" || refreshed != 1 {
		t.Fatalf("near expiry: %q %v refreshed=%d", tok, err, refreshed)
	}
	bad := CFOAuthToken(filepath.Join(t.TempDir(), "none.json"), func(context.Context) error { return nil })
	if _, err := bad(context.Background()); err == nil {
		t.Fatal("missing file gave a token")
	}
}

// ignoringBucket wraps fakeBucket but ignores conditions, as the REST API
// does; the publisher's lock must still prevent lost updates.
type ignoringBucket struct{ *fakeBucket }

func (b ignoringBucket) Put(ctx context.Context, key string, data []byte, ct string, _ Cond) (string, error) {
	time.Sleep(time.Millisecond)
	return b.fakeBucket.Put(ctx, key, data, ct, Cond{})
}

func TestPublisherSerialisesPerArtifact(t *testing.T) {
	inner := newFakeBucket()
	p := testPublisher(t, ignoringBucket{inner})
	ctx := context.Background()
	first, err := p.Publish(ctx, PublishRequest{Session: "s1", Files: page("v1")})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := p.Publish(ctx, PublishRequest{ID: first.ID, Session: "s1", Files: page(fmt.Sprintf("n%d", i))})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	m := readMeta(t, inner, first.ID)
	if m.Latest != 9 {
		t.Fatalf("latest %d, want 9 (a concurrent publish was lost)", m.Latest)
	}
	for v := 1; v <= 9; v++ {
		if _, ok := inner.objs[fmt.Sprintf("a/%s/v/%d/index.html", first.ID, v)]; !ok {
			t.Fatalf("version %d missing", v)
		}
	}
}
