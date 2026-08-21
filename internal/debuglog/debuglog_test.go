package debuglog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	code := m.Run()
	resetForTest()
	os.Exit(code)
}

func TestWriteWithoutEnableUsesFallbackPath(t *testing.T) {
	resetForTest()
	dir := t.TempDir()
	p := filepath.Join(dir, "fallback.log")
	SetPath(p)
	t.Cleanup(resetForTest)

	if Enabled() {
		t.Fatal("enabled without Enable")
	}
	if err := Write("hello\n"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "hello\n" {
		t.Fatalf("got %q", b)
	}
}

func TestEnableWritesAndReportsPath(t *testing.T) {
	resetForTest()
	dir := t.TempDir()
	p := filepath.Join(dir, "debug.log")
	if err := Enable(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resetForTest)

	if !Enabled() {
		t.Fatal("expected enabled")
	}
	if Path() != p {
		t.Fatalf("path = %s", Path())
	}
	if Writer() == nil {
		t.Fatal("writer is nil")
	}
	if err := Write("line1\n"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "line1") {
		t.Fatalf("got %q", b)
	}
}

func TestEnableCreatesParentDir(t *testing.T) {
	resetForTest()
	p := filepath.Join(t.TempDir(), "nested", "dir", "debug.log")
	if err := Enable(p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(resetForTest)
	if err := Write("ok\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal(err)
	}
}
