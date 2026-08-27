package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestSaveAndGetParentID(t *testing.T) {
	repo, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	ctx := context.Background()
	s := &domain.Session{
		ID: "child", ParentID: "parent",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}
	if err := repo.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, "child")
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentID != "parent" {
		t.Fatalf("parent_id=%q", got.ParentID)
	}
}

func TestSavePreservesParentIDWhenEmpty(t *testing.T) {
	repo, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "child", ParentID: "parent",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, &domain.Session{
		ID: "child", ParentID: "",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, "child")
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentID != "parent" {
		t.Fatalf("COALESCE failed: parent_id=%q", got.ParentID)
	}
}
