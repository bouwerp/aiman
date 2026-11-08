//go:build acceptance

package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bouwerp/aiman/internal/domain"
)

const (
	fileSess  = "fs"
	fileOther = "fs-other"
	fileBare  = "fs-bare"
)

// fileReply is the session.file result.
type fileReply struct {
	Type    string `json:"type"`
	Session string `json:"session"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Offset  int64  `json:"offset"`
	Data    string `json:"data"`
	EOF     bool   `json:"eof"`
	Binary  bool   `json:"binary"`
}

// bigUTF8 is 200 KiB in which a 64 KiB page boundary falls inside a
// three-byte rune ("x€€" is 7 bytes and 65536 mod 7 is 2).
var bigUTF8 = strings.Repeat("x€€", 29257) + "x"

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fileServer is a serve rig whose session fs has a temp worktree that is also
// its working directory, beside an unrelated session and one with no roots.
// It returns the worktree after EvalSymlinks.
func fileServer(t *testing.T) (*Server, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	writeFixture(t, filepath.Join(root, "notes.md"), "# notes\nhello\n")
	writeFixture(t, filepath.Join(root, "big.txt"), bigUTF8)
	writeFixture(t, filepath.Join(root, "huge.txt"), strings.Repeat("a", 300<<10))
	writeFixture(t, filepath.Join(root, "blob.bin"), "head\x00tail")
	writeFixture(t, filepath.Join(root, ".env"), "TOKEN=redacted\n")
	writeFixture(t, filepath.Join(root, "sub", ".git", "config"), "[core]\n")
	writeFixture(t, filepath.Join(root, "key.pem"), "-----BEGIN redacted-----\n")
	writeFixture(t, filepath.Join(outside, "secret.txt"), "outside redacted\n")
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}

	row := func(id, worktree, wd string) domain.Session {
		return domain.Session{
			ID: id, Name: id, Group: "g", TerminalName: id,
			Backend: domain.BackendPTY, WorktreePath: worktree, WorkingDirectory: wd,
		}
	}
	srv, _, _ := ptyServer(t,
		row(fileSess, root, root),
		row(fileOther, outside, outside),
		row(fileBare, "", ""))
	return srv, root
}

func fileCall(t *testing.T, srv *Server, caller string, params map[string]any) Response {
	t.Helper()
	return scopeCall(t, srv, caller, scopeNoPID, "session.file", params)
}

func readFile(t *testing.T, srv *Server, params map[string]any) fileReply {
	t.Helper()
	if _, ok := params["session"]; !ok {
		params["session"] = fileSess
	}
	var got fileReply
	decodeTrResult(t, fileCall(t, srv, "", params), &got)
	return got
}

func TestSessionFileReadsRelativeAndAbsolute(t *testing.T) {
	srv, root := fileServer(t)
	want := "# notes\nhello\n"
	abs := filepath.Join(root, "notes.md")
	for _, path := range []string{"notes.md", abs} {
		t.Run(path, func(t *testing.T) {
			got := readFile(t, srv, map[string]any{"path": path})
			if got.Type != "file" || got.Session != fileSess {
				t.Fatalf("type %q session %q, want file and %s", got.Type, got.Session, fileSess)
			}
			if got.Path != abs {
				t.Fatalf("path %q, want the absolute %q", got.Path, abs)
			}
			if got.Data != want || !got.EOF || got.Size != int64(len(want)) || got.Offset != 0 || got.Binary {
				t.Fatalf("got %+v, want the whole file, eof, size %d, offset 0, not binary", got, len(want))
			}
		})
	}
}

func TestSessionFilePagesConcatenate(t *testing.T) {
	srv, _ := fileServer(t)
	var all strings.Builder
	var offset int64
	pages := 0
	for {
		if pages > 10 {
			t.Fatalf("no eof after %d pages", pages)
		}
		got := readFile(t, srv, map[string]any{"path": "big.txt", "offset": offset, "limit": 65536})
		pages++
		if got.Offset != offset {
			t.Fatalf("page %d: offset %d, want the requested %d", pages, got.Offset, offset)
		}
		if got.Size != int64(len(bigUTF8)) {
			t.Fatalf("page %d: size %d, want %d", pages, got.Size, len(bigUTF8))
		}
		if len(got.Data) > 65536 {
			t.Fatalf("page %d: %d bytes, over the limit", pages, len(got.Data))
		}
		if !utf8.ValidString(got.Data) {
			t.Fatalf("page %d at offset %d is not cut on a UTF-8 boundary", pages, offset)
		}
		all.WriteString(got.Data)
		offset += int64(len(got.Data))
		if got.EOF {
			break
		}
		if got.Data == "" {
			t.Fatalf("page %d: empty data without eof", pages)
		}
	}
	if pages < 4 {
		t.Fatalf("%d pages for a 200 KiB file at 64 KiB a page, want at least 4", pages)
	}
	if all.String() != bigUTF8 {
		t.Fatalf("pages concatenate to %d bytes that differ from the %d-byte file", all.Len(), len(bigUTF8))
	}
}

func TestSessionFileLimits(t *testing.T) {
	srv, _ := fileServer(t)
	if FileMaxReplyBytes != 262144 {
		t.Fatalf("FileMaxReplyBytes = %d, want 262144", FileMaxReplyBytes)
	}

	t.Run("limit capped", func(t *testing.T) {
		got := readFile(t, srv, map[string]any{"path": "huge.txt", "limit": 1_000_000})
		if len(got.Data) != 262144 || got.EOF {
			t.Fatalf("%d bytes eof %v, want 262144 and not eof", len(got.Data), got.EOF)
		}
	})

	t.Run("default limit", func(t *testing.T) {
		got := readFile(t, srv, map[string]any{"path": "huge.txt"})
		if len(got.Data) != 65536 || got.EOF || got.Offset != 0 {
			t.Fatalf("%d bytes eof %v offset %d, want 65536, not eof, offset 0", len(got.Data), got.EOF, got.Offset)
		}
	})

	t.Run("offset past the end", func(t *testing.T) {
		got := readFile(t, srv, map[string]any{"path": "notes.md", "offset": 1 << 20})
		if got.Data != "" || !got.EOF {
			t.Fatalf("data %q eof %v, want empty and eof", got.Data, got.EOF)
		}
	})
}

func TestSessionFileBinary(t *testing.T) {
	srv, _ := fileServer(t)
	got := readFile(t, srv, map[string]any{"path": "blob.bin"})
	if !got.Binary || got.Data != "" || !got.EOF {
		t.Fatalf("binary %v data %q eof %v, want binary, empty, eof", got.Binary, got.Data, got.EOF)
	}
}

func TestSessionFileRefusals(t *testing.T) {
	srv, root := fileServer(t)
	cases := []struct {
		name, path, code string
	}{
		{"symlink out of the root", "link.txt", CodeForbidden},
		{".env", ".env", CodeForbidden},
		{".git element", "sub/.git/config", CodeForbidden},
		{"absolute .git element", filepath.Join(root, "sub", ".git", "config"), CodeForbidden},
		{"pem", "key.pem", CodeForbidden},
		{"missing", "missing.txt", CodeNotFound},
		{"directory", "dir", CodeInvalidParams},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := fileCall(t, srv, "", map[string]any{"session": fileSess, "path": c.path})
			wantCode(t, resp, c.code)
			if resp.Result != nil {
				t.Fatalf("refusal carried a result: %+v", resp.Result)
			}
		})
	}
}

func TestSessionFileNoRootsIsUnavailable(t *testing.T) {
	srv, _ := fileServer(t)
	resp := fileCall(t, srv, "", map[string]any{"session": fileBare, "path": "/etc/hostname"})
	wantCode(t, resp, CodeUnavailable)
}

func TestSessionFileScope(t *testing.T) {
	srv, _ := fileServer(t)

	t.Run("outside the caller's tree", func(t *testing.T) {
		resp := fileCall(t, srv, fileOther, map[string]any{"session": fileSess, "path": "notes.md"})
		wantCode(t, resp, CodeSessionScope)
	})

	t.Run("own session", func(t *testing.T) {
		var got fileReply
		decodeTrResult(t, fileCall(t, srv, fileSess, map[string]any{"session": fileSess, "path": "notes.md"}), &got)
		if got.Data != "# notes\nhello\n" {
			t.Fatalf("own read: data %q", got.Data)
		}
	})
}
