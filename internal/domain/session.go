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
	SessionStatusReview       SessionStatus = "REVIEW"
)

type SessionMode string

const (
	SessionModeInteractive SessionMode = "INTERACTIVE"
	SessionModeAutonomous  SessionMode = "AUTONOMOUS"
)

type AutonomousConfig struct {
	TriggerType       string `json:"trigger_type"`    // "github" or "sentry"
	GitHubRepo        string `json:"github_repo"`     // "owner/repo"
	FilterLabels      string `json:"filter_labels"`   // e.g. "bug,aiman-auto"
	MaxConcurrency    int    `json:"max_concurrency"` // e.g. 5
	ReuseWorkspace    bool   `json:"reuse_workspace"`
	PollFrequencySecs int    `json:"poll_frequency_secs"` // e.g. 300
}

type SyncMode string

const (
	SyncModeTwoWay        SyncMode = "two-way-safe"
	SyncModeOneWaySafe    SyncMode = "one-way-safe"
	SyncModeOneWayReplica SyncMode = "one-way-replica"
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
	AgentModel       string // LLM model in use (e.g. "claude-opus-4-5"); detected at session creation
	Status           SessionStatus
	Mode             SessionMode       // INTERACTIVE or AUTONOMOUS
	TriggerSource    string            // e.g., github, sentry, jira
	TriggerEventID   string            // ID of the event that triggered the autonomous session
	AutonomousConfig *AutonomousConfig // Specific configuration for autonomous polling
	Tunnels          []Tunnel
	AWSProfileName   string     // legacy session-scoped AWS profile on the remote; only kept for migration detection and cleanup
	AWSConfig        *AWSConfig // role/region/policy used to refresh the shared remote AWS credential set; persisted for refresh
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
		if target == SessionStatusSyncing || target == SessionStatusCleanup || target == SessionStatusError || target == SessionStatusReview {
			s.Status = target
			return nil
		}
	case SessionStatusReview:
		if target == SessionStatusCleanup || target == SessionStatusError || target == SessionStatusActive {
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

// Secret represents a named env-var secret that can be injected into sessions.
type Secret struct {
	Key         string // env var name, e.g. MY_API_KEY
	Value       string
	Description string // optional human-readable label
}

// SessionRepository defines the interface for session persistence
type SessionRepository interface {
	Save(ctx context.Context, s *Session) error
	Get(ctx context.Context, id string) (*Session, error)
	List(ctx context.Context) ([]Session, error)
	Delete(ctx context.Context, id string) error
	Close() error
	HasActiveSessionForEvent(ctx context.Context, source string, eventID string) (bool, error)

	// Snapshot persistence
	SaveSnapshot(ctx context.Context, s *SessionSnapshot) error
	GetLatestSnapshot(ctx context.Context, sessionID string) (*SessionSnapshot, error)
	ListSnapshots(ctx context.Context, sessionID string) ([]SessionSnapshot, error)
	ListAllSnapshots(ctx context.Context) ([]SessionSnapshot, error)
	MarkSnapshotInjected(ctx context.Context, id string) error
	DeleteSnapshot(ctx context.Context, id string) error

	// Secret persistence
	ListSecrets(ctx context.Context) ([]Secret, error)
	SaveSecret(ctx context.Context, s Secret) error
	DeleteSecret(ctx context.Context, key string) error
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
	AttachExisting bool           // attach to an already-existing worktree without attempting git setup
	ReuseWorkspace bool           // bypass git worktree and execute directly in the main clone
	AdHoc          bool           // ad-hoc session: no git repo, no JIRA; Branch is used as the session label
	SSHManager     RemoteExecutor // remote to create the session on; uses FlowManager default if nil
	RemoteHost     string         // host identifier to tag the session with (e.g. "mydevbox.example.com")
	// PriorSnapshot is an optional snapshot from a previous session on the same branch/issue.
	// When set, its summary and next steps are injected into the agent task file so the
	// new session can continue from where the prior one left off.
	PriorSnapshot *SessionSnapshot
	// AWSConfig specifies per-session AWS credentials to push to the remote at session start.
	// When set, Aiman refreshes the shared remote AWS credential/config files before
	// starting or refreshing the session, and only injects region env when needed.
	// Inherited from the remote's AWSDelegation config when nil (and the remote has
	// SyncCredentials enabled).
	AWSConfig *AWSConfig
	// OpenRouterAPIKey is the API key to inject as OPENROUTER_API_KEY into the tmux session
	// environment. Read from the local OPENROUTER_API_KEY env var by default; may be
	// overridden in the session creation summary screen.
	OpenRouterAPIKey string
	// EnvSecrets is the list of global secrets selected for injection into this session.
	// Each secret is added as a -e KEY=VALUE flag to the tmux new-session command.
	EnvSecrets []Secret
	// InitialPrompt is free-text entered in the session summary dialog. It is appended
	// to the agent's initial prompt (after any JIRA task trigger) and delivered via
	// tmux send-keys. Empty means no extra prompt text.
	InitialPrompt string
	// AutonomousConfig configures polling rules if Mode is AUTONOMOUS.
	AutonomousConfig *AutonomousConfig

	// Trigger metadata for ephemeral agents spawned from rules.
	Mode           SessionMode
	TriggerSource  string
	TriggerEventID string
}
