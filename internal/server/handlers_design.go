package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/artifact"
	"github.com/bouwerp/aiman/internal/design"
	"github.com/bouwerp/aiman/internal/domain"
)

// SetDesignPublisher attaches the design publisher. Without it design.publish
// fails.
func (s *Server) SetDesignPublisher(p *design.Publisher) {
	s.designer = p
}

// DesignPublishTimeout bounds one design.publish: clone, fetch, push and the
// Pages lookups. CallRaw waits a little longer so it reads the answer.
const DesignPublishTimeout = 3 * time.Minute

func (s *Server) handleDesignPublish(ctx context.Context, req Request) Response {
	ctx, cancel := context.WithTimeout(ctx, DesignPublishTimeout)
	defer cancel()
	if s.designer == nil {
		return errResp(req.ID, CodeInvalidParams, "design publishing is not available")
	}
	var p struct {
		Session string `json:"session"`
		File    string `json:"file"`
		Key     string `json:"key"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	target := strings.TrimSpace(p.Session)
	if target == "" {
		target = req.Caller
	}
	if target == "" || strings.TrimSpace(p.File) == "" {
		return errResp(req.ID, CodeInvalidParams, "session and file are required")
	}
	sess, ok := s.findSession(ctx, target)
	if !ok {
		return errResp(req.ID, CodeNotFound, "session not found")
	}
	wd := sessionDir(sess)
	if wd == "" {
		return errResp(req.ID, CodeInvalidParams, "session has no working directory")
	}
	md, err := readConfined(sess, wd, p.File)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	key := strings.TrimSpace(p.Key)
	if key == "" {
		key = design.Key(sess.IssueKey, sess.Branch)
		if sess.ReviewPR != "" {
			key = reviewBriefKey(sess.ReviewPR)
		}
	}
	var res design.Result
	if !s.designer.HasRepo() && s.artifacts != nil {
		var resp *Response
		res, resp = s.publishDesignArtifact(ctx, req.ID, sess, md, repoBase(sess.RepoName), key)
		if resp != nil {
			return *resp
		}
	} else {
		res, err = s.designer.PublishBytes(ctx, md, gitDirOf(wd), repoBase(sess.RepoName), key)
		if errors.Is(err, design.ErrInvalid) {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
		if err != nil {
			return errResp(req.ID, CodePublishFailed, err.Error())
		}
	}
	if sess.ReviewPR != "" {
		if err := s.addReviewGate(sess.ID); err != nil {
			return errResp(req.ID, CodeCreateFailed, err.Error())
		}
	}
	accepted, rejected, deferred := design.CountTriage(string(md))
	s.appendEvent(ctx, sess.ID, domain.EventActorAiman, domain.EventDesignPublished, map[string]any{
		"revision": res.Revision, "url": res.URL,
		"accepted": accepted, "rejected": rejected, "deferred": deferred,
	})
	return Response{ID: req.ID, Result: map[string]any{"type": "design", "design": res}}
}

// publishDesignArtifact publishes a design page as an artifact when no docs
// repository is configured. A later revision of the same key goes to the
// artifact an earlier one made, when the session may write it, so the link
// stays the same.
func (s *Server) publishDesignArtifact(ctx context.Context, id string, sess domain.Session, md []byte, repoName, key string) (design.Result, *Response) {
	if err := design.CheckNames(repoName, key); err != nil {
		r := errResp(id, CodeInvalidParams, err.Error())
		return design.Result{}, &r
	}
	title := design.ArtifactTitle(repoName, key)
	prev := ""
	recs, err := s.artifacts.Store.List(ctx)
	if err != nil {
		r := errResp(id, CodePublishFailed, err.Error())
		return design.Result{}, &r
	}
	for _, rec := range recs {
		if rec.Title == title && s.ArtifactCanWrite(rec.Session, sess.ID) {
			prev = rec.ID
			break
		}
	}
	pub, err := s.artifacts.Publish(ctx, artifact.PublishRequest{
		ID: prev, Title: title, Session: sess.ID,
		Files: []artifact.File{{Path: "index.html", Data: design.ArtifactPage(md, title)}},
	})
	if err != nil {
		r := artifactErr(id, err)
		return design.Result{}, &r
	}
	s.appendEvent(ctx, sess.ID, domain.EventActorAiman, domain.EventArtifactPublished, map[string]any{
		"id": pub.ID, "version": pub.Version, "files": 1 + len(s.artifacts.Kit), "bytes": len(md),
	})
	return design.Result{Path: repoName + "/" + key + ".md", URL: pub.URL, Revision: pub.Version, Artifact: pub.ID}, nil
}

func sessionDir(s domain.Session) string {
	if s.WorkingDirectory != "" {
		return s.WorkingDirectory
	}
	return s.WorktreePath
}

// repoBase is the repository part of an owner/repo slug.
func repoBase(slug string) string {
	if i := strings.LastIndex(slug, "/"); i >= 0 {
		slug = slug[i+1:]
	}
	if slug == "" {
		return "repo"
	}
	return slug
}

// gitDirOf finds the git directory of the checkout containing dir by reading
// .git (a directory, or a "gitdir:" file in a worktree). Empty when none.
func gitDirOf(dir string) string {
	for d := dir; ; d = filepath.Dir(d) {
		dot := filepath.Join(d, ".git")
		if fi, err := os.Stat(dot); err == nil {
			if fi.IsDir() {
				return dot
			}
			b, err := os.ReadFile(dot)
			if err != nil {
				return ""
			}
			if g, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir:"); ok {
				g = strings.TrimSpace(g)
				if !filepath.IsAbs(g) {
					g = filepath.Join(d, g)
				}
				return g
			}
			return ""
		}
		if parent := filepath.Dir(d); parent == d {
			return ""
		}
	}
}

// readConfined reads file (relative to wd) after resolving symlinks, and only
// when it is a .md file inside the session's working directory or worktree,
// so an agent cannot publish files such as ~/.aiman/config.yaml.
func readConfined(sess domain.Session, wd, file string) ([]byte, error) {
	src := file
	if !filepath.IsAbs(src) {
		src = filepath.Join(wd, src)
	}
	real, err := filepath.EvalSymlinks(src)
	if err != nil {
		return nil, fmt.Errorf("markdown file: %w", err)
	}
	if !isMarkdown(src) || !isMarkdown(real) {
		return nil, errors.New("markdown file must be a .md file")
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
			b, err := os.ReadFile(real)
			if err != nil {
				return nil, fmt.Errorf("read markdown file: %w", err)
			}
			return b, nil
		}
	}
	return nil, errors.New("markdown file must be inside the session's working directory")
}

func isMarkdown(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".md")
}
