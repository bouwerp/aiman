package harness

import (
	"context"
	"errors"
	"path/filepath"
	"sync"

	"github.com/bouwerp/aiman/internal/infra/git"
	"github.com/bouwerp/aiman/internal/review"
)

// FakeGitHub serves fixed pull requests and records posted reviews.
type FakeGitHub struct {
	mu    sync.Mutex
	prs   map[string]review.PR
	posts []review.Post
}

// SetPR makes ref answer with pr.
func (g *FakeGitHub) SetPR(ref review.Ref, pr review.PR) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.prs == nil {
		g.prs = map[string]review.PR{}
	}
	g.prs[ref.String()] = pr
}

// ListPRs returns every PR of slug.
func (g *FakeGitHub) ListPRs(_ context.Context, slug string) ([]review.PR, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []review.PR
	for key, pr := range g.prs {
		if ref, err := review.ParseRef(key); err == nil && ref.Slug() == slug {
			out = append(out, pr)
		}
	}
	return out, nil
}

// PR returns the PR set for ref.
func (g *FakeGitHub) PR(_ context.Context, ref review.Ref) (review.PR, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	pr, ok := g.prs[ref.String()]
	if !ok {
		return review.PR{}, errors.New("no such pull request")
	}
	return pr, nil
}

// PostReview records the post.
func (g *FakeGitHub) PostReview(_ context.Context, _ review.Ref, post review.Post) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.posts = append(g.posts, post)
	return nil
}

// Posts returns every review posted.
func (g *FakeGitHub) Posts() []review.Post {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]review.Post(nil), g.posts...)
}

// EnableReview makes review creates work: claude is the installed smart and
// worker tier, PRs come from the returned FakeGitHub, the production git
// manager makes the worktree through the fake remote (which records its
// commands, runs none and reports origin as o/r), and review state and
// scratch directories go under the harness dir.
func (h *Harness) EnableReview() *FakeGitHub {
	h.EnableCoordinator()
	gh := &FakeGitHub{}
	h.srv.SetReviewGitHub(gh)
	h.remote.Reply("remote get-url origin", "git@github.com:o/r.git\n")
	h.srv.SetReviewWorktrees(git.NewManager(nil))
	h.srv.SetReviewDir(h.ReviewDir())
	return gh
}

// ReviewDir is where serve keeps review state files and scratch directories
// in a case.
func (h *Harness) ReviewDir() string {
	return filepath.Join(h.Dir, "pr-reviews")
}
