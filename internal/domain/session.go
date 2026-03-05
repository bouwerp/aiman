package domain

import (
	"context"
	"errors"
	"time"
)

type SessionStatus string

const (
	SessionStatusProvisioning SessionStatus = "PROVISIONING"
	SessionStatusActive       SessionStatus = "ACTIVE"
	SessionStatusSyncing      SessionStatus = "SYNCING"
	SessionStatusCleanup      SessionStatus = "CLEANUP"
	SessionStatusError        SessionStatus = "ERROR"
)

type Session struct {
	ID            string
	IssueKey      string
	Branch        string
	RepoName      string
	RemoteHost    string
	WorktreePath  string
	TmuxSession   string
	MutagenSyncID string
	LocalPath     string
	AgentName     string
	Status        SessionStatus
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

var ErrInvalidTransition = errors.New("invalid session state transition")

func (s *Session) Transition(target SessionStatus) error {
	switch s.Status {
	case "":
		if target == SessionStatusProvisioning {
			s.Status = target
			return nil
		}
	case SessionStatusProvisioning:
		if target == SessionStatusActive || target == SessionStatusError {
			s.Status = target
			return nil
		}
	case SessionStatusActive:
		if target == SessionStatusSyncing || target == SessionStatusCleanup || target == SessionStatusError {
			s.Status = target
			return nil
		}
	case SessionStatusSyncing:
		if target == SessionStatusCleanup || target == SessionStatusError {
			s.Status = target
			return nil
		}
	case SessionStatusCleanup:
		if target == SessionStatusError { // Allow transition to error from cleanup
			s.Status = target
			return nil
		}
	}
	return ErrInvalidTransition
}

// SessionRepository defines the interface for session persistence
type SessionRepository interface {
	Save(ctx context.Context, s *Session) error
	Get(ctx context.Context, id string) (*Session, error)
	List(ctx context.Context) ([]Session, error)
	Delete(ctx context.Context, id string) error
	Close() error
}
