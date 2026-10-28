package usecase

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// batchRemote is a discovererRemote that also implements domain.BatchDiscovery,
// so discovery should take the one-round-trip path and never touch the
// per-item scan calls.
type batchRemote struct {
	discovererRemote
	wtRecords []domain.WorktreeRecord
	wtErr     error
	wtCalls   int
}

func (b *batchRemote) ScanWorktreeTree(context.Context) ([]domain.WorktreeRecord, error) {
	b.wtCalls++
	if b.wtErr != nil {
		return nil, b.wtErr
	}
	return b.wtRecords, nil
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

func TestDiscoverUsesBatchPathWhenAvailable(t *testing.T) {
	remote := &batchRemote{
		wtRecords: []domain.WorktreeRecord{
			{RepoPath: "/repos/app", WorktreePath: "/repos/app@WTB-1234-do-a-thing", State: domain.WorktreeOK, AimanID: "p1"},
		},
	}
	remote.outputs = ptyListOutput(`{"id":"p1","name":"WTB-1234-do-a-thing","dir":"/repos/app@WTB-1234-do-a-thing","status":"running"}`)
	// The per-item path would need these; the batch path must not consult them.
	remote.repoPaths = []string{"/repos/app"}

	sessions, err := NewSessionDiscoverer(remote, &recordingSyncEngine{}).Discover(context.Background(), "regent0")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if remote.wtCalls != 1 {
		t.Errorf("want one worktree batch call, got %d", remote.wtCalls)
	}
	for _, c := range remote.commands {
		// Fixed single-round-trip scans are allowed: the PTY runtime list and
		// the native-session hook directory sweep.
		if strings.Contains(c, "native-sessions") || strings.Contains(c, "aiman pty list") {
			continue
		}
		t.Errorf("batch path should issue no per-item commands, got %v", remote.commands)
		break
	}
	if len(sessions) != 1 {
		t.Fatalf("the live session and its worktree are one session, got %d: %+v", len(sessions), sessions)
	}
	if got := sessions[0]; got.ID != "p1" || got.Status != domain.SessionStatusActive || got.RemoteHost != "regent0" {
		t.Errorf("session = %+v", got)
	}
}

// A batch failure must degrade to the per-item path rather than reporting the
// remote as empty.
func TestDiscoverFallsBackWhenBatchFails(t *testing.T) {
	remote := &batchRemote{wtErr: fmt.Errorf("remote refused")}
	remote.repoPaths = []string{"/repos/app"}
	remote.worktreesByRepo = map[string][]string{"/repos/app": {"/repos/app@feat"}}
	remote.validDirs = map[string]bool{"/repos/app@feat": true}
	remote.outputs = map[string]string{`git -C "/repos/app@feat" rev-parse --git-dir 2>/dev/null || echo BROKEN`: ".git"}

	sessions, err := NewSessionDiscoverer(remote, &recordingSyncEngine{}).Discover(context.Background(), "regent0")
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(sessions) != 1 || sessions[0].WorktreePath != "/repos/app@feat" {
		t.Fatalf("expected the per-item path to supply the worktree, got %+v", sessions)
	}
}
