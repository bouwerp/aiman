package ui

import (
	"errors"
	"slices"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// Ids serve refuses as terminated or forgotten leave the laptop DB and the
// list, so the next stream reconnect does not register them again.
func TestSessionsRegisteredPrunesForgottenRows(t *testing.T) {
	repo := &startupSessionRepo{}
	m := NewModel(testCfg(), nil, nil, repo, nil, nil, nil)
	m.allSessions = []domain.Session{
		{ID: "keep", Name: "keep", RemoteHost: "regent0"},
		{ID: "gone", Name: "gone", RemoteHost: "regent0"},
		{ID: "elsewhere", Name: "elsewhere", RemoteHost: "other"},
	}
	m.Update(sessionsRegisteredMsg{host: "regent0", n: 2, forgotten: []string{"gone", "elsewhere"}})
	if got := sessionIDs(m.allSessions); !slices.Equal(got, []string{"keep", "elsewhere"}) {
		t.Fatalf("allSessions = %v", got)
	}
	if !slices.Equal(repo.deletedID, []string{"gone"}) {
		t.Fatalf("deleted = %v", repo.deletedID)
	}
}

func TestSessionsRegisteredErrorPrunesNothing(t *testing.T) {
	repo := &startupSessionRepo{}
	m := NewModel(testCfg(), nil, nil, repo, nil, nil, nil)
	m.allSessions = []domain.Session{{ID: "a", RemoteHost: "regent0"}}
	m.Update(sessionsRegisteredMsg{host: "regent0", n: 1, err: errors.New("boom")})
	if len(m.allSessions) != 1 || len(repo.deletedID) != 0 {
		t.Fatalf("rows %v deleted %v", sessionIDs(m.allSessions), repo.deletedID)
	}
}

func TestSessionsForHostFiltersHostAndPlaceholders(t *testing.T) {
	in := []domain.Session{
		{ID: "real-a", RemoteHost: "a"},
		{ID: "real-b", RemoteHost: "b"},
		{ID: "pending-x", RemoteHost: "a"},
	}
	got := sessionsForHost(in, "a")
	if len(got) != 1 || got[0].ID != "real-a" {
		t.Fatalf("got %+v, want only real-a", got)
	}
	got[0].ID = "mutated"
	if in[0].ID != "real-a" {
		t.Fatalf("input was mutated: %+v", in[0])
	}
}
