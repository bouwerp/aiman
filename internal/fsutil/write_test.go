package fsutil

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteIfChanged(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.txt")
	changed, err := WriteIfChanged(p, []byte("a"), 0o600)
	if err != nil || !changed {
		t.Fatalf("first write: changed=%v err=%v", changed, err)
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	changed, err = WriteIfChanged(p, []byte("a"), 0o600)
	if err != nil || changed {
		t.Fatalf("identical write: changed=%v err=%v", changed, err)
	}
	st, _ := os.Stat(p)
	if !st.ModTime().Equal(old) {
		t.Fatalf("mtime changed: %v", st.ModTime())
	}
	changed, err = WriteIfChanged(p, []byte("b"), 0o644)
	if err != nil || !changed {
		t.Fatalf("new bytes: changed=%v err=%v", changed, err)
	}
	got, _ := os.ReadFile(p)
	st, _ = os.Stat(p)
	if string(got) != "b" || st.Mode().Perm() != 0o644 {
		t.Fatalf("content=%q mode=%v", got, st.Mode())
	}
	entries, _ := os.ReadDir(filepath.Dir(p))
	if len(entries) != 1 {
		t.Fatalf("temp file left behind: %v", entries)
	}
}
