package usecase

import (
	"context"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// A repository's own checkout is listed by `git worktree list` but is not an
// orphan worktree. Surfacing it invented one session per repository on the
// remote, which buried the real sessions.
func TestDiscoverSkipsMainWorktrees(t *testing.T) {
	remote := &batchRemote{
		wtRecords: []domain.WorktreeRecord{
			// Main checkout: repo path == worktree path.
			{RepoPath: "/repos/app", WorktreePath: "/repos/app", State: domain.WorktreeOK},
			// Trailing-slash and dot forms must normalise to the same thing.
			{RepoPath: "/repos/api/", WorktreePath: "/repos/api", State: domain.WorktreeOK},
			{RepoPath: "/repos/web", WorktreePath: "/repos/web/.", State: domain.WorktreeOK},
			// A genuine linked worktree.
			{RepoPath: "/repos/app", WorktreePath: "/repos/app@feature", State: domain.WorktreeOK},
		},
	}

	sessions, err := NewSessionDiscoverer(remote, &recordingSyncEngine{}).Discover(context.Background(), "regent0")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected only the linked worktree, got %d: %+v", len(sessions), sessions)
	}
	if sessions[0].TmuxSession != "app@feature" {
		t.Errorf("TmuxSession = %q, want app@feature", sessions[0].TmuxSession)
	}
}

func TestIsMainWorktree(t *testing.T) {
	tests := []struct {
		repo, worktree string
		want           bool
	}{
		{"/repos/app", "/repos/app", true},
		{"/repos/app/", "/repos/app", true},
		{"/repos/app", "/repos/app/", true},
		{"/repos/app", "/repos/app/.", true},
		{"/repos/app", "/repos/app@feature", false},
		{"/repos/app", "/repos/app/.claude/worktrees/agent-1", false},
		{"/repos/app", "/repos/other", false},
	}
	for _, tt := range tests {
		got := isMainWorktree(domain.WorktreeRecord{RepoPath: tt.repo, WorktreePath: tt.worktree})
		if got != tt.want {
			t.Errorf("isMainWorktree(%q, %q) = %v, want %v", tt.repo, tt.worktree, got, tt.want)
		}
	}
}

// An orphan worktree with no aiman-id used to get a fresh UUID on every scan,
// so each launch saved a duplicate row for the same directory.
func TestDiscoverGivesUnlabelledWorktreesStableIDs(t *testing.T) {
	records := []domain.WorktreeRecord{
		{RepoPath: "/repos/app", WorktreePath: "/repos/app@one", State: domain.WorktreeOK},
		{RepoPath: "/repos/app", WorktreePath: "/repos/app@two", State: domain.WorktreeOK},
	}

	idsFor := func() map[string]string {
		remote := &batchRemote{wtRecords: records}
		sessions, err := NewSessionDiscoverer(remote, &recordingSyncEngine{}).Discover(context.Background(), "regent0")
		if err != nil {
			t.Fatal(err)
		}
		out := make(map[string]string, len(sessions))
		for _, s := range sessions {
			out[s.WorktreePath] = s.ID
		}
		return out
	}

	first, second := idsFor(), idsFor()
	if len(first) != 2 {
		t.Fatalf("expected 2 sessions, got %v", first)
	}
	for path, id := range first {
		if second[path] != id {
			t.Errorf("id for %s changed between scans: %q then %q", path, id, second[path])
		}
		if id == "" {
			t.Errorf("no id assigned for %s", path)
		}
	}
	// Distinct worktrees must not collapse onto one id.
	if first["/repos/app@one"] == first["/repos/app@two"] {
		t.Error("different worktrees were given the same id")
	}
}

// An id read from the remote always wins over a derived one.
func TestDiscoverPrefersRemoteAimanID(t *testing.T) {
	remote := &batchRemote{
		wtRecords: []domain.WorktreeRecord{
			{RepoPath: "/repos/app", WorktreePath: "/repos/app@feature", State: domain.WorktreeOK, AimanID: "real-id"},
		},
	}
	sessions, err := NewSessionDiscoverer(remote, &recordingSyncEngine{}).Discover(context.Background(), "regent0")
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].ID != "real-id" {
		t.Fatalf("expected the remote's aiman-id to be used, got %+v", sessions)
	}
}

func TestStableSessionIDIsDeterministicAndDistinct(t *testing.T) {
	a := stableSessionID("host1", "/repos/app@one")
	if a != stableSessionID("host1", "/repos/app@one") {
		t.Error("not deterministic for the same inputs")
	}
	if a == stableSessionID("host1", "/repos/app@two") {
		t.Error("different worktrees collided")
	}
	if a == stableSessionID("host2", "/repos/app@one") {
		t.Error("the same path on different hosts collided")
	}
	if a == "" {
		t.Error("empty id")
	}
}
