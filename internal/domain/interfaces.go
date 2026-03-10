package domain

import (
	"context"
	"io"
	"os/exec"
)

// IssueProvider represents a source of JIRA issues.
type IssueProvider interface {
	SearchIssues(ctx context.Context, query string) ([]Issue, error)
	GetIssue(ctx context.Context, key string) (Issue, error)
}

// RepositoryManager manages git repositories and worktrees.
type RepositoryManager interface {
	ListRepos(ctx context.Context) ([]Repo, error)
	SetupWorktree(ctx context.Context, repo Repo, branch string) (Worktree, error)
	SetupRemoteWorktree(ctx context.Context, remote RemoteExecutor, repo Repo, branch string) (Worktree, error)
	GetGitStatus(ctx context.Context, remote RemoteExecutor, path string) (GitStatus, error)
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
	CaptureTmuxPane(ctx context.Context, sessionName string) (string, error)
	AttachTmuxSession(sessionName string) *exec.Cmd
	StreamTmuxSession(ctx context.Context, sessionName string) (io.ReadWriteCloser, error)
	StartTmuxSession(ctx context.Context, name string) error
	Close() error
}

// SyncEngine manages file synchronization between local and remote.
type SyncEngine interface {
	StartSync(ctx context.Context, localPath, remotePath string) error
	StopSync(ctx context.Context) error
	GetStatus(ctx context.Context) (string, error)
	ListSyncSessions(ctx context.Context) ([]SyncSession, error)
}

// SyncSession represents a file synchronization session.
type SyncSession struct {
	ID         string
	Name       string
	LocalPath  string
	RemotePath string
	Status     string
}

// Repo represents a git repository.
type Repo struct {
	Name  string
	URL   string
	IsNew bool
}

// Worktree represents a git worktree.
type Worktree struct {
	Path   string
	Branch string
}
