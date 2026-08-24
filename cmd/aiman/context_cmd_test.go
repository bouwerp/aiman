package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
