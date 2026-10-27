package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/contextstore"
	"github.com/bouwerp/aiman/internal/domain"
)

func startContextServer(t *testing.T) string {
	t.Helper()
	dir := shortTempDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	store := contextstore.NewFiles(shortTempDir(t))
	ctx, cancel := context.WithCancel(context.Background())
	srv := New(ln, nil, nil, nil, store, nil, "test")
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(cancel)
	return SocketPath(dir)
}

func TestContextPutGetListFindPack(t *testing.T) {
	sock := startContextServer(t)

	put, err := Call(sock, "context.put", map[string]any{
		"title":    "Auth cookie",
		"abstract": "SPA cannot set the session cookie.",
		"body":     "Set it on the API host.",
		"group":    "WTB-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if put.Error != nil {
		t.Fatalf("put: %+v", put.Error)
	}
	var putRes struct {
		Type string      `json:"type"`
		ID   string      `json:"id"`
		Note ContextNote `json:"note"`
	}
	raw, _ := json.Marshal(put.Result)
	if err := json.Unmarshal(raw, &putRes); err != nil || putRes.ID == "" {
		t.Fatalf("put result %s: %v", raw, err)
	}

	got, err := Call(sock, "context.get", map[string]any{"id": putRes.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got.Error != nil {
		t.Fatalf("get: %+v", got.Error)
	}
	var getRes struct {
		Note ContextNote `json:"note"`
	}
	raw, _ = json.Marshal(got.Result)
	if err := json.Unmarshal(raw, &getRes); err != nil {
		t.Fatal(err)
	}
	if getRes.Note.Body != "Set it on the API host." || getRes.Note.Namespace != domain.ContextNSGroup {
		t.Fatalf("get %s", raw)
	}

	list, err := Call(sock, "context.list", map[string]any{"group": "WTB-1"})
	if err != nil {
		t.Fatal(err)
	}
	if list.Error != nil {
		t.Fatalf("list: %+v", list.Error)
	}
	var listRes struct {
		Notes []ContextNote `json:"notes"`
	}
	raw, _ = json.Marshal(list.Result)
	if err := json.Unmarshal(raw, &listRes); err != nil || len(listRes.Notes) != 1 {
		t.Fatalf("list %s", raw)
	}
	if listRes.Notes[0].Body != "" {
		t.Fatalf("list must omit body: %s", raw)
	}

	found, err := Call(sock, "context.find", map[string]any{"text": "session cookie"})
	if err != nil {
		t.Fatal(err)
	}
	var findRes struct {
		Type string           `json:"type"`
		Hits []map[string]any `json:"hits"`
	}
	raw, _ = json.Marshal(found.Result)
	if err := json.Unmarshal(raw, &findRes); err != nil || len(findRes.Hits) != 1 {
		t.Fatalf("find %s", raw)
	}
	if findRes.Type != "context_find" {
		t.Fatalf("find type = %q", findRes.Type)
	}
	hit := findRes.Hits[0]
	if len(hit) != 5 || hit["id"] == "" || hit["type"] != "fact" || hit["title"] != "Auth cookie" || hit["abstract"] == "" || hit["age"] == "" {
		t.Fatalf("find hit = %#v", hit)
	}
	for _, key := range []string{"body", "ns", "key", "created", "session"} {
		if _, ok := hit[key]; ok {
			t.Fatalf("find hit leaked %q: %#v", key, hit)
		}
	}

	pack, err := Call(sock, "context.pack", map[string]any{"group": "WTB-1"})
	if err != nil {
		t.Fatal(err)
	}
	if pack.Error != nil {
		t.Fatalf("pack: %+v", pack.Error)
	}
	var packRes struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	raw, _ = json.Marshal(pack.Result)
	if err := json.Unmarshal(raw, &packRes); err != nil {
		t.Fatal(err)
	}
	if packRes.Type != "context_pack" || !strings.Contains(packRes.Text, "Auth cookie") {
		t.Fatalf("pack %s", raw)
	}

	stats, err := Call(sock, "context.stats", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Error != nil {
		t.Fatalf("stats: %+v", stats.Error)
	}
	raw, _ = json.Marshal(stats.Result)
	var statsRes struct {
		Type  string              `json:"type"`
		Stats domain.ContextStats `json:"stats"`
	}
	if err := json.Unmarshal(raw, &statsRes); err != nil {
		t.Fatal(err)
	}
	if statsRes.Type != "context_stats" || statsRes.Stats.Notes < 1 || statsRes.Stats.Ops["put"].Count < 1 {
		t.Fatalf("stats %s", raw)
	}
}

func TestContextGetMissing(t *testing.T) {
	sock := startContextServer(t)
	resp, err := Call(sock, "context.get", map[string]any{"id": "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != CodeNotFound {
		t.Fatalf("want not_found, got %+v", resp.Error)
	}
}

func TestContextStoreUnavailable(t *testing.T) {
	dir := shortTempDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = New(ln, nil, nil, nil, nil, nil, "t").Serve(ctx) }()
	resp, err := Call(SocketPath(dir), "context.list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("want unavailable, got %+v", resp.Error)
	}
}

func TestContextPutTypedOverSocket(t *testing.T) {
	sock := startContextServer(t)
	put, err := Call(sock, "context.put", map[string]any{
		"title":       "Auth decision",
		"group":       "WTB-1",
		"type":        "decision",
		"supersedes":  "old-1",
		"valid_until": "2030-01-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	if put.Error != nil {
		t.Fatalf("put: %+v", put.Error)
	}
	var res struct {
		Note ContextNote `json:"note"`
	}
	raw, _ := json.Marshal(put.Result)
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if res.Note.Type != "decision" || res.Note.Supersedes != "old-1" {
		t.Fatalf("note = %+v", res.Note)
	}
	if res.Note.ValidUntil != "2030-01-01T00:00:00Z" {
		t.Fatalf("valid_until = %q", res.Note.ValidUntil)
	}
}

func TestContextFindRankedHits(t *testing.T) {
	sock := startContextServer(t)
	for _, note := range []map[string]any{
		{"title": "Fact", "body": "needle", "group": "G1", "type": "fact"},
		{"title": "Decision", "body": "needle", "group": "G1", "type": "decision"},
	} {
		if resp, err := Call(sock, "context.put", note); err != nil || resp.Error != nil {
			t.Fatalf("put %v: %v %+v", note, err, resp.Error)
		}
	}
	resp, err := Call(sock, "context.find", map[string]any{"text": "needle"})
	if err != nil {
		t.Fatal(err)
	}
	var res struct {
		Hits []map[string]any `json:"hits"`
	}
	raw, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 2 || res.Hits[0]["type"] != "decision" {
		t.Fatalf("hits = %#v", res.Hits)
	}
}
