package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/artifact"
	"github.com/bouwerp/aiman/internal/domain"
)

// CodeArtifactConflict refuses an artifact write that lost a race or named a
// stale expect_version.
const CodeArtifactConflict = "artifact_conflict"

// ArtifactPublishTimeout bounds one artifact.publish: reading the files and
// up to 64 MiB of R2 PUTs. CallRaw waits a little longer so it reads the
// answer.
const ArtifactPublishTimeout = 3 * time.Minute

// SetArtifactPublisher attaches the artifact publisher. Without it every
// artifact method answers unavailable.
func (s *Server) SetArtifactPublisher(p *artifact.Publisher) {
	s.artifacts = p
}

// ArtifactCanWrite is Publisher.CanWrite for serve: caller may add a version
// to owner's artifact when caller is owner or one of owner's ancestors by row
// ParentID (or the holder's recorded parent).
func (s *Server) ArtifactCanWrite(owner, caller string) bool {
	if owner == "" || caller == "" {
		return owner == caller
	}
	if owner == caller {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	list, err := s.loadSessions(ctx)
	if err != nil {
		return false
	}
	scope := sessionScope{caller: caller, rows: make(map[string]domain.Session, len(list)), pty: s.pty}
	for _, sess := range list {
		scope.rows[sess.ID] = sess
	}
	return scope.descends(owner)
}

func (s *Server) dispatchArtifact(ctx context.Context, req Request) Response {
	switch req.Method {
	case "artifact.publish":
		return s.handleArtifactPublish(ctx, req)
	case "artifact.list":
		return s.handleArtifactList(ctx, req)
	case "artifact.get":
		return s.handleArtifactGet(ctx, req)
	case "artifact.share", "artifact.unshare", "artifact.delete":
		return s.handleArtifactOperator(ctx, req)
	}
	return errResp(req.ID, CodeInvalidParams, "unknown method "+req.Method)
}

func artifactErr(id string, err error) Response {
	switch {
	case errors.Is(err, artifact.ErrConflict):
		return errResp(id, CodeArtifactConflict, err.Error())
	case errors.Is(err, artifact.ErrInvalid), errors.Is(err, artifact.ErrLimits):
		return errResp(id, CodeInvalidParams, err.Error())
	case errors.Is(err, artifact.ErrNotOwner):
		return errResp(id, CodeSessionScope, err.Error())
	case errors.Is(err, artifact.ErrNotFound):
		return errResp(id, CodeNotFound, err.Error())
	}
	return errResp(id, CodePublishFailed, err.Error())
}

func decodeParams(req Request, p any) *Response {
	if len(req.Params) == 0 {
		return nil
	}
	if err := json.Unmarshal(req.Params, p); err != nil {
		r := errResp(req.ID, CodeInvalidParams, err.Error())
		return &r
	}
	return nil
}

var themeRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func (s *Server) handleArtifactPublish(ctx context.Context, req Request) Response {
	if s.artifacts == nil {
		return errResp(req.ID, CodeUnavailable, "artifact publishing is not configured on this host")
	}
	ctx, cancel := context.WithTimeout(ctx, ArtifactPublishTimeout)
	defer cancel()
	var p struct {
		Session string `json:"session"`
		Page    string `json:"page"`
		Files   []struct {
			Path string `json:"path"`
			From string `json:"from"`
		} `json:"files"`
		Title         string `json:"title"`
		ID            string `json:"id"`
		ExpectVersion int    `json:"expect_version"`
		Theme         string `json:"theme"`
	}
	if bad := decodeParams(req, &p); bad != nil {
		return *bad
	}
	scope, isSession := s.sessionScopeOf(ctx, req)
	target := strings.TrimSpace(p.Session)
	if target == "" {
		target = scope.caller
	}
	if target == "" {
		return errResp(req.ID, CodeInvalidParams, "session is required")
	}
	if strings.TrimSpace(p.Page) == "" {
		return errResp(req.ID, CodeInvalidParams, "page is required")
	}
	if p.Theme != "" && !themeRe.MatchString(p.Theme) {
		return errResp(req.ID, CodeInvalidParams, fmt.Sprintf("theme %q is not a theme name", p.Theme))
	}
	sess, ok := s.findSession(ctx, target)
	if !ok {
		return errResp(req.ID, CodeNotFound, "session not found")
	}
	if refused, out := s.refuseOutOfScope(ctx, req, sess.ID); out {
		return refused
	}
	wd := sessionDir(sess)
	if wd == "" {
		return errResp(req.ID, CodeInvalidParams, "session has no working directory")
	}
	pageAbs := p.Page
	if !filepath.IsAbs(pageAbs) {
		pageAbs = filepath.Join(wd, pageAbs)
	}
	html, err := readConfinedAny(sess, wd, p.Page)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, "page: "+err.Error())
	}
	if p.Theme != "" {
		html = withKitTheme(html, p.Theme)
	}
	files := []artifact.File{{Path: "index.html", Data: html}}
	pageDir := filepath.Dir(pageAbs)
	for _, f := range p.Files {
		if strings.TrimSpace(f.From) == "" {
			return errResp(req.ID, CodeInvalidParams, "every file needs from")
		}
		data, err := readConfinedAny(sess, wd, f.From)
		if err != nil {
			return errResp(req.ID, CodeInvalidParams, f.From+": "+err.Error())
		}
		dest := f.Path
		if dest == "" {
			from := f.From
			if !filepath.IsAbs(from) {
				from = filepath.Join(wd, from)
			}
			rel, err := filepath.Rel(pageDir, from)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return errResp(req.ID, CodeInvalidParams, f.From+" is outside the page's directory: give it a path")
			}
			dest = filepath.ToSlash(rel)
		}
		files = append(files, artifact.File{Path: dest, Data: data})
	}
	// A new artifact belongs to the session whose files it holds. A version of
	// an existing one is checked against the caller: "" is the operator.
	owner := ""
	if isSession {
		owner = scope.caller
	}
	if p.ID == "" {
		owner = sess.ID
	}
	res, err := s.artifacts.Publish(ctx, artifact.PublishRequest{
		ID: strings.TrimSpace(p.ID), Title: strings.TrimSpace(p.Title), Session: owner,
		ExpectVersion: p.ExpectVersion, Files: files,
	})
	if err != nil {
		return artifactErr(req.ID, err)
	}
	var total int
	for _, f := range files {
		total += len(f.Data)
	}
	ledger := sess.ID
	if isSession && scope.caller != "" {
		ledger = scope.caller
	}
	actor := domain.EventActorAgent
	if !isSession {
		actor = domain.EventActorUser
	}
	s.appendEvent(ctx, ledger, actor, domain.EventArtifactPublished, map[string]any{
		"id": res.ID, "version": res.Version, "files": len(files) + len(s.artifacts.Kit), "bytes": total,
	})
	return Response{ID: req.ID, Result: map[string]any{"type": "artifact", "artifact": res}}
}

// artifactRow is one entry of artifact.list.
type artifactRow struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	Session string    `json:"session"`
	Latest  int       `json:"latest"`
	URL     string    `json:"url"`
	Shared  bool      `json:"shared"`
	Created time.Time `json:"created"`
}

func (s *Server) artifactRow(r artifact.Record) artifactRow {
	return artifactRow{
		ID: r.ID, Title: r.Title, Session: r.Session, Latest: r.Latest, URL: s.artifacts.URL(r.ID),
		Shared: len(r.Share.Emails)+len(r.Share.Domains) > 0, Created: r.Created,
	}
}

func (s *Server) handleArtifactList(ctx context.Context, req Request) Response {
	if s.artifacts == nil {
		return errResp(req.ID, CodeUnavailable, "artifact publishing is not configured on this host")
	}
	recs, err := s.artifacts.Store.List(ctx)
	if err != nil {
		return errResp(req.ID, CodeUnavailable, err.Error())
	}
	rows := make([]artifactRow, 0, len(recs))
	for _, r := range recs {
		rows = append(rows, s.artifactRow(r))
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "artifacts", "artifacts": rows}}
}

// artifactVersion is one version in artifact.get.
type artifactVersion struct {
	N       int                 `json:"n"`
	Session string              `json:"session"`
	Created time.Time           `json:"created"`
	URL     string              `json:"url"`
	Files   []artifact.FileInfo `json:"files"`
}

func (s *Server) handleArtifactGet(ctx context.Context, req Request) Response {
	if s.artifacts == nil {
		return errResp(req.ID, CodeUnavailable, "artifact publishing is not configured on this host")
	}
	var p struct {
		ID string `json:"id"`
	}
	if bad := decodeParams(req, &p); bad != nil {
		return *bad
	}
	if !artifact.ValidID(p.ID) {
		return errResp(req.ID, CodeInvalidParams, "id is required: 20 lower-case base32 characters")
	}
	rec, err := s.artifacts.Store.Get(ctx, p.ID)
	if err == nil && rec.Deleted {
		err = fmt.Errorf("%w: %s was deleted", artifact.ErrNotFound, p.ID)
	}
	if err != nil {
		return artifactErr(req.ID, err)
	}
	vs, err := s.artifacts.Store.Versions(ctx, p.ID)
	if err != nil {
		return errResp(req.ID, CodeUnavailable, err.Error())
	}
	versions := make([]artifactVersion, 0, len(vs))
	base := s.artifacts.URL(p.ID)
	for _, v := range vs {
		versions = append(versions, artifactVersion{N: v.N, Session: v.Session, Created: v.Created,
			URL: fmt.Sprintf("%sv/%d/", base, v.N), Files: v.Files})
	}
	info := struct {
		artifactRow
		Versions []artifactVersion  `json:"versions"`
		Share    artifact.ShareList `json:"share"`
	}{s.artifactRow(rec), versions, rec.Share}
	return Response{ID: req.ID, Result: map[string]any{"type": "artifact_info", "artifact": info}}
}

// handleArtifactOperator answers share, unshare and delete, which only the
// operator may call.
func (s *Server) handleArtifactOperator(ctx context.Context, req Request) Response {
	if s.isSessionCall(ctx, req) {
		return errResp(req.ID, CodeNeedsOperator, req.Method+" is the operator's")
	}
	if s.artifacts == nil {
		return errResp(req.ID, CodeUnavailable, "artifact publishing is not configured on this host")
	}
	var p struct {
		ID      string   `json:"id"`
		Emails  []string `json:"emails"`
		Domains []string `json:"domains"`
		All     bool     `json:"all"`
	}
	if bad := decodeParams(req, &p); bad != nil {
		return *bad
	}
	if !artifact.ValidID(p.ID) {
		return errResp(req.ID, CodeInvalidParams, "id is required: 20 lower-case base32 characters")
	}
	list := artifact.ShareList{Emails: p.Emails, Domains: p.Domains}
	switch req.Method {
	case "artifact.delete":
		if err := s.artifacts.Delete(ctx, p.ID); err != nil {
			return artifactErr(req.ID, err)
		}
		return Response{ID: req.ID, Result: map[string]any{"type": "artifact_deleted", "id": p.ID}}
	case "artifact.share":
		if len(list.Emails)+len(list.Domains) == 0 {
			return errResp(req.ID, CodeInvalidParams, "emails or domains are required")
		}
		m, err := s.artifacts.Share(ctx, p.ID, list, artifact.ShareList{}, false)
		if err != nil {
			return artifactErr(req.ID, err)
		}
		return Response{ID: req.ID, Result: map[string]any{"type": "artifact_share", "share": m.Share}}
	default: // artifact.unshare
		if !p.All && len(list.Emails)+len(list.Domains) == 0 {
			return errResp(req.ID, CodeInvalidParams, "emails, domains or all is required")
		}
		m, err := s.artifacts.Share(ctx, p.ID, artifact.ShareList{}, list, p.All)
		if err != nil {
			return artifactErr(req.ID, err)
		}
		return Response{ID: req.ID, Result: map[string]any{"type": "artifact_share", "share": m.Share}}
	}
}

// readConfinedAny reads file (relative to wd) after resolving symlinks, only
// when it is a regular file inside the session's working directory or
// worktree. It is readConfined without the .md rule.
func readConfinedAny(sess domain.Session, wd, file string) ([]byte, error) {
	src := file
	if !filepath.IsAbs(src) {
		src = filepath.Join(wd, src)
	}
	real, err := filepath.EvalSymlinks(src)
	if err != nil {
		return nil, err
	}
	for _, root := range []string{wd, sess.WorktreePath} {
		if root == "" {
			continue
		}
		r, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(r, real); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			fi, err := os.Stat(real)
			if err != nil {
				return nil, err
			}
			if !fi.Mode().IsRegular() {
				return nil, errors.New("not a regular file")
			}
			if fi.Size() > artifact.MaxFileBytes {
				return nil, fmt.Errorf("%w: %d bytes, at most %d", artifact.ErrLimits, fi.Size(), artifact.MaxFileBytes)
			}
			return os.ReadFile(real)
		}
	}
	return nil, errors.New("file must be inside the session's working directory")
}

var (
	htmlTagRe   = regexp.MustCompile(`(?i)<html(\s[^>]*)?>`)
	kitThemeRe  = regexp.MustCompile(`(?i)\sdata-kit-theme\s*=`)
	htmlStartRe = regexp.MustCompile(`(?i)<html`)
)

// withKitTheme sets data-kit-theme on the page's <html> tag unless the tag
// already carries one. A page with no <html> tag is left as it is.
func withKitTheme(page []byte, theme string) []byte {
	loc := htmlTagRe.FindIndex(page)
	if loc == nil || kitThemeRe.Match(page[loc[0]:loc[1]]) {
		return page
	}
	at := htmlStartRe.FindIndex(page[loc[0]:])[1] + loc[0]
	var b bytes.Buffer
	b.Grow(len(page) + len(theme) + 20)
	b.Write(page[:at])
	b.WriteString(` data-kit-theme="` + theme + `"`)
	b.Write(page[at:])
	return b.Bytes()
}
