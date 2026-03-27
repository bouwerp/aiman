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
	SessionStatusInactive     SessionStatus = "INACTIVE"
)

type Session struct {
	ID            string
	IssueKey      string
	Branch        string
	RepoName      string
	RemoteHost    string
	WorktreePath  string
	WorkingDirectory string
	TmuxSession   string
	MutagenSyncID string
	LocalPath     string
	AgentName     string
	Status        SessionStatus
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type GitStatus struct {
	Branch          string
	Ahead           int
	Behind          int
	UntrackedCount  int
	StagedCount     int
	UnstagedCount   int
	PullRequest     *PullRequest
	UnpushedCommits int
	TrackingRemote  string
	HasUpstream     bool
}

type PullRequest struct {
	ID            int
	Number        int
	Title         string
	State         string
	URL           string
	ReviewStatus  string // approved, changes_requested, pending
	CommentCount  int
	ChecksStatus  string // success, failure, pending
	ChecksSummary string // e.g. "10/12 passed"
}

var ErrInvalidTransition = errors.New("invalid session state transition")

func (s *Session) Transition(target SessionStatus) error {
	switch s.Status {
	case "", SessionStatusInactive:
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

// SessionConfig holds the configuration for creating a new session.
type SessionConfig struct {
	IssueKey       string
	Issue          *Issue         // full JIRA issue (if created from a JIRA issue); used to generate initial agent prompt
	Branch         string
	Repo           Repo
	Directory      string
	Agent          *Agent
	Skills         []Skill
	PromptFree     bool
	ExistingBranch bool           // start from an existing remote branch instead of creating a new one
	SSHManager     RemoteExecutor // remote to create the session on; uses FlowManager default if nil
	RemoteHost     string         // host identifier to tag the session with (e.g. "mydevbox.example.com")
}
