package server

import (
	"context"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestSessionRenameLeavesBranchWorktreeAndTmux(t *testing.T) {
	repo := testRepo(t)
	ctx := context.Background()
	orig := &domain.Session{
		ID:           "id-1",
		Name:         "q1",
		Group:        "quick",
		Branch:       "adhoc-20260821-1200",
		WorktreePath: "/home/dev/adhoc-20260821-1200",
		TmuxSession:  "adhoc-20260821-1200",
		Status:       domain.SessionStatusActive,
		CreatedAt:    time.Now(),
	}
	if err := repo.Save(ctx, orig); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = New(ln, repo, nil, nil, nil, "t").Serve(cctx) }()

	resp, err := Call(SocketPath(dir), "session.rename", map[string]any{"id": "q1", "name": "spike"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Fatalf("rename: %+v", resp.Error)
	}
	got, err := repo.Get(ctx, "id-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "spike" {
		t.Fatalf("Name=%q", got.Name)
	}
	if got.Branch != orig.Branch {
		t.Fatalf("Branch changed: %q -> %q", orig.Branch, got.Branch)
	}
	if got.WorktreePath != orig.WorktreePath {
		t.Fatalf("WorktreePath changed: %q -> %q", orig.WorktreePath, got.WorktreePath)
	}
	if got.TmuxSession != orig.TmuxSession {
		t.Fatalf("TmuxSession changed: %q -> %q", orig.TmuxSession, got.TmuxSession)
	}
}
