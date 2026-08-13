package usecase

import (
	"context"
	"fmt"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// batchRemote is a discovererRemote that also implements domain.BatchDiscovery,
// so discovery should take the one-round-trip path and never touch the
// per-item scan calls.
type batchRemote struct {
	discovererRemote
	tmuxRecords []domain.TmuxSessionRecord
	wtRecords   []domain.WorktreeRecord
	tmuxErr     error
	wtErr       error
	tmuxCalls   int
	wtCalls     int
}

func (b *batchRemote) ScanTmuxSessionDetails(context.Context) ([]domain.TmuxSessionRecord, error) {
	b.tmuxCalls++
	if b.tmuxErr != nil {
		return nil, b.tmuxErr
	}
	return b.tmuxRecords, nil
}

func (b *batchRemote) ScanWorktreeTree(context.Context) ([]domain.WorktreeRecord, error) {
	b.wtCalls++
	if b.wtErr != nil {
		return nil, b.wtErr
	}
	return b.wtRecords, nil
}

func TestDiscoverUsesBatchPathWhenAvailable(t *testing.T) {
	remote := &batchRemote{
		tmuxRecords: []domain.TmuxSessionRecord{{
			Name:      "WTB-1234-do-a-thing",
			AimanID:   "id-from-tmux",
			CWD:       "/home/code/repos/app@WTB-1234-do-a-thing",
			GitRoot:   "/home/code/repos/app@WTB-1234-do-a-thing",
			RemoteURL: "git@github.com:acme/app.git",
		}},
	}
	// The per-item path would need these; the batch path must not consult them.
	remote.tmuxSessions = []string{"should-not-be-used"}
	remote.repoPaths = []string{"/home/code/repos/app"}

	sessions, err := NewSessionDiscoverer(remote, &recordingSyncEngine{}).Discover(context.Background(), "regent0")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if remote.tmuxCalls != 1 || remote.wtCalls != 1 {
		t.Errorf("expected one batch call each, got tmux=%d worktree=%d", remote.tmuxCalls, remote.wtCalls)
	}
	if len(remote.commands) != 0 {
		t.Errorf("batch path should issue no per-item commands, got %v", remote.commands)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d: %+v", len(sessions), sessions)
	}

	got := sessions[0]
	if got.ID != "id-from-tmux" {
		t.Errorf("ID = %q, want the tmux AIMAN_ID", got.ID)
	}
	if got.TmuxSession != "WTB-1234-do-a-thing" {
		t.Errorf("TmuxSession = %q", got.TmuxSession)
	}
	if got.IssueKey != "WTB-1234" {
		t.Errorf("IssueKey = %q, want WTB-1234", got.IssueKey)
	}
	if got.RepoName != "acme/app" {
		t.Errorf("RepoName = %q, want acme/app derived from the origin URL", got.RepoName)
	}
	if got.RemoteHost != "regent0" {
		t.Errorf("RemoteHost = %q", got.RemoteHost)
	}
}

func TestDiscoverBatchSkipsMissingAndBrokenWorktrees(t *testing.T) {
	remote := &batchRemote{
		wtRecords: []domain.WorktreeRecord{
			{RepoPath: "/repos/app", WorktreePath: "/repos/app@alive", State: domain.WorktreeOK, AimanID: "keep-me"},
			{RepoPath: "/repos/app", WorktreePath: "/repos/app@gone", State: domain.WorktreeMissing},
			{RepoPath: "/repos/app", WorktreePath: "/repos/app@bad", State: domain.WorktreeBroken},
		},
	}

	sessions, err := NewSessionDiscoverer(remote, &recordingSyncEngine{}).Discover(context.Background(), "regent0")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected only the live worktree, got %d: %+v", len(sessions), sessions)
	}
	if sessions[0].ID != "keep-me" {
		t.Errorf("ID = %q, want the id resolved on the remote", sessions[0].ID)
	}
	if sessions[0].Status != domain.SessionStatusInactive {
		t.Errorf("orphan worktree Status = %q, want inactive", sessions[0].Status)
	}
}

// A batch failure must degrade to the per-item path rather than reporting the
// remote as empty.
func TestDiscoverFallsBackWhenBatchFails(t *testing.T) {
	remote := &batchRemote{
		tmuxErr: fmt.Errorf("remote refused"),
		wtErr:   fmt.Errorf("remote refused"),
	}
	remote.tmuxSessions = []string{"fallback-session"}
	remote.repoPaths = nil

	sessions, err := NewSessionDiscoverer(remote, &recordingSyncEngine{}).Discover(context.Background(), "regent0")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 || sessions[0].TmuxSession != "fallback-session" {
		t.Fatalf("expected the per-item path to supply the session, got %+v", sessions)
	}
}

// An executor without the capability keeps working exactly as before.
func TestDiscoverPerItemPathStillWorks(t *testing.T) {
	remote := &discovererRemote{tmuxSessions: []string{"plain-session"}}

	sessions, err := NewSessionDiscoverer(remote, &recordingSyncEngine{}).Discover(context.Background(), "regent0")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 || sessions[0].TmuxSession != "plain-session" {
		t.Fatalf("expected the plain executor to still discover, got %+v", sessions)
	}
}

func TestSessionFromTmuxRecordPrefersTmuxIDOverFileID(t *testing.T) {
	got := sessionFromTmuxRecord("h", domain.TmuxSessionRecord{
		Name:        "s",
		AimanID:     "from-tmux",
		FileAimanID: "from-file",
		CWD:         "/w",
	})
	if got.ID != "from-tmux" {
		t.Errorf("ID = %q, want from-tmux", got.ID)
	}

	got = sessionFromTmuxRecord("h", domain.TmuxSessionRecord{
		Name:        "s",
		FileAimanID: "from-file",
		CWD:         "/w",
	})
	if got.ID != "from-file" {
		t.Errorf("ID = %q, want the git-metadata id when tmux carries none", got.ID)
	}
}

func TestSessionFromTmuxRecordFallsBackToCWDWhenNoGitRoot(t *testing.T) {
	got := sessionFromTmuxRecord("h", domain.TmuxSessionRecord{Name: "s", CWD: "/some/dir"})
	if got.WorktreePath != "/some/dir" {
		t.Errorf("WorktreePath = %q, want the CWD", got.WorktreePath)
	}
	if got.RepoName != "dir" {
		t.Errorf("RepoName = %q, want the path basename", got.RepoName)
	}
}
