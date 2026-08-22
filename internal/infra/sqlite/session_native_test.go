package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestSaveAndGetAgentSessionID(t *testing.T) {
	repo, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "s1", AgentSessionID: "native-1", AgentSessionPath: "/tmp/a.jsonl",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentSessionID != "native-1" || got.AgentSessionPath != "/tmp/a.jsonl" {
		t.Fatalf("%+v", got)
	}
}

func TestSavePreservesAgentSessionIDWhenEmpty(t *testing.T) {
	repo, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	ctx := context.Background()
	if err := repo.Save(ctx, &domain.Session{
		ID: "s2", AgentSessionID: "keep-me", AgentName: "claude",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, &domain.Session{
		ID: "s2", AgentSessionID: "", AgentName: "",
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, "s2")
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentSessionID != "keep-me" {
		t.Fatalf("COALESCE failed: %q", got.AgentSessionID)
	}
	if got.AgentName != "claude" {
		t.Fatalf("agent name COALESCE failed: %q", got.AgentName)
	}
}

func TestSaveAndGetHookState(t *testing.T) {
	repo, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Second)
	if err := repo.Save(ctx, &domain.Session{
		ID: "s3", AgentTitle: "fix auth", AgentEnded: true,
		HookState: domain.AgentStateWaitingInput, HookStateMessage: "git push",
		HookStateSource: "lifecycle", HookStateSeq: 3, HookStateAt: at,
		Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, "s3")
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentTitle != "fix auth" || !got.AgentEnded || got.HookState != domain.AgentStateWaitingInput {
		t.Fatalf("%+v", got)
	}
	if got.HookStateMessage != "git push" || got.HookStateSeq != 3 {
		t.Fatalf("%+v", got)
	}
}
