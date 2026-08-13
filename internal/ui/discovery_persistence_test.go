package ui

import (
	"context"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

// savingSessionRepo records what discovery persists.
type savingSessionRepo struct {
	startupSessionRepo
	saved []string
}

func (r *savingSessionRepo) Save(_ context.Context, s *domain.Session) error {
	r.saved = append(r.saved, s.ID)
	r.sessions = append(r.sessions, *s)
	return nil
}

func testCfg() *config.Config {
	return &config.Config{Remotes: []config.Remote{{Host: "regent0"}}}
}

// applyDiscoveryResult used to save every discovered session before applying
// shouldMergeDiscoveredSession. That defeated the filter permanently: it admits
// an inactive session only when the database already knows it, so writing every
// orphan worktree up front meant the next scan found it stored and accepted it
// forever. The session list then filled with every worktree on the remote and
// buried the live ones.
func TestDiscoveryDoesNotPersistUnknownInactiveSessions(t *testing.T) {
	repo := &savingSessionRepo{}
	cfg := testCfg()
	m := NewModel(cfg, nil, nil, repo, nil, nil, nil)

	m.applyDiscoveryResult(discoveryResultMsg{
		scannedHosts: map[string]bool{"regent0": true},
		sessions: []domain.Session{
			{ID: "live", RemoteHost: "regent0", TmuxSession: "WTB-1", Status: domain.SessionStatusActive},
			{ID: "syncing", RemoteHost: "regent0", TmuxSession: "WTB-2", Status: domain.SessionStatusSyncing},
			// An orphan worktree the database has never heard of.
			{ID: "orphan-1", RemoteHost: "regent0", TmuxSession: "app@stray", Status: domain.SessionStatusInactive,
				WorktreePath: "/repos/app@stray"},
			{ID: "orphan-2", RemoteHost: "regent0", TmuxSession: "agent-abc", Status: domain.SessionStatusInactive,
				WorktreePath: "/repos/app/.claude/worktrees/agent-abc"},
		},
	})

	for _, id := range repo.saved {
		if id == "orphan-1" || id == "orphan-2" {
			t.Errorf("unknown inactive session %q was persisted; it will be admitted forever after", id)
		}
	}
	for _, want := range []string{"live", "syncing"} {
		found := false
		for _, id := range repo.saved {
			if id == want {
				found = true
			}
		}
		if !found {
			t.Errorf("expected live session %q to be persisted, saved=%v", want, repo.saved)
		}
	}

	shown := make(map[string]bool)
	for _, s := range m.allSessions {
		shown[s.ID] = true
	}
	if !shown["live"] || !shown["syncing"] {
		t.Errorf("live sessions missing from the list: %v", m.allSessions)
	}
	if shown["orphan-1"] || shown["orphan-2"] {
		t.Errorf("orphan worktrees should not be listed: %v", m.allSessions)
	}
}

// An inactive session the database already knows about is aiman's own, and must
// keep showing — the fix above must not throw those away.
func TestDiscoveryKeepsKnownInactiveSessions(t *testing.T) {
	known := domain.Session{
		ID: "aiman-made", RemoteHost: "regent0", TmuxSession: "app@feature",
		Status: domain.SessionStatusInactive, WorktreePath: "/repos/app@feature",
	}
	repo := &savingSessionRepo{startupSessionRepo: startupSessionRepo{sessions: []domain.Session{known}}}
	cfg := testCfg()

	dbSessions, err := loadConfiguredSessions(context.Background(), cfg, repo)
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(cfg, nil, dbSessions, repo, nil, nil, nil)

	m.applyDiscoveryResult(discoveryResultMsg{
		scannedHosts: map[string]bool{"regent0": true},
		sessions:     []domain.Session{known},
	})

	found := false
	for _, s := range m.allSessions {
		if s.ID == "aiman-made" {
			found = true
		}
	}
	if !found {
		t.Errorf("a known inactive session was dropped: %v", m.allSessions)
	}
}
