package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestSaveAndGetRole(t *testing.T) {
	r := newCIRepo(t)
	ctx := context.Background()
	s := &domain.Session{ID: "rev", Role: domain.SessionRoleReviewer, Status: domain.SessionStatusActive, CreatedAt: time.Now()}
	if err := r.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, "rev")
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != domain.SessionRoleReviewer {
		t.Fatalf("role=%q", got.Role)
	}
}

func TestSavePreservesRoleWhenEmpty(t *testing.T) {
	r := newCIRepo(t)
	ctx := context.Background()
	s := &domain.Session{ID: "rev", Role: domain.SessionRoleReviewer, Status: domain.SessionStatusActive, CreatedAt: time.Now()}
	if err := r.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	s.Role = ""
	if err := r.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, "rev")
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != domain.SessionRoleReviewer {
		t.Fatalf("a blank save wiped the role: %q", got.Role)
	}
}

func TestIssueTypeSurvivesBlankSave(t *testing.T) {
	r := newCIRepo(t)
	ctx := context.Background()
	s := &domain.Session{ID: "s1", IssueKey: "PROJ-1", IssueType: "Bug", Status: domain.SessionStatusActive, CreatedAt: time.Now()}
	if err := r.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	s.IssueType = ""
	if err := r.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.IssueType != "Bug" {
		t.Fatalf("issue_type=%q", got.IssueType)
	}
}
