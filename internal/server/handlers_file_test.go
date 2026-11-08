package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestUTF8Page(t *testing.T) {
	euro := "€" // three bytes
	cases := []struct {
		name  string
		data  string
		limit int
		want  string
	}{
		{"shorter than limit", "abc", 10, "abc"},
		{"cut on a boundary", "abcdef", 3, "abc"},
		{"cut inside a rune moves it on", "a" + euro + "b", 2, "a"},
		{"cut after the rune's lead byte", "ab" + euro, 3, "ab"},
		{"rune longer than the limit stays whole", euro + "b", 1, euro},
		{"invalid bytes cut at the limit", "\x80\x80\x80\x80\x80", 2, "\x80\x80"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := string(utf8Page([]byte(c.data), c.limit)); got != c.want {
				t.Fatalf("utf8Page(%q, %d) = %q, want %q", c.data, c.limit, got, c.want)
			}
		})
	}
}

func TestSecretFileName(t *testing.T) {
	for _, name := range []string{".env", ".env.local", "tls.pem", "server.key", "id_rsa", "id_rsa.pub", "id_ed25519", "credentials"} {
		if !secretFileName(name) {
			t.Errorf("%q not treated as a secret", name)
		}
	}
	for _, name := range []string{"env", "main.go", "keys.go", "credentials.md", "README.env.md", "pem"} {
		if secretFileName(name) {
			t.Errorf("%q treated as a secret", name)
		}
	}
}

func TestConfineSessionFile(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{filepath.Join(root, "a.txt"), filepath.Join(sub, "b.txt"), filepath.Join(root, "real.env.txt")} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A secret-named link to an ordinary file and an ordinary link to a
	// secret are both refused.
	if err := os.Symlink(filepath.Join(root, "a.txt"), filepath.Join(root, ".env")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "id_rsa"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "id_rsa"), filepath.Join(root, "plain.txt")); err != nil {
		t.Fatal(err)
	}
	sess := domain.Session{ID: "s", WorktreePath: root, WorkingDirectory: sub}

	cases := []struct {
		path, wantPath, wantCode string
	}{
		{"b.txt", filepath.Join(sub, "b.txt"), ""},
		{"../a.txt", filepath.Join(root, "a.txt"), ""},
		{"../../etc/passwd", "", CodeForbidden},
		{"/etc/passwd", "", CodeForbidden},
		{"../.env", "", CodeForbidden},
		{"../plain.txt", "", CodeForbidden},
		{"nope.txt", "", CodeNotFound},
		{"../real.env.txt", filepath.Join(root, "real.env.txt"), ""},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			got, code, _ := confineSessionFile(sess, c.path)
			if code != c.wantCode || got != c.wantPath {
				t.Fatalf("confineSessionFile(%q) = %q, %q; want %q, %q", c.path, got, code, c.wantPath, c.wantCode)
			}
		})
	}

	t.Run("relative path with no working directory uses the worktree", func(t *testing.T) {
		got, code, _ := confineSessionFile(domain.Session{WorktreePath: root}, "a.txt")
		if code != "" || got != filepath.Join(root, "a.txt") {
			t.Fatalf("got %q, %q", got, code)
		}
	})
}
