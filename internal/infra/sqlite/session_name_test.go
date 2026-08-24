package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestSaveIgnoresEphemeralSessionID(t *testing.T) {
	repo, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{ID: "pending-1", TmuxSession: "x"}); err != nil {
		t.Fatal(err)
	}
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("ephemeral id persisted: %+v", list)
	}
}

func TestOpenDropsEphemeralSessionIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aiman.db")
	repo, err := NewRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{ID: "keep", TmuxSession: "live"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(`INSERT INTO sessions (id, tmux_session, created_at, updated_at)
		VALUES ('pending-99', 'ghost', datetime('now'), datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	if err := repo.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	list, err := reopened.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "keep" {
		t.Fatalf("got %+v", list)
	}
}

func TestSaveAndGetNameGroup(t *testing.T) {
	repo, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	ctx := context.Background()
	s := &domain.Session{
		ID: "s1", Name: "impl", Group: "WTB-1",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}
	if err := repo.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "impl" || got.Group != "WTB-1" {
		t.Fatalf("got name=%q group=%q", got.Name, got.Group)
	}
}

func TestSavePreservesNameAndGroupWhenEmpty(t *testing.T) {
	repo, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "s2", Name: "reviewer", Group: "quick",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, &domain.Session{
		ID: "s2", Name: "", Group: "",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, "s2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "reviewer" || got.Group != "quick" {
		t.Fatalf("COALESCE failed: name=%q group=%q", got.Name, got.Group)
	}
}

func TestListIncludesNameGroup(t *testing.T) {
	repo, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "s3", Name: "q1", Group: "quick",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	list, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "q1" || list[0].Group != "quick" {
		t.Fatalf("list = %+v", list)
	}
}
