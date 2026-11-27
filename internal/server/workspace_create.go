package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/usecase"
)

// Workspace sessions span 2 to maxWorkspaceRepos repositories.
const (
	minWorkspaceRepos = 2
	maxWorkspaceRepos = 8
)

// repoParam is one entry of session.create's repos.
type repoParam struct {
	Repo string `json:"repo"`
	Base string `json:"base"`
}

// SessionRepoInfo is one member of a workspace session on the wire.
type SessionRepoInfo struct {
	Repo         string `json:"repo"`
	Branch       string `json:"branch,omitempty"`
	BaseBranch   string `json:"base_branch,omitempty"`
	WorktreePath string `json:"worktree_path,omitempty"`
}

func repoInfos(members []domain.SessionRepo) []SessionRepoInfo {
	if len(members) == 0 {
		return nil
	}
	out := make([]SessionRepoInfo, 0, len(members))
	for _, m := range members {
		out = append(out, SessionRepoInfo{Repo: m.Repo, Branch: m.Branch, BaseBranch: m.BaseBranch, WorktreePath: m.WorktreePath})
	}
	return out
}

// validRepoSlug accepts an owner/name slug that cannot be read as an option.
func validRepoSlug(slug string) error {
	owner, name, ok := strings.Cut(slug, "/")
	switch {
	case slug == "":
		return errors.New("repo is empty")
	case !ok || owner == "" || name == "" || strings.Contains(name, "/"):
		return fmt.Errorf("repo %q is not owner/name", slug)
	case strings.HasPrefix(slug, "-"):
		return fmt.Errorf("repo %q starts with '-'", slug)
	case owner == "." || owner == ".." || name == "." || name == "..":
		return fmt.Errorf("repo %q is not owner/name", slug)
	}
	for _, r := range slug {
		if r <= ' ' || r == 0x7f || strings.ContainsRune("\\:~^?*[", r) {
			return fmt.Errorf("repo %q contains the forbidden character %q", slug, r)
		}
	}
	return nil
}

// workspaceSpecs validates session.create's repos and returns them in order.
// No repos is (nil, nil).
func (p createParams) workspaceSpecs() ([]domain.RepoSpec, error) {
	if len(p.Repos) == 0 {
		return nil, nil
	}
	switch {
	case strings.TrimSpace(p.Repo) != "":
		return nil, errors.New("repos and repo are exclusive")
	case p.Quick:
		return nil, errors.New("repos and quick are exclusive")
	case strings.TrimSpace(p.Review) != "":
		return nil, errors.New("repos and review are exclusive")
	case p.Consult:
		return nil, errors.New("repos and consult are exclusive")
	case p.ExistingBranch:
		return nil, errors.New("repos and existing_branch are exclusive")
	case strings.TrimSpace(p.Dir) != "":
		return nil, errors.New("repos and dir are exclusive")
	case len(p.Repos) < minWorkspaceRepos || len(p.Repos) > maxWorkspaceRepos:
		return nil, fmt.Errorf("repos takes %d to %d repositories, got %d", minWorkspaceRepos, maxWorkspaceRepos, len(p.Repos))
	}
	seen := map[string]bool{}
	specs := make([]domain.RepoSpec, 0, len(p.Repos))
	for _, r := range p.Repos {
		slug := strings.TrimSpace(r.Repo)
		if err := validRepoSlug(slug); err != nil {
			return nil, err
		}
		key := strings.ToLower(slug)
		if seen[key] {
			return nil, fmt.Errorf("repo %s is listed twice", slug)
		}
		seen[key] = true
		base := strings.TrimSpace(r.Base)
		if base != "" {
			if err := domain.ValidBranchName(base); err != nil {
				return nil, fmt.Errorf("base of %s: %w", slug, err)
			}
		}
		specs = append(specs, domain.RepoSpec{Repo: slug, Base: base})
	}
	return specs, nil
}

// sessionReposSetter is a repository that stores workspace members.
type sessionReposSetter interface {
	SetSessionRepos(ctx context.Context, sessionID string, members []domain.SessionRepo) error
}

// saveMembers stores a workspace session's members. Save never writes them,
// so this runs right after the row's save.
func (s *Server) saveMembers(ctx context.Context, sess *domain.Session) error {
	if len(sess.Members) == 0 || s.repo == nil {
		return nil
	}
	setter, ok := s.repo.(sessionReposSetter)
	if !ok {
		return nil
	}
	if err := setter.SetSessionRepos(ctx, sess.ID, sess.Members); err != nil {
		return err
	}
	s.sessList.invalidate()
	return nil
}

// discardMembers removes each member worktree and its branch of a workspace
// session the create made, then the workspace directory when only aiman's
// scaffolding is left in it.
func (s *Server) discardMembers(ctx context.Context, sess domain.Session) {
	if s.remote == nil {
		return
	}
	for _, m := range sess.Members {
		if m.WorktreePath == "" {
			continue
		}
		if _, err := s.remote.Execute(ctx, discardWorktreeScript(m.WorktreePath, m.Branch)); err != nil {
			log.Printf("session.create: remove member %s of %s: %v", m.Repo, sess.ID, err)
		}
	}
	if sess.WorktreePath != "" {
		if _, err := s.remote.Execute(ctx, usecase.WorkspaceRemoveDirScript(sess.WorktreePath)); err != nil {
			log.Printf("session.create: remove workspace directory of %s: %v", sess.ID, err)
		}
	}
}
