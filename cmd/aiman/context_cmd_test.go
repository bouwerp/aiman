package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/contextstore"
	"github.com/bouwerp/aiman/internal/domain"
)

func TestContextNSKey(t *testing.T) {
	ns, key := contextNSKey(map[string]string{"group": "WTB-1"})
	if ns != domain.ContextNSGroup || key != "WTB-1" {
		t.Fatalf("%s %s", ns, key)
	}
	ns, key = contextNSKey(map[string]string{"repo": "org/repo"})
	if ns != domain.ContextNSRepo || key != "org/repo" {
		t.Fatalf("%s %s", ns, key)
	}
}

func TestReadPutBodyFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(p, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readPutBody(map[string]string{"body-file": p}, nil)
	if err != nil || got != "hello" {
		t.Fatalf("%q %v", got, err)
	}
	got, err = readPutBody(map[string]string{}, []string{"a", "b"})
	if err != nil || got != "a b" {
		t.Fatalf("%q %v", got, err)
	}
}

func TestContextViaFilesPack(t *testing.T) {
	store := contextstore.NewFiles(t.TempDir())
	if _, err := store.Put(context.Background(), domain.ContextEntry{
		Namespace: domain.ContextNSGroup, Key: "G1", Title: "Note", Abstract: "abs",
	}); err != nil {
		t.Fatal(err)
	}
	if err := contextViaFiles(store, "pack", []string{"--group", "G1"}); err != nil {
		t.Fatal(err)
	}
	if err := contextViaFiles(store, "stats", nil); err != nil {
		t.Fatal(err)
	}
}

func TestContextRPCStats(t *testing.T) {
	method, _, err := contextRPC("stats", nil)
	if err != nil || method != "context.stats" {
		t.Fatalf("%s %v", method, err)
	}
}

func TestParseImportAgentsFromCLI(t *testing.T) {
	if got := contextstore.ParseImportAgents("claude,grok"); len(got) != 2 {
		t.Fatalf("%v", got)
	}
}

func TestContextRPCRequiresTitle(t *testing.T) {
	_, _, err := contextRPC("put", []string{"--abstract", "x"})
	if err == nil {
		t.Fatal("expected usage error")
	}
}

func TestParseValidUntil(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	got, err := parseValidUntil("30d", now)
	if err != nil || !got.Equal(now.Add(720*time.Hour)) {
		t.Fatalf("30d = %v %v", got, err)
	}
	got, err = parseValidUntil("2026-12-31", now)
	if err != nil || got.Format(time.RFC3339) != "2026-12-31T00:00:00Z" {
		t.Fatalf("date = %v %v", got, err)
	}
	got, err = parseValidUntil("2030-01-01T10:30:00Z", now)
	if err != nil || got.Format(time.RFC3339) != "2030-01-01T10:30:00Z" {
		t.Fatalf("rfc3339 = %v %v", got, err)
	}
	if _, err := parseValidUntil("x", now); err == nil {
		t.Fatal("expected error")
	}
}

func TestContextRPCTypedPut(t *testing.T) {
	_, params, err := contextRPC("put", []string{
		"--title", "T", "--type", "decision", "--supersedes", "old-1", "--valid-until", "2030-01-01",
	})
	if err != nil {
		t.Fatal(err)
	}
	if params["type"] != "decision" || params["supersedes"] != "old-1" || params["valid_until"] != "2030-01-01T00:00:00Z" {
		t.Fatalf("params = %#v", params)
	}
}
