package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestShouldMergeDiscoveredSession(t *testing.T) {
	dbSessions := map[string]domain.Session{
		"known": {ID: "known"},
	}

	tests := []struct {
		name string
		s    domain.Session
		want bool
	}{
		{
			name: "active session without DB record still merges",
			s: domain.Session{
				ID:     "live",
				Status: domain.SessionStatusActive,
			},
			want: true,
		},
		{
			name: "inactive session with DB record still merges",
			s: domain.Session{
				ID:     "known",
				Status: domain.SessionStatusInactive,
			},
			want: true,
		},
		{
			name: "inactive session without DB record is skipped",
			s: domain.Session{
				ID:     "ghost",
				Status: domain.SessionStatusInactive,
			},
			want: false,
		},
		{
			name: "inactive session without ID is skipped",
			s: domain.Session{
				Status: domain.SessionStatusInactive,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldMergeDiscoveredSession(tt.s, dbSessions); got != tt.want {
				t.Fatalf("shouldMergeDiscoveredSession(%+v) = %v, want %v", tt.s, got, tt.want)
			}
		})
	}
}

func TestOverlayPersistedSessionFieldsKeepsNameAndGroup(t *testing.T) {
	live := domain.Session{
		ID: "s1", Status: domain.SessionStatusActive,
		TmuxSession: "wtb-1", RemoteHost: "regent0",
	}
	stored := domain.Session{
		ID: "s1", Name: "impl", Group: "WTB-1925",
		IssueKey: "WTB-1925", Branch: "wtb-1925-fix",
		RepoName: "org/repo", WorktreePath: "/wt", LocalPath: "/local",
		AgentName: "claude", MutagenSyncID: "sync-1",
	}
	got := overlayPersistedSessionFields(live, stored)
	if got.Name != "impl" || got.Group != "WTB-1925" {
		t.Fatalf("name=%q group=%q", got.Name, got.Group)
	}
	if got.TmuxSession != "wtb-1" || got.RemoteHost != "regent0" {
		t.Fatalf("live identity clobbered: %+v", got)
	}
	if got.IssueKey != "WTB-1925" || got.RepoName != "org/repo" {
		t.Fatalf("other persisted fields dropped: %+v", got)
	}
}
