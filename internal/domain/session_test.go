package domain

import (
	"testing"
)

func TestSessionTransition(t *testing.T) {
	tests := []struct {
		name     string
		initial  SessionStatus
		target   SessionStatus
		expected SessionStatus
		wantErr  bool
	}{
		{
			name:     "Initial to Provisioning",
			initial:  "",
			target:   SessionStatusProvisioning,
			expected: SessionStatusProvisioning,
			wantErr:  false,
		},
		{
			name:     "Provisioning to Active",
			initial:  SessionStatusProvisioning,
			target:   SessionStatusActive,
			expected: SessionStatusActive,
			wantErr:  false,
		},
		{
			name:     "Active to Syncing",
			initial:  SessionStatusActive,
			target:   SessionStatusSyncing,
			expected: SessionStatusSyncing,
			wantErr:  false,
		},
		{
			name:     "Syncing to Cleanup",
			initial:  SessionStatusSyncing,
			target:   SessionStatusCleanup,
			expected: SessionStatusCleanup,
			wantErr:  false,
		},
		{
			name:     "Invalid Transition (Initial to Active)",
			initial:  "",
			target:   SessionStatusActive,
			expected: "",
			wantErr:  true,
		},
		{
			name:     "Invalid Transition (Syncing to Active)",
			initial:  SessionStatusSyncing,
			target:   SessionStatusActive,
			expected: SessionStatusSyncing,
			wantErr:  true,
		},
		{
			name:     "To Error from Active",
			initial:  SessionStatusActive,
			target:   SessionStatusError,
			expected: SessionStatusError,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Session{Status: tt.initial}
			err := s.Transition(tt.target)
			if (err != nil) != tt.wantErr {
				t.Errorf("Session.Transition() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && s.Status != tt.expected {
				t.Errorf("Session.Transition() status = %v, expected %v", s.Status, tt.expected)
			}
		})
	}
}
