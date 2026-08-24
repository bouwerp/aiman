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
	dir := t.TempDir()
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	store := contextstore.NewFiles(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	srv := New(ln, nil, nil, nil, store, "test")
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
	raw, _ = json.Marshal(found.Result)
	if err := json.Unmarshal(raw, &listRes); err != nil || len(listRes.Notes) != 1 {
		t.Fatalf("find %s", raw)
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
	dir := t.TempDir()
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = New(ln, nil, nil, nil, nil, "t").Serve(ctx) }()
	resp, err := Call(SocketPath(dir), "context.list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("want unavailable, got %+v", resp.Error)
	}
}
