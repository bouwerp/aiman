package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// Acceptance tests for multi-repo sessions, piece P1. Children must not edit
// this file.

func newSessionReposRepo(t *testing.T) *Repository {
	t.Helper()
	r, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func members() []domain.SessionRepo {
	return []domain.SessionRepo{
		{Repo: "bouwerp/aiman", Branch: "host-tab", BaseBranch: "main", WorktreePath: "/r/host-tab@ws/aiman", Ord: 0},
		{Repo: "bouwerp/aiman-phone", Branch: "host-tab", BaseBranch: "develop", WorktreePath: "/r/host-tab@ws/aiman-phone", Ord: 1},
	}
}

func TestSessionReposRoundTrip(t *testing.T) {
	ctx := context.Background()
	r := newSessionReposRepo(t)
	s := &domain.Session{ID: "ws-1", Name: "host-tab", Branch: "host-tab", WorktreePath: "/r/host-tab@ws", WorkingDirectory: "/r/host-tab@ws", Status: domain.SessionStatusActive}
	if err := r.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := r.SetSessionRepos(ctx, "ws-1", members()); err != nil {
		t.Fatal(err)
	}
	plain := &domain.Session{ID: "plain", Name: "p", RepoName: "bouwerp/aiman", Status: domain.SessionStatusActive}
	if err := r.Save(ctx, plain); err != nil {
		t.Fatal(err)
	}

	got, err := r.Get(ctx, "ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsWorkspace() || len(got.Members) != 2 {
		t.Fatalf("Get members = %+v", got.Members)
	}
	if got.Members[0] != members()[0] || got.Members[1] != members()[1] {
		t.Fatalf("Get members = %+v, want %+v (in Ord order)", got.Members, members())
	}

	all, err := r.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		switch s.ID {
		case "ws-1":
			if len(s.Members) != 2 {
				t.Errorf("List ws-1 members = %+v", s.Members)
			}
		case "plain":
			if s.Members != nil || s.IsWorkspace() {
				t.Errorf("List plain members = %+v", s.Members)
			}
		}
	}

	// SetSessionRepos replaces the set.
	if err := r.SetSessionRepos(ctx, "ws-1", members()[1:]); err != nil {
		t.Fatal(err)
	}
	got, _ = r.Get(ctx, "ws-1")
	if len(got.Members) != 1 || got.Members[0].Repo != "bouwerp/aiman-phone" {
		t.Fatalf("after replace members = %+v", got.Members)
	}

	// Delete removes the rows with the session.
	if err := r.Delete(ctx, "ws-1"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := r.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM session_repos WHERE session_id = ?`, "ws-1").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("session_repos rows left after Delete: %d", n)
	}
}

// Discovery, hook reports and session.register save sessions with no
// members; that must never wipe or change the stored set.
func TestSaveNilMembersKeepsRows(t *testing.T) {
	ctx := context.Background()
	r := newSessionReposRepo(t)
	s := &domain.Session{ID: "ws-2", Name: "w", Branch: "b", Status: domain.SessionStatusActive}
	if err := r.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	if err := r.SetSessionRepos(ctx, "ws-2", members()); err != nil {
		t.Fatal(err)
	}
	if err := r.Save(ctx, &domain.Session{ID: "ws-2", Status: domain.SessionStatusInactive}); err != nil {
		t.Fatal(err)
	}
	// Save never writes members, even a non-nil slice.
	if err := r.Save(ctx, &domain.Session{ID: "ws-2", Status: domain.SessionStatusActive, Members: members()[:1]}); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, "ws-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Members) != 2 {
		t.Fatalf("members after Save = %+v, want the 2 stored rows", got.Members)
	}
}
