package server

import (
	"context"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/harness"
)

const (
	// HarnessDebugTimeout bounds harness.debug: two control socket calls.
	HarnessDebugTimeout = 15 * time.Second
	// harnessFilesMax caps the paths harness.files lists.
	harnessFilesMax = 5000
	// harnessSourceRoot is where the app container sees the run's tree; a
	// debugger's stack frames name files under it.
	harnessSourceRoot = "/src"
)

// handleHarnessDebug answers harness.debug {run|session, op}. Status is any
// in-scope caller's; take over and release are a person's, so a session
// cannot pull the debugger from a tester.
func (s *Server) handleHarnessDebug(ctx context.Context, req Request, p harnessParams) Response {
	op := strings.TrimSpace(p.Op)
	if op == "" {
		op = harness.DebugStatus
	}
	if op != harness.DebugStatus && s.isSessionCall(ctx, req) {
		return errResp(req.ID, CodeNeedsOperator, "only the operator takes over or releases the debugger")
	}
	run, fail := s.harnessRun(ctx, req, p, false)
	if fail != nil {
		return *fail
	}
	m := s.harness.d.Manager
	m.Touch(run.ID)
	dctx, cancel := context.WithTimeout(ctx, HarnessDebugTimeout)
	defer cancel()
	st, err := m.Debug(dctx, run.ID, op, s.harnessActor(ctx, req))
	if err != nil {
		return harnessErr(req.ID, err, CodeUnavailable)
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "harness_debug", "debug": st}}
}

// harnessSource resolves the run a source call names and its exported tree.
func (s *Server) harnessSource(ctx context.Context, req Request, p harnessParams) (harness.Run, string, *Response) {
	run, fail := s.harnessRun(ctx, req, p, true)
	if fail != nil {
		return run, "", fail
	}
	_, dir, err := s.harness.d.Manager.SourceDir(run.ID)
	if err != nil {
		r := harnessErr(req.ID, err, CodeUnavailable)
		return run, "", &r
	}
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		r := errResp(req.ID, CodeUnavailable, err.Error())
		return run, "", &r
	}
	return run, root, nil
}

// handleHarnessFiles lists the run's exported tree for the console's source
// view: regular files only, never under .git or named like a secret.
func (s *Server) handleHarnessFiles(ctx context.Context, req Request, p harnessParams) Response {
	run, root, fail := s.harnessSource(ctx, req, p)
	if fail != nil {
		return *fail
	}
	files := []string{}
	truncated := false
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable entry is left out of the listing
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || rel == "." {
			return nil //nolint:nilerr // the root itself
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || forbiddenFilePath(rel) {
			return nil
		}
		if len(files) >= harnessFilesMax {
			truncated = true
			return filepath.SkipAll
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return errResp(req.ID, CodeUnavailable, err.Error())
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type": "harness_files", "run": run.ID, "root": harnessSourceRoot, "files": files, "truncated": truncated,
	}}
}

type harnessFileResult struct {
	Type   string `json:"type"`
	Run    string `json:"run"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Offset int64  `json:"offset"`
	Data   string `json:"data"`
	EOF    bool   `json:"eof"`
	Binary bool   `json:"binary"`
}

// handleHarnessFile reads a page of one file of the run's exported tree,
// under session.file's rules. path is relative to the tree, or a container
// path under /src as a debugger's stack frame names it.
func (s *Server) handleHarnessFile(ctx context.Context, req Request, p harnessParams) Response {
	if strings.TrimSpace(p.Path) == "" {
		return errResp(req.ID, CodeInvalidParams, "path is required")
	}
	if p.Offset < 0 || p.Limit < 0 {
		return errResp(req.ID, CodeInvalidParams, "offset and limit must not be negative")
	}
	rel, ok := harnessRelPath(p.Path)
	if !ok {
		return errResp(req.ID, CodeForbidden, "path is not in this run's source tree")
	}
	run, root, fail := s.harnessSource(ctx, req, p)
	if fail != nil {
		return *fail
	}
	full, code, msg := confineFile([]string{root}, root, rel, "this run's source tree")
	if code != "" {
		return errResp(req.ID, code, msg)
	}
	limit := p.Limit
	if limit == 0 {
		limit = FileDefaultReplyBytes
	}
	page := fileResult{}
	if code, msg := page.read(full, p.Offset, min(limit, FileMaxReplyBytes)); code != "" {
		return errResp(req.ID, code, msg)
	}
	shown, err := filepath.Rel(root, full)
	if err != nil {
		return errResp(req.ID, CodeUnavailable, err.Error())
	}
	return Response{ID: req.ID, Result: harnessFileResult{
		Type: "harness_file", Run: run.ID, Path: filepath.ToSlash(shown), Size: page.Size,
		Offset: page.Offset, Data: page.Data, EOF: page.EOF, Binary: page.Binary,
	}}
}

// harnessRelPath turns a requested path into one relative to the tree: a
// container path must lie under /src, and a relative path must not climb out.
func harnessRelPath(p string) (string, bool) {
	p = strings.TrimSpace(filepath.ToSlash(p))
	if path.IsAbs(p) {
		clean := path.Clean(p)
		rest, ok := strings.CutPrefix(clean, harnessSourceRoot+"/")
		if !ok {
			return "", false
		}
		p = rest
	}
	clean := path.Clean(p)
	if clean == ".." || strings.HasPrefix(clean, "../") || clean == "." {
		return "", false
	}
	return filepath.FromSlash(clean), true
}
