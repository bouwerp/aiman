package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/bouwerp/aiman/internal/domain"
)

const (
	// FileMaxReplyBytes caps the data one session.file reply carries.
	FileMaxReplyBytes = 256 << 10
	// FileDefaultReplyBytes is the page size when the call names no limit.
	FileDefaultReplyBytes = 64 << 10
	// fileSniffBytes is how much of a file is searched for a NUL to call it
	// binary.
	fileSniffBytes = 8 << 10
)

type fileResult struct {
	Type    string `json:"type"`
	Session string `json:"session"`
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	Offset  int64  `json:"offset"`
	Data    string `json:"data"`
	EOF     bool   `json:"eof"`
	Binary  bool   `json:"binary"`
}

// handleSessionFile reads a page of a file inside the session's worktree or
// working directory for the phone's file viewer.
func (s *Server) handleSessionFile(ctx context.Context, req Request) Response {
	var params struct {
		Session string `json:"session"`
		Path    string `json:"path"`
		Offset  int64  `json:"offset"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	sess, fail, ok := s.resolveScopedSession(ctx, req, params.Session)
	if !ok {
		return fail
	}
	if strings.TrimSpace(params.Path) == "" {
		return errResp(req.ID, CodeInvalidParams, "path is required")
	}
	if params.Offset < 0 || params.Limit < 0 {
		return errResp(req.ID, CodeInvalidParams, "offset and limit must not be negative")
	}
	limit := params.Limit
	if limit == 0 {
		limit = FileDefaultReplyBytes
	}
	limit = min(limit, FileMaxReplyBytes)

	path, code, msg := confineSessionFile(sess, params.Path)
	if code != "" {
		return errResp(req.ID, code, msg)
	}
	out := fileResult{Type: "file", Session: sess.ID, Path: path}
	if code, msg := out.read(path, params.Offset, limit); code != "" {
		return errResp(req.ID, code, msg)
	}
	return Response{ID: req.ID, Result: out}
}

// fileRoots returns the session's worktree and working directory after
// symlinks, skipping empty and unresolvable ones, and the base a relative
// path resolves against: the working directory, else the worktree.
func fileRoots(sess domain.Session) (roots []string, base string) {
	for _, dir := range []string{sess.WorkingDirectory, sess.WorktreePath} {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		r, err := filepath.EvalSymlinks(dir)
		if err != nil {
			continue
		}
		if base == "" {
			base = r
		}
		roots = append(roots, r)
	}
	return roots, base
}

// confineSessionFile resolves a requested path and answers it only inside one
// of the session's roots, never under a .git directory and never for a base
// name that looks like a secret. Both the requested and the resolved path are
// checked, so a symlink named like a secret or one pointing at a secret is
// refused alike. A missing path is judged by where it would resolve, so a
// path outside the roots is forbidden whether or not it exists.
func confineSessionFile(sess domain.Session, requested string) (path, code, msg string) {
	roots, base := fileRoots(sess)
	if len(roots) == 0 {
		return "", CodeUnavailable, "session has no worktree or working directory"
	}
	abs := filepath.Clean(requested)
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(base, abs)
	}
	if forbiddenFilePath(abs) {
		return "", CodeForbidden, "path is not served"
	}
	resolved, exists, err := resolveExisting(abs)
	if err != nil {
		return "", CodeForbidden, "path is not in this session's worktree"
	}
	inside := false
	for _, root := range roots {
		if resolved == root || within(root, resolved) {
			inside = true
			if rel, rerr := filepath.Rel(root, resolved); rerr == nil && forbiddenFilePath(rel) {
				return "", CodeForbidden, "path is not served"
			}
		}
	}
	if !inside {
		return "", CodeForbidden, "path is not in this session's worktree"
	}
	if forbiddenFilePath(filepath.Base(resolved)) {
		return "", CodeForbidden, "path is not served"
	}
	if !exists {
		return "", CodeNotFound, "no such file"
	}
	return resolved, "", ""
}

// forbiddenFilePath reports a path with a .git element or a base name that
// looks like a secret.
func forbiddenFilePath(path string) bool {
	for _, el := range strings.Split(filepath.ToSlash(path), "/") {
		if el == ".git" {
			return true
		}
	}
	return secretFileName(filepath.Base(path))
}

// secretFileName matches .env, .env.*, *.pem, *.key, id_rsa*, id_ed25519* and
// credentials.
func secretFileName(name string) bool {
	switch {
	case name == ".env", strings.HasPrefix(name, ".env."),
		strings.HasSuffix(name, ".pem"), strings.HasSuffix(name, ".key"),
		strings.HasPrefix(name, "id_rsa"), strings.HasPrefix(name, "id_ed25519"),
		name == "credentials":
		return true
	}
	return false
}

// read fills one page of a confined file. The path is checked with Lstat
// before opening and the opened file must be that same regular file, so a
// symlink or FIFO swapped in after confinement is refused.
func (o *fileResult) read(path string, offset int64, limit int) (code, msg string) {
	before, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return CodeNotFound, "no such file"
		}
		return CodeUnavailable, err.Error()
	}
	if before.IsDir() {
		return CodeInvalidParams, "path is a directory"
	}
	if !before.Mode().IsRegular() {
		return CodeInvalidParams, "path is not a regular file"
	}
	f, err := os.Open(path) //nolint:gosec // G304: confined to the session's roots by confineSessionFile
	if err != nil {
		return CodeUnavailable, err.Error()
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return CodeUnavailable, err.Error()
	}
	if !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return CodeUnavailable, "file changed while opening"
	}
	o.Size = info.Size()

	sniff := make([]byte, min(o.Size, fileSniffBytes))
	n, err := f.ReadAt(sniff, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return CodeUnavailable, err.Error()
	}
	if bytes.IndexByte(sniff[:n], 0) >= 0 {
		o.Binary, o.EOF = true, true
		return "", ""
	}

	o.Offset = min(offset, o.Size)
	// Three more bytes than the limit let a page that would end inside a rune
	// keep the whole rune when nothing else fits.
	buf := make([]byte, min(int64(limit)+utf8.UTFMax-1, o.Size-o.Offset))
	got, err := f.ReadAt(buf, o.Offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return CodeUnavailable, err.Error()
	}
	page := utf8Page(buf[:got], limit)
	o.Data = string(page)
	o.EOF = o.Offset+int64(len(page)) >= o.Size
	return "", ""
}

// utf8Page cuts data to at most limit bytes on a UTF-8 boundary so pages
// concatenate. data holds up to utf8.UTFMax-1 bytes past the limit, or stops
// at the end of the file when it is no longer than limit. A rune split by the
// cut moves to the next page; when that would leave the page empty the rune is
// kept whole instead, so a tiny limit still advances.
func utf8Page(data []byte, limit int) []byte {
	if len(data) <= limit || utf8.RuneStart(data[limit]) {
		return data[:min(limit, len(data))]
	}
	for start := limit - 1; start >= 0 && limit-start < utf8.UTFMax; start-- {
		if utf8.RuneStart(data[start]) {
			if start > 0 {
				return data[:start]
			}
			_, size := utf8.DecodeRune(data)
			return data[:max(size, 1)]
		}
	}
	// Not valid UTF-8 near the cut: cut at the limit.
	return data[:limit]
}
