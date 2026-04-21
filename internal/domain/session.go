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
	ID               string
	IssueKey         string
	Branch           string
	RepoName         string
	RemoteHost       string
	WorktreePath     string
	WorkingDirectory string
	TmuxSession      string
	MutagenSyncID    string
	LocalPath        string
	AgentName        string
	Status           SessionStatus
	Tunnels          []Tunnel
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Tunnel defines a local SSH port forward bound to a session.
// Traffic flows from local 127.0.0.1:<LocalPort> to remote 127.0.0.1:<RemotePort>.
type Tunnel struct {
	LocalPort  int
	RemotePort int
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
	ID                      int
	Number                  int
	Title                   string
	State                   string // OPEN / CLOSED / MERGED (API raw when available)
	DisplayState            string // open, draft, merged, closed — for UI
	IsDraft                 bool
	Merged                  bool
	URL                     string
	ReviewStatus            string // approved, changes_requested, pending, none
	ReviewDecision          string // APPROVED, CHANGES_REQUESTED, REVIEW_REQUIRED, etc.
	CommentCount            int    // top-level PR comments from gh (approximate)
	UnresolvedReviewThreads int    // open review threads; -1 if unknown
	HasMergeConflict        bool   // true when GitHub reports merge conflicts
	Mergeable               string // MERGEABLE, CONFLICTING, UNKNOWN (raw)
	MergeStateStatus        string // e.g. CLEAN, DIRTY, UNSTABLE (raw)
	ChecksStatus            string // success, failure, pending, none
	ChecksSummary           string // e.g. "10/12 passed"
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

	// Snapshot persistence
	SaveSnapshot(ctx context.Context, s *SessionSnapshot) error
	GetLatestSnapshot(ctx context.Context, sessionID string) (*SessionSnapshot, error)
	ListSnapshots(ctx context.Context, sessionID string) ([]SessionSnapshot, error)
	ListAllSnapshots(ctx context.Context) ([]SessionSnapshot, error)
	MarkSnapshotInjected(ctx context.Context, id string) error
	DeleteSnapshot(ctx context.Context, id string) error
}

// SessionConfig holds the configuration for creating a new session.
type SessionConfig struct {
	IssueKey       string
	Issue          *Issue // full JIRA issue (if created from a JIRA issue); used to generate initial agent prompt
	Branch         string
	Repo           Repo
	Directory      string
	Agent          *Agent
	Skills         []Skill
	PromptFree     bool
	ExistingBranch bool           // start from an existing remote branch instead of creating a new one
	SSHManager     RemoteExecutor // remote to create the session on; uses FlowManager default if nil
	RemoteHost     string         // host identifier to tag the session with (e.g. "mydevbox.example.com")
	// PriorSnapshot is an optional snapshot from a previous session on the same branch/issue.
	// When set, its summary and next steps are injected into the agent task file so the
	// new session can continue from where the prior one left off.
	PriorSnapshot *SessionSnapshot
}
