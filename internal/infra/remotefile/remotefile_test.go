package remotefile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/local"
)

type fakeWriter struct {
	out     string
	err     error
	cmds    []string
	written map[string][]byte
	writes  int
}

func (f *fakeWriter) Execute(_ context.Context, cmd string) (string, error) {
	f.cmds = append(f.cmds, cmd)
	return f.out, f.err
}

func (f *fakeWriter) WriteFile(_ context.Context, path string, content []byte) error {
	if f.written == nil {
		f.written = map[string][]byte{}
	}
	f.written[path] = content
	f.writes++
	return nil
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestWriteIfChangedSkipsIdentical(t *testing.T) {
	content := []byte("hello\n")
	w := &fakeWriter{out: sum(content) + "  /p\n"}
	changed, err := WriteIfChanged(context.Background(), w, "/p", content)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("changed = true, want false")
	}
	if w.writes != 0 {
		t.Fatalf("WriteFile called %d times, want 0", w.writes)
	}
	if len(w.cmds) != 1 || !strings.Contains(w.cmds[0], "sha256sum -- '/p'") {
		t.Fatalf("cmds = %q, want one sha256sum of '/p'", w.cmds)
	}
}

func TestWriteIfChangedWritesOnDifference(t *testing.T) {
	content := []byte("hello\n")
	w := &fakeWriter{out: sum([]byte("other")) + "  /p\n"}
	changed, err := WriteIfChanged(context.Background(), w, "/p", content)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("changed = false, want true")
	}
	if w.writes != 1 || string(w.written["/p"]) != "hello\n" {
		t.Fatalf("writes = %d, written = %q", w.writes, w.written)
	}
}

func TestWriteIfChangedWritesWhenHashUnavailable(t *testing.T) {
	content := []byte("hello\n")
	cases := map[string]*fakeWriter{
		"empty output":   {out: ""},
		"execute error":  {out: sum(content) + "  /p\n", err: errors.New("ssh: broken pipe")},
		"no sha256sum":   {out: "bash: sha256sum: command not found\n"},
		"truncated hash": {out: sum(content)[:32] + "  /p\n"},
		"trailing junk":  {out: sum(content) + "x  /p\n"},
	}
	for name, w := range cases {
		t.Run(name, func(t *testing.T) {
			changed, err := WriteIfChanged(context.Background(), w, "/p", content)
			if err != nil {
				t.Fatal(err)
			}
			if !changed {
				t.Fatal("changed = false, want true")
			}
			if w.writes != 1 {
				t.Fatalf("WriteFile called %d times, want 1", w.writes)
			}
		})
	}
}

func TestWriteIfChangedQuotesPath(t *testing.T) {
	w := &fakeWriter{}
	if _, err := WriteIfChanged(context.Background(), w, "/a b/it's", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.cmds[0], `'/a b/it'\''s'`) {
		t.Fatalf("cmd = %q, want the path single-quoted", w.cmds[0])
	}
}

func TestWriteIfChangedRealBash(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dir with space", ".aiman_task.md")
	ex := local.NewExecutor(t.TempDir())
	content := []byte("# task\n")

	changed, err := WriteIfChanged(ctx, ex, path, content)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first write: changed = false, want true")
	}
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	changed, err = WriteIfChanged(ctx, ex, path, content)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("identical write: changed = true, want false")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(old) {
		t.Fatalf("mtime = %v, want unchanged %v", info.ModTime(), old)
	}

	changed, err = WriteIfChanged(ctx, ex, path, []byte("# new task\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || string(got) != "# new task\n" {
		t.Fatalf("changed = %v, content = %q", changed, got)
	}
}
