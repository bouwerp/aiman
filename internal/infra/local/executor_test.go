package local

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileExpandsLeadingHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := NewExecutor("").WriteFile(context.Background(), "$HOME/sub/f.json", []byte("x")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(home, "sub", "f.json")) //nolint:gosec // the test's own temp dir
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "x" {
		t.Fatalf("content %q", got)
	}
}

func TestWriteFileLeavesOtherPathsAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "$HOME", "f.json")
	if err := NewExecutor("").WriteFile(context.Background(), path, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a $HOME that does not lead the path is literal: %v", err)
	}
}
