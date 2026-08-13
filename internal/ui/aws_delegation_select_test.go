package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestFirstSyncingDelegation(t *testing.T) {
	tests := []struct {
		name        string
		remote      config.Remote
		wantProfile string // "" means expect nil
	}{
		{
			name:   "no delegations configured",
			remote: config.Remote{},
		},
		{
			name: "singular form is used",
			remote: config.Remote{
				AWSDelegation: &config.AWSDelegation{Profile: "solo", SyncCredentials: true},
			},
			wantProfile: "solo",
		},
		{
			// Previously the summary screen read only AWSDelegation, so a remote
			// configured with the plural form showed no AWS fields at all.
			name: "plural form is used when the singular is absent",
			remote: config.Remote{
				AWSDelegations: []*config.AWSDelegation{
					{Profile: "dev", SyncCredentials: true},
				},
			},
			wantProfile: "dev",
		},
		{
			name: "entries that do not sync credentials are skipped",
			remote: config.Remote{
				AWSDelegations: []*config.AWSDelegation{
					{Profile: "no-sync", SyncCredentials: false},
					{Profile: "syncs", SyncCredentials: true},
				},
			},
			wantProfile: "syncs",
		},
		{
			name: "all non-syncing yields nothing",
			remote: config.Remote{
				AWSDelegations: []*config.AWSDelegation{
					{Profile: "a", SyncCredentials: false},
					{Profile: "b", SyncCredentials: false},
				},
			},
		},
		{
			name: "singular wins over plural when both sync",
			remote: config.Remote{
				AWSDelegation:  &config.AWSDelegation{Profile: "singular", SyncCredentials: true},
				AWSDelegations: []*config.AWSDelegation{{Profile: "plural", SyncCredentials: true}},
			},
			wantProfile: "singular",
		},
		{
			name: "nil entries are tolerated",
			remote: config.Remote{
				AWSDelegations: []*config.AWSDelegation{nil, {Profile: "ok", SyncCredentials: true}},
			},
			wantProfile: "ok",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := firstSyncingDelegation(tt.remote)
			if tt.wantProfile == "" {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected delegation %q, got nil", tt.wantProfile)
			}
			if got.Profile != tt.wantProfile {
				t.Errorf("got profile %q, want %q", got.Profile, tt.wantProfile)
			}
		})
	}
}
