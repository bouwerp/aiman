package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestReviewPRRoundTripSurvivesBlankSave(t *testing.T) {
	r := newCIRepo(t)
	ctx := context.Background()
	s := &domain.Session{ID: "s1", ReviewPR: "o/r#7", Status: domain.SessionStatusActive, CreatedAt: time.Now()}
	if err := r.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	s.ReviewPR = ""
	if err := r.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ReviewPR != "o/r#7" {
		t.Fatalf("review_pr=%q", got.ReviewPR)
	}
}
