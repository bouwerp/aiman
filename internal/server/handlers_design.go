package server

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/design"
	"github.com/bouwerp/aiman/internal/domain"
)

// SetDesignPublisher attaches the design publisher. Without it design.publish
// fails.
func (s *Server) SetDesignPublisher(p *design.Publisher) {
	s.designer = p
}

func (s *Server) handleDesignPublish(ctx context.Context, req Request) Response {
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
	src := p.File
	if !filepath.IsAbs(src) {
		src = filepath.Join(wd, src)
	}
	key := strings.TrimSpace(p.Key)
	if key == "" {
		key = design.Key(sess.IssueKey, sess.Branch)
	}
	pub := *s.designer
	pub.LocalDir = gitDirOf(wd)
	res, err := pub.Publish(ctx, src, repoBase(sess.RepoName), key)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	md, _ := os.ReadFile(src)
	accepted, rejected, deferred := design.CountTriage(string(md))
	s.appendEvent(ctx, sess.ID, domain.EventActorAiman, domain.EventDesignPublished, map[string]any{
		"revision": res.Revision, "url": res.URL,
		"accepted": accepted, "rejected": rejected, "deferred": deferred,
	})
	return Response{ID: req.ID, Result: map[string]any{"type": "design", "design": res}}
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
