package server

import (
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/review"
)

func TestCoverageNoMethod(t *testing.T) {
	r := newReviewRig(t)
	r.srv.SetReviewRunner(review.Runner{Docker: (&fakeDocker{out: "ok\n"}).run, UID: 1000, GID: 1000})
	r.srv.SetReviewConfig(config.ReviewConfig{}, map[string]config.RepoBootstrap{
		"o/r": {Review: config.RepoReview{Image: "node:lts"}},
	})
	info := createReview(t, r.srv)

	resp := rpc(t, r.srv, info.ID, "review.coverage", map[string]any{"session": info.ID})
	if resp.Error == nil || resp.Error.Code != CodeReviewNoCoverage {
		t.Fatalf("want %s, got %+v", CodeReviewNoCoverage, resp.Error)
	}
}

func TestCoverageNoLanguage(t *testing.T) {
	r := newReviewRig(t)
	r.srv.SetReviewRunner(review.Runner{Docker: (&fakeDocker{out: "ok\n"}).run, UID: 1000, GID: 1000})
	info := createReview(t, r.srv)

	resp := rpc(t, r.srv, info.ID, "review.coverage", map[string]any{"session": info.ID})
	if resp.Error == nil || resp.Error.Code != CodeReviewNoCoverage {
		t.Fatalf("want %s, got %+v", CodeReviewNoCoverage, resp.Error)
	}
}

func TestCallDeadlineReviewCoverage(t *testing.T) {
	if got := callDeadline("review.coverage"); got != ReviewRunTimeout+15*time.Second {
		t.Fatalf("review.coverage deadline %v, want %v", got, ReviewRunTimeout+15*time.Second)
	}
}
