package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestSaveKeepsLaunchModelWhenEmpty(t *testing.T) {
	repo, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "l1", AgentName: "pi", LaunchModel: "openrouter/qwen/qwen3-coder", LaunchEffort: "high",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, &domain.Session{ID: "l1", Status: domain.SessionStatusActive, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, "l1")
	if err != nil {
		t.Fatal(err)
	}
	if got.LaunchModel != "openrouter/qwen/qwen3-coder" || got.LaunchEffort != "high" {
		t.Fatalf("launch = %q/%q after an empty save", got.LaunchModel, got.LaunchEffort)
	}

	if err := repo.ClearNativeAgentIdentity(ctx, "l1"); err != nil {
		t.Fatal(err)
	}
	got, err = repo.Get(ctx, "l1")
	if err != nil {
		t.Fatal(err)
	}
	if got.LaunchModel != "" || got.LaunchEffort != "" {
		t.Fatalf("agent switch kept launch %q/%q", got.LaunchModel, got.LaunchEffort)
	}
}

// Tier rides with the launch model: an empty discovery save keeps it, and an
// agent switch clears it.
func TestSaveKeepsTierAndAgentSwitchClearsIt(t *testing.T) {
	repo, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "t1", AgentName: "pi", LaunchModel: "kimi-k3", Tier: "smart",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, &domain.Session{ID: "t1", Status: domain.SessionStatusActive, CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != "smart" {
		t.Fatalf("tier = %q after an empty save, want smart", got.Tier)
	}
	if err := repo.ClearLaunchModel(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	got, err = repo.Get(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tier != "" {
		t.Fatalf("tier = %q after an agent switch, want empty", got.Tier)
	}
}
