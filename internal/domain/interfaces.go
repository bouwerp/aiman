package domain

import (
	"context"
	"io"
	"os/exec"
	"time"
)

// IssueProvider represents a source of JIRA issues.
type IssueProvider interface {
	SearchIssues(ctx context.Context, query string) ([]Issue, error)
	GetIssue(ctx context.Context, key string) (Issue, error)
	TransitionIssue(ctx context.Context, key string, status string) error
}

// RepositoryManager manages git repositories and worktrees.
type RepositoryManager interface {
	ListRepos(ctx context.Context) ([]Repo, error)
	ListRemoteBranches(ctx context.Context, remote RemoteExecutor, repo Repo) ([]string, error)
	SetupWorktree(ctx context.Context, repo Repo, branch string) (Worktree, error)
	SetupRemoteWorktree(ctx context.Context, remote RemoteExecutor, repo Repo, branch string, baseBranch string) (Worktree, error)
	SetupRemoteWorktreeFromBranch(ctx context.Context, remote RemoteExecutor, repo Repo, branch string) (Worktree, error)
	SetupSharedWorkspace(ctx context.Context, remote RemoteExecutor, repo Repo, branch string) (Worktree, error)
	// FindExistingWorktree returns the path to an already-registered healthy worktree
	// without creating a new one. Used when the user explicitly attaches to an existing worktree.
	FindExistingWorktree(ctx context.Context, remote RemoteExecutor, repo Repo, branch string) (Worktree, error)
	// EnsureAimanSessionFilesGitignored appends Aiman session scaffolding ignore rules to the worktree .gitignore if missing. No-op if path is not a git worktree.
	EnsureAimanSessionFilesGitignored(ctx context.Context, remote RemoteExecutor, worktreePath string) error
	GetGitStatus(ctx context.Context, remote RemoteExecutor, path string) (GitStatus, error)
}

// WorktreeState is the liveness of a worktree as resolved on the remote.
type WorktreeState string

const (
	// WorktreeOK means the directory exists and git resolves a git-dir for it.
	WorktreeOK WorktreeState = "ok"
	// WorktreeMissing means the registered path no longer exists on disk.
	WorktreeMissing WorktreeState = "missing"
	// WorktreeBroken means the directory exists but git cannot resolve it.
	WorktreeBroken WorktreeState = "broken"
)

// WorktreeRecord is one git worktree registered on a remote, with its liveness
// check and aiman id already resolved. Producing these server-side is what lets
// discovery ask one question instead of four per worktree.
type WorktreeRecord struct {
	RepoPath     string
	WorktreePath string
	State        WorktreeState
	// AimanID is the id read from <git-dir>/aiman-id, or empty when the
	// worktree carries no aiman metadata.
	AimanID string
}

// TmuxSessionRecord is one remote tmux session with the per-session facts
// discovery needs, gathered in a single pass on the remote.
type TmuxSessionRecord struct {
	Name string
	// AimanID comes from the tmux session environment (AIMAN_ID).
	AimanID string
	CWD     string
	GitRoot string
	// FileAimanID is the id stored in the worktree's git metadata, used when
	// the tmux environment carries none.
	FileAimanID string
	RemoteURL   string
}

// BatchDiscovery is an optional capability a RemoteExecutor may implement to
// answer a whole discovery pass in a fixed number of round trips rather than
// one per repo, worktree and tmux session. Discovery type-asserts for it and
// falls back to the per-item calls on RemoteExecutor when it is absent.
type BatchDiscovery interface {
	// ScanWorktreeTree returns every worktree under the configured root,
	// pruning stale registrations as it goes.
	ScanWorktreeTree(ctx context.Context) ([]WorktreeRecord, error)
	// ScanTmuxSessionDetails returns every tmux session with its resolved
	// working directory, git root, origin URL and aiman ids.
	ScanTmuxSessionDetails(ctx context.Context) ([]TmuxSessionRecord, error)
}

// RemoteExecutor manages remote connections and command execution.
type RemoteExecutor interface {
	Connect(ctx context.Context) error
	GetRoot() string
	Execute(ctx context.Context, cmd string) (string, error)
	WriteFile(ctx context.Context, path string, content []byte) error
	ValidateDir(ctx context.Context, path string) error
	ScanTmuxSessions(ctx context.Context) ([]string, error)
	ScanGitRepos(ctx context.Context) ([]string, error)
	ScanWorktrees(ctx context.Context, repoPath string) ([]string, error)
	GetGitRoot(ctx context.Context, path string) (string, error)
	GetTmuxSessionCWD(ctx context.Context, sessionName string) (string, error)
	GetTmuxSessionEnv(ctx context.Context, sessionName, envVar string) (string, error)
	CaptureTmuxPane(ctx context.Context, sessionName string) (string, error)
	AttachTmuxSession(sessionName string) *exec.Cmd
	StreamTmuxSession(ctx context.Context, sessionName string) (io.ReadWriteCloser, error)
	StartTmuxSession(ctx context.Context, name string) error
	ProvisionRemote(ctx context.Context, steps []ProvisionStep, progress chan<- ProvisionProgress) error
	Close() error
}

type ProvisionStep struct {
	ID          string
	Name        string
	Command     string
	Description string
}

type ProvisionProgress struct {
	StepID  string
	Status  string // "pending", "running", "success", "error"
	Message string
}

// SyncEngine manages file synchronization between local and remote.
type SyncEngine interface {
	StartSync(ctx context.Context, name, localPath, remotePath string, labels map[string]string, mode SyncMode) error
	StopSync(ctx context.Context) error
	GetStatus(ctx context.Context) (string, error)
	ListSyncSessions(ctx context.Context) ([]SyncSession, error)
	// GetSyncStatus returns the status of a specific sync session.
	GetSyncStatus(ctx context.Context, name string) (string, error)
	// TerminateSync terminates a sync session by name. Errors are ignored
	// since the sync may already be gone.
	TerminateSync(ctx context.Context, name string)
}

// SyncSession represents a file synchronization session.
type SyncSession struct {
	ID             string
	Name           string
	LocalPath      string
	RemotePath     string
	RemoteEndpoint string // e.g. code@regent0 when Beta URL is user@host:/path (before path strip)
	Status         string
	Labels         map[string]string
}

// Repo represents a git repository.
type Repo struct {
	Name  string
	URL   string
	IsNew bool
	// LastActivityAt is the latest of pushedAt/updatedAt from `gh repo list` when the repo was listed; zero if unknown.
	LastActivityAt time.Time
}

// Worktree represents a git worktree.
type Worktree struct {
	Path   string
	Branch string
}
