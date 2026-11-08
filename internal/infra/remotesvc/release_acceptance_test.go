package remotesvc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"
)

type releaseServer struct {
	mu          sync.Mutex
	status      int
	body        string
	etag        string // sent as ETag; a request whose If-None-Match equals it gets 304
	ifNoneMatch []string
}

func (s *releaseServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	inm := r.Header.Get("If-None-Match")
	s.ifNoneMatch = append(s.ifNoneMatch, inm)
	if s.etag != "" && inm == s.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if s.etag != "" {
		w.Header().Set("ETag", s.etag)
	}
	w.WriteHeader(s.status)
	_, _ = w.Write([]byte(s.body))
}

func (s *releaseServer) sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ifNoneMatch...)
}

func serveRelease(t *testing.T, s *releaseServer) {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	prev := latestReleaseURL
	latestReleaseURL = srv.URL
	t.Cleanup(func() { latestReleaseURL = prev })
}

const readyBody = `{
  "tag_name": "v0.35.0",
  "published_at": "2026-11-08T10:07:00Z",
  "assets": [
    {"name": "aiman-linux-amd64.tar.gz"},
    {"name": "aiman-darwin-arm64.tar.gz"},
    {"name": "checksums.txt"},
    {"name": "install.sh"}
  ]
}`

const unreadyBody = `{
  "tag_name": "v0.35.0",
  "published_at": "2026-11-08T10:07:00Z",
  "assets": [
    {"name": "aiman-linux-amd64.tar.gz"},
    {"name": "aiman-darwin-arm64.tar.gz"}
  ]
}`

func TestP1LatestReleaseReady(t *testing.T) {
	s := &releaseServer{status: http.StatusOK, body: readyBody, etag: `W/"abc123"`}
	serveRelease(t, s)

	rel, changed, err := LatestRelease(context.Background(), "")
	if err != nil {
		t.Fatalf("LatestRelease: %v", err)
	}
	if !changed {
		t.Fatal("changed = false on a 200")
	}
	if rel.Tag != "v0.35.0" {
		t.Fatalf("Tag = %q, want v0.35.0", rel.Tag)
	}
	if rel.ETag != `W/"abc123"` {
		t.Fatalf("ETag = %q, want the response header", rel.ETag)
	}
	if !rel.Ready() {
		t.Fatalf("Ready() = false with assets %q", rel.Assets)
	}
	if !slices.Contains(rel.Assets, "checksums.txt") || !slices.Contains(rel.Assets, "aiman-linux-amd64.tar.gz") {
		t.Fatalf("Assets = %q, want the asset names", rel.Assets)
	}
	if want := time.Date(2026, 11, 8, 10, 7, 0, 0, time.UTC); !rel.PublishedAt.Equal(want) {
		t.Fatalf("PublishedAt = %v, want %v", rel.PublishedAt, want)
	}
	if got := s.sent(); len(got) != 1 || got[0] != "" {
		t.Fatalf("If-None-Match sent %q, want none without an etag", got)
	}
}

func TestP1LatestReleaseNotReadyWithoutChecksums(t *testing.T) {
	serveRelease(t, &releaseServer{status: http.StatusOK, body: unreadyBody, etag: `"e1"`})
	rel, changed, err := LatestRelease(context.Background(), "")
	if err != nil || !changed {
		t.Fatalf("LatestRelease: changed=%v err=%v", changed, err)
	}
	if rel.Tag != "v0.35.0" {
		t.Fatalf("Tag = %q", rel.Tag)
	}
	if rel.Ready() {
		t.Fatalf("Ready() = true without checksums.txt: %q", rel.Assets)
	}
}

func TestP1LatestReleaseReadyRule(t *testing.T) {
	for _, c := range []struct {
		assets []string
		want   bool
	}{
		{nil, false},
		{[]string{"install.sh"}, false},
		{[]string{"checksums.txt.sig"}, false},
		{[]string{"a.tar.gz", "checksums.txt"}, true},
	} {
		if got := (Release{Tag: "v1.0.0", Assets: c.assets}).Ready(); got != c.want {
			t.Errorf("Ready(%q) = %v, want %v", c.assets, got, c.want)
		}
	}
}

func TestP1LatestReleaseNotModified(t *testing.T) {
	s := &releaseServer{status: http.StatusOK, body: readyBody, etag: `"e1"`}
	serveRelease(t, s)

	rel, changed, err := LatestRelease(context.Background(), `"e1"`)
	if err != nil {
		t.Fatalf("LatestRelease on 304: %v", err)
	}
	if changed {
		t.Fatal("changed = true on a 304")
	}
	if rel.Tag != "" || len(rel.Assets) != 0 || rel.ETag != "" {
		t.Fatalf("304 returned %+v, want a zero Release", rel)
	}
	if got := s.sent(); len(got) != 1 || got[0] != `"e1"` {
		t.Fatalf("If-None-Match sent %q, want the etag", got)
	}

	// A stale etag gets the full answer.
	rel, changed, err = LatestRelease(context.Background(), `"old"`)
	if err != nil || !changed || rel.Tag != "v0.35.0" || rel.ETag != `"e1"` {
		t.Fatalf("stale etag: rel=%+v changed=%v err=%v", rel, changed, err)
	}
}

func TestP1LatestReleaseRefuses(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		body   string
	}{
		"invalid tag":  {http.StatusOK, `{"tag_name":"v1.0.0; id","assets":[{"name":"checksums.txt"}]}`},
		"describe tag": {http.StatusOK, `{"tag_name":"v0.35.0-3-gabc1234","assets":[]}`},
		"no tag":       {http.StatusOK, `{"assets":[]}`},
		"bad json":     {http.StatusOK, `<html>`},
		"rate limited": {http.StatusForbidden, `{}`},
		"server error": {http.StatusInternalServerError, `{}`},
	} {
		t.Run(name, func(t *testing.T) {
			serveRelease(t, &releaseServer{status: c.status, body: c.body})
			if rel, _, err := LatestRelease(context.Background(), ""); err == nil {
				t.Fatalf("accepted %+v", rel)
			}
		})
	}
}

func TestP1LatestReleaseTagStillWorks(t *testing.T) {
	serveRelease(t, &releaseServer{status: http.StatusOK, body: unreadyBody, etag: `"e1"`})
	tag, err := LatestReleaseTag(context.Background())
	if err != nil || tag != "v0.35.0" {
		t.Fatalf("LatestReleaseTag = %q, %v; want v0.35.0", tag, err)
	}

	serveRelease(t, &releaseServer{status: http.StatusOK, body: `{"tag_name":"v1.0.0; id"}`})
	if tag, err := LatestReleaseTag(context.Background()); err == nil {
		t.Fatalf("LatestReleaseTag accepted %q", tag)
	}
}
