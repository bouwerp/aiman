package artifact

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// Acceptance tests for docs/specs/artifacts.md piece A (publisher). Do not edit.

type fakeObj struct {
	data []byte
	etag string
	ct   string
}

type fakeBucket struct {
	mu      sync.Mutex
	objs    map[string]fakeObj
	puts    []string // keys in PUT order
	conds   map[string]Cond
	n       int
	failPut func(key string) error
}

func newFakeBucket() *fakeBucket {
	return &fakeBucket{objs: map[string]fakeObj{}, conds: map[string]Cond{}}
}

func (b *fakeBucket) Get(_ context.Context, key string) ([]byte, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	o, ok := b.objs[key]
	if !ok {
		return nil, "", ErrNotFound
	}
	return append([]byte(nil), o.data...), o.etag, nil
}

func (b *fakeBucket) Put(_ context.Context, key string, data []byte, ct string, c Cond) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failPut != nil {
		if err := b.failPut(key); err != nil {
			return "", err
		}
	}
	o, exists := b.objs[key]
	if c.IfNoneMatch && exists {
		return "", ErrPrecondition
	}
	if c.IfMatch != "" && (!exists || o.etag != c.IfMatch) {
		return "", ErrPrecondition
	}
	b.n++
	etag := fmt.Sprintf(`"e%d"`, b.n)
	b.objs[key] = fakeObj{data: append([]byte(nil), data...), etag: etag, ct: ct}
	b.puts = append(b.puts, key)
	b.conds[key] = c
	return etag, nil
}

func (b *fakeBucket) Delete(_ context.Context, keys []string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, k := range keys {
		delete(b.objs, k)
	}
	return nil
}

func (b *fakeBucket) List(_ context.Context, prefix string) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for k := range b.objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

func testStore(t *testing.T) *SQLStore {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := OpenSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func testPublisher(t *testing.T, b Bucket) *Publisher {
	t.Helper()
	ids := 0
	return &Publisher{
		Bucket: b,
		Store:  testStore(t),
		Host:   "aiman.fyi",
		Kit:    []File{{Path: "theme.css", Data: []byte("css")}, {Path: "kit.js", Data: []byte("js")}},
		Now:    func() time.Time { return time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC) },
		NewID: func() string {
			ids++
			return fmt.Sprintf("abcdefghijklmnopqrs%c", 'a'+ids-1)
		},
	}
}

func page(html string, extra ...File) []File {
	return append([]File{{Path: "index.html", Data: []byte(html)}}, extra...)
}

func readMeta(t *testing.T, b *fakeBucket, id string) Meta {
	t.Helper()
	data, _, err := b.Get(context.Background(), "a/"+id+"/meta.json")
	if err != nil {
		t.Fatal(err)
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPublishWritesVersionFilesThenMeta(t *testing.T) {
	b := newFakeBucket()
	p := testPublisher(t, b)
	got, err := p.Publish(context.Background(), PublishRequest{
		Title: "Load test", Session: "s1",
		Files: page("<p>hi</p>", File{Path: "img/a.svg", Data: []byte("<svg/>")}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "abcdefghijklmnopqrsa" || got.Version != 1 || got.URL != "https://aiman.fyi/a/abcdefghijklmnopqrsa/" {
		t.Fatalf("published %+v", got)
	}
	base := "a/" + got.ID + "/"
	want := map[string]bool{base + "v/1/index.html": true, base + "v/1/img/a.svg": true, base + "v/1/kit/theme.css": true, base + "v/1/kit/kit.js": true}
	if len(b.puts) != len(want)+1 {
		t.Fatalf("puts %v", b.puts)
	}
	for _, k := range b.puts[:len(b.puts)-1] {
		if !want[k] {
			t.Fatalf("unexpected put %s (all %v)", k, b.puts)
		}
		if !b.conds[k].IfNoneMatch {
			t.Fatalf("version file %s written without If-None-Match", k)
		}
	}
	last := b.puts[len(b.puts)-1]
	if last != base+"meta.json" {
		t.Fatalf("meta.json must be written last, got %v", b.puts)
	}
	if !b.conds[last].IfNoneMatch {
		t.Fatal("a new artifact's meta.json must be written with If-None-Match")
	}
	m := readMeta(t, b, got.ID)
	if m.Latest != 1 || m.Title != "Load test" || len(m.Share.Emails) != 0 || len(m.Share.Domains) != 0 || m.Deleted {
		t.Fatalf("meta %+v", m)
	}
	if ct := b.objs[base+"v/1/index.html"].ct; !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("index.html content type %q", ct)
	}
	rec, err := p.Store.Get(context.Background(), got.ID)
	if err != nil || rec.Latest != 1 || rec.Session != "s1" || rec.Title != "Load test" {
		t.Fatalf("record %+v %v", rec, err)
	}
	vs, err := p.Store.Versions(context.Background(), got.ID)
	if err != nil || len(vs) != 1 || vs[0].N != 1 || len(vs[0].Files) != 4 {
		t.Fatalf("versions %+v %v", vs, err)
	}
}

func TestPublishNewVersionAndExpectVersion(t *testing.T) {
	b := newFakeBucket()
	p := testPublisher(t, b)
	ctx := context.Background()
	first, err := p.Publish(ctx, PublishRequest{Session: "s1", Files: page("v1")})
	if err != nil {
		t.Fatal(err)
	}
	metaKey := "a/" + first.ID + "/meta.json"
	etag1 := b.objs[metaKey].etag

	second, err := p.Publish(ctx, PublishRequest{ID: first.ID, Session: "s1", ExpectVersion: 1, Files: page("v2")})
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.Version != 2 || second.URL != first.URL {
		t.Fatalf("second %+v", second)
	}
	if c := b.conds[metaKey]; c.IfMatch != etag1 {
		t.Fatalf("meta.json update must use If-Match %s, got %+v", etag1, c)
	}
	if !bytes.Equal(b.objs["a/"+first.ID+"/v/1/index.html"].data, []byte("v1")) {
		t.Fatal("version 1 was changed")
	}
	if readMeta(t, b, first.ID).Latest != 2 {
		t.Fatal("meta latest not 2")
	}
	if _, err := p.Publish(ctx, PublishRequest{ID: first.ID, Session: "s1", ExpectVersion: 1, Files: page("v3")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale expect_version: %v", err)
	}
	if _, err := p.Publish(ctx, PublishRequest{ID: "zzzzzzzzzzzzzzzzzzzz", Session: "s1", Files: page("x")}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
}

func TestPublishMetaRaceIsConflictAndStoreUnchanged(t *testing.T) {
	b := newFakeBucket()
	p := testPublisher(t, b)
	ctx := context.Background()
	first, err := p.Publish(ctx, PublishRequest{Session: "s1", Files: page("v1")})
	if err != nil {
		t.Fatal(err)
	}
	metaKey := "a/" + first.ID + "/meta.json"
	// Another writer changes meta.json between our read and our write.
	b.failPut = func(key string) error {
		if key == metaKey {
			b.objs[metaKey] = fakeObj{data: b.objs[metaKey].data, etag: `"other"`}
		}
		return nil
	}
	if _, err := p.Publish(ctx, PublishRequest{ID: first.ID, Session: "s1", Files: page("v2")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("meta race: %v", err)
	}
	rec, _ := p.Store.Get(ctx, first.ID)
	if rec.Latest != 1 {
		t.Fatalf("store moved to %d after a failed meta write", rec.Latest)
	}
}

func TestPublishRefusals(t *testing.T) {
	p := testPublisher(t, newFakeBucket())
	ctx := context.Background()
	if _, err := p.Publish(ctx, PublishRequest{Session: "s1", Files: []File{{Path: "a.html", Data: []byte("x")}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("no index.html: %v", err)
	}
	for _, bad := range []string{"../x", "/abs", "a/../../b", "a//b", ""} {
		if _, err := p.Publish(ctx, PublishRequest{Session: "s1", Files: page("x", File{Path: bad, Data: []byte("y")})}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("path %q: %v", bad, err)
		}
	}
	many := page("x")
	for i := 0; i < MaxFiles; i++ {
		many = append(many, File{Path: fmt.Sprintf("f%d.txt", i), Data: []byte("y")})
	}
	if _, err := p.Publish(ctx, PublishRequest{Session: "s1", Files: many}); !errors.Is(err, ErrLimits) {
		t.Fatalf("too many files: %v", err)
	}
	big := make([]byte, MaxFileBytes+1)
	if _, err := p.Publish(ctx, PublishRequest{Session: "s1", Files: page("x", File{Path: "big.bin", Data: big})}); !errors.Is(err, ErrLimits) {
		t.Fatalf("big file: %v", err)
	}
	chunk := make([]byte, 20<<20)
	total := page("x")
	for i := 0; i < 4; i++ {
		total = append(total, File{Path: fmt.Sprintf("c%d.bin", i), Data: chunk})
	}
	if _, err := p.Publish(ctx, PublishRequest{Session: "s1", Files: total}); !errors.Is(err, ErrLimits) {
		t.Fatalf("over total: %v", err)
	}
}

func TestPublishOwnership(t *testing.T) {
	p := testPublisher(t, newFakeBucket())
	ctx := context.Background()
	a, err := p.Publish(ctx, PublishRequest{Session: "s1", Files: page("v1")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Publish(ctx, PublishRequest{ID: a.ID, Session: "s2", Files: page("v2")}); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("other session: %v", err)
	}
	if _, err := p.Publish(ctx, PublishRequest{ID: a.ID, Session: "", Files: page("v2")}); err != nil {
		t.Fatalf("operator: %v", err)
	}
	p.CanWrite = func(owner, caller string) bool { return owner == "s1" && caller == "parent" }
	if _, err := p.Publish(ctx, PublishRequest{ID: a.ID, Session: "parent", Files: page("v3")}); err != nil {
		t.Fatalf("CanWrite parent: %v", err)
	}
}

func TestShareAndDelete(t *testing.T) {
	b := newFakeBucket()
	p := testPublisher(t, b)
	ctx := context.Background()
	a, err := p.Publish(ctx, PublishRequest{Session: "s1", Files: page("v1")})
	if err != nil {
		t.Fatal(err)
	}
	m, err := p.Share(ctx, a.ID, ShareList{Emails: []string{"Lead@Partner.com"}, Domains: []string{"Example.COM"}}, ShareList{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(m.Share.Emails, ",") != "lead@partner.com" || strings.Join(m.Share.Domains, ",") != "example.com" {
		t.Fatalf("share %+v", m.Share)
	}
	if got := readMeta(t, b, a.ID); got.Latest != 1 || len(got.Share.Emails) != 1 {
		t.Fatalf("meta after share %+v", got)
	}
	if c := b.conds["a/"+a.ID+"/meta.json"]; c.IfMatch == "" {
		t.Fatal("share must write meta.json with If-Match")
	}
	m, err = p.Share(ctx, a.ID, ShareList{}, ShareList{Emails: []string{"lead@partner.com"}}, false)
	if err != nil || len(m.Share.Emails) != 0 || len(m.Share.Domains) != 1 {
		t.Fatalf("unshare one %+v %v", m, err)
	}
	m, err = p.Share(ctx, a.ID, ShareList{}, ShareList{}, true)
	if err != nil || len(m.Share.Emails)+len(m.Share.Domains) != 0 {
		t.Fatalf("unshare all %+v %v", m, err)
	}
	rec, _ := p.Store.Get(ctx, a.ID)
	if len(rec.Share.Domains) != 0 {
		t.Fatalf("store share %+v", rec.Share)
	}

	// A failed meta write leaves the store unchanged.
	b.failPut = func(string) error { return errors.New("r2 down") }
	if _, err := p.Share(ctx, a.ID, ShareList{Emails: []string{"x@y.com"}}, ShareList{}, false); err == nil {
		t.Fatal("share with R2 down succeeded")
	}
	rec, _ = p.Store.Get(ctx, a.ID)
	if len(rec.Share.Emails) != 0 {
		t.Fatal("store changed although R2 refused")
	}
	b.failPut = nil

	if err := p.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if !readMeta(t, b, a.ID).Deleted {
		t.Fatal("meta not marked deleted")
	}
	if _, ok := b.objs["a/"+a.ID+"/v/1/index.html"]; ok {
		t.Fatal("version files kept after delete")
	}
	list, _ := p.Store.List(ctx)
	for _, r := range list {
		if r.ID == a.ID {
			t.Fatal("deleted artifact still listed")
		}
	}
}
