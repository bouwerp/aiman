package server

import (
	"context"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

const (
	// RepoListTimeout bounds one refresh of repo.list: one gh call per owner.
	RepoListTimeout = 45 * time.Second
	repoListTTL     = 5 * time.Minute
	// repoListRetry spaces refreshes while they fail, so a host with a broken
	// gh does not make every picker wait out a timeout.
	repoListRetry = time.Minute
)

// RepoLister lists the repositories the git config lets this host see.
type RepoLister interface {
	ListRepos(ctx context.Context) ([]domain.Repo, error)
}

// SetRepoLister attaches the lister behind repo.list.
func (s *Server) SetRepoLister(l RepoLister) {
	s.repos.lister = l
}

// repoCache keeps the last good list. One refresh runs at a time; a caller
// that waited behind it reads what it fetched.
type repoCache struct {
	lister RepoLister
	now    func() time.Time

	mu      sync.Mutex
	list    []RepoInfo
	loaded  bool
	nextTry time.Time
}

// RepoInfo is one repo.list entry. LastActivityAt is RFC3339, or empty when
// unknown.
type RepoInfo struct {
	Slug           string `json:"slug"`
	LastActivityAt string `json:"last_activity_at"`
}

func (c *repoCache) get(ctx context.Context) ([]RepoInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	if c.loaded && now().Before(c.nextTry) {
		return c.list, nil
	}
	ctx, cancel := context.WithTimeout(ctx, RepoListTimeout)
	defer cancel()
	repos, err := c.lister.ListRepos(ctx)
	if err != nil {
		c.nextTry = now().Add(repoListRetry)
		if c.loaded {
			return c.list, nil
		}
		return nil, err
	}
	c.list = make([]RepoInfo, 0, len(repos))
	for _, r := range repos {
		info := RepoInfo{Slug: r.Name}
		if !r.LastActivityAt.IsZero() {
			info.LastActivityAt = r.LastActivityAt.UTC().Format(time.RFC3339)
		}
		c.list = append(c.list, info)
	}
	c.loaded = true
	c.nextTry = now().Add(repoListTTL)
	return c.list, nil
}

func (s *Server) handleRepoList(ctx context.Context, req Request) Response {
	if s.repos.lister == nil {
		return errResp(req.ID, CodeUnavailable, "repository listing is not available")
	}
	list, err := s.repos.get(ctx)
	if err != nil {
		return errResp(req.ID, CodeUnavailable, err.Error())
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "repos", "repos": list}}
}
