package gateway

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreateTokenCreatesThenReuses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, TokenFileName)

	tok, created, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created || len(tok) != tokenBytes*2 {
		t.Fatalf("created=%v len=%d", created, len(tok))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm %o want 0600", info.Mode().Perm())
	}

	again, created, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if created || again != tok {
		t.Fatalf("reuse created=%v got %q want %q", created, again, tok)
	}
}

func TestTokenEqual(t *testing.T) {
	if !TokenEqual("abc", "abc") {
		t.Fatal("equal tokens")
	}
	if TokenEqual("abc", "abd") {
		t.Fatal("different tokens")
	}
	if TokenEqual("abc", "ab") {
		t.Fatal("different lengths")
	}
}
