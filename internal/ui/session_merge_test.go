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
