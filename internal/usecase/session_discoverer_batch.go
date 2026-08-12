package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/google/uuid"
)

// aimanIDReadCmd reads a worktree's aiman id from git metadata, falling back to
// the legacy dot-file at the worktree root and migrating it when found.
func aimanIDReadCmd(worktreePath string) string {
	return fmt.Sprintf(
		"git_dir=$(git -C %q rev-parse --absolute-git-dir 2>/dev/null) && if [ -f \"$git_dir/aiman-id\" ]; then cat \"$git_dir/aiman-id\"; elif [ -f %q/.aiman-id ]; then cat %q/.aiman-id; fi",
		worktreePath, worktreePath, worktreePath)
}

// gatherTmuxRecords collects one record per remote tmux session. When the
// executor implements domain.BatchDiscovery this is a single round trip;
// otherwise it falls back to the per-session calls on RemoteExecutor.
func (d *SessionDiscoverer) gatherTmuxRecords(ctx context.Context) ([]domain.TmuxSessionRecord, error) {
	if batch, ok := d.remoteExecutor.(domain.BatchDiscovery); ok {
		records, err := batch.ScanTmuxSessionDetails(ctx)
		if err == nil {
			return records, nil
		}
		// A batch failure is not fatal: fall through and pay for the slow path
		// rather than reporting the remote as having no sessions.
	}
	return d.tmuxRecordsPerItem(ctx)
}

func (d *SessionDiscoverer) tmuxRecordsPerItem(ctx context.Context) ([]domain.TmuxSessionRecord, error) {
	names, err := d.remoteExecutor.ScanTmuxSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to scan tmux sessions: %w", err)
	}

	records := make([]domain.TmuxSessionRecord, 0, len(names))
	for _, name := range names {
		rec := domain.TmuxSessionRecord{Name: name}
		if env, err := d.remoteExecutor.GetTmuxSessionEnv(ctx, name, "AIMAN_ID"); err == nil {
			rec.AimanID = strings.TrimSpace(env)
		}
		if cwd, err := d.remoteExecutor.GetTmuxSessionCWD(ctx, name); err == nil {
			rec.CWD = cwd
			if root, err := d.remoteExecutor.GetGitRoot(ctx, normalizePath(cwd)); err == nil {
				rec.GitRoot = root
			}
		}

		root := rec.GitRoot
		if root == "" {
			root = rec.CWD
		}
		if root == "" {
			records = append(records, rec)
			continue
		}
		// Only pay for the id lookup when tmux did not already answer it.
		if rec.AimanID == "" {
			if id, err := d.remoteExecutor.Execute(ctx, aimanIDReadCmd(normalizePath(root))); err == nil {
				rec.FileAimanID = strings.TrimSpace(id)
			}
		}
		if url, err := d.remoteExecutor.Execute(ctx, fmt.Sprintf("git -C %q remote get-url origin 2>/dev/null", normalizePath(root))); err == nil {
			rec.RemoteURL = strings.TrimSpace(url)
		}
		records = append(records, rec)
	}
	return records, nil
}

// gatherWorktreeRecords collects every registered worktree under the remote's
// root, batched into one round trip when the executor supports it.
func (d *SessionDiscoverer) gatherWorktreeRecords(ctx context.Context) []domain.WorktreeRecord {
	if batch, ok := d.remoteExecutor.(domain.BatchDiscovery); ok {
		records, err := batch.ScanWorktreeTree(ctx)
		if err == nil {
			return records
		}
	}
	return d.worktreeRecordsPerItem(ctx)
}

func (d *SessionDiscoverer) worktreeRecordsPerItem(ctx context.Context) []domain.WorktreeRecord {
	repos, err := d.remoteExecutor.ScanGitRepos(ctx)
	if err != nil {
		return nil
	}

	var records []domain.WorktreeRecord
	for _, repoPath := range repos {
		worktrees, err := d.remoteExecutor.ScanWorktrees(ctx, repoPath)
		if err != nil {
			continue
		}
		for _, wtPath := range worktrees {
			normalized := normalizePath(wtPath)
			rec := domain.WorktreeRecord{
				RepoPath:     repoPath,
				WorktreePath: normalized,
				State:        domain.WorktreeMissing,
			}
			if d.isDiscoverableOrphanWorktree(ctx, repoPath, normalized) {
				rec.State = domain.WorktreeOK
				if id, err := d.remoteExecutor.Execute(ctx, aimanIDReadCmd(normalized)); err == nil {
					rec.AimanID = strings.TrimSpace(id)
				}
			}
			records = append(records, rec)
		}
	}
	return records
}

// sessionFromRecord builds a session from a tmux record and cross-references it
// against the known mutagen syncs, mirroring what the per-call path did once it
// had gathered the same facts.
func (d *SessionDiscoverer) sessionFromRecord(host string, rec domain.TmuxSessionRecord, mutagenSessions []domain.SyncSession) domain.Session {
	session := sessionFromTmuxRecord(host, rec)

	if session.WorktreePath != "" {
		for _, ms := range mutagenSessions {
			if !d.isSessionMatch(session, ms) {
				continue
			}
			session.LocalPath = normalizePath(ms.LocalPath)
			if ms.Name != "" {
				session.MutagenSyncID = ms.Name
			} else {
				session.MutagenSyncID = ms.ID
			}
			session.Status = domain.SessionStatusSyncing
			break
		}
	}

	// Legacy sessions carry no aiman id anywhere; give them a stable one now.
	if session.ID == "" {
		session.ID = uuid.New().String()
	}
	return session
}

// sessionFromTmuxRecord converts a gathered record into a session, applying the
// same field precedence the per-call path used: the tmux environment's AIMAN_ID
// wins over the id stored in git metadata, and a git root wins over the raw CWD.
func sessionFromTmuxRecord(host string, rec domain.TmuxSessionRecord) domain.Session {
	session := domain.Session{
		TmuxSession: rec.Name,
		RemoteHost:  host,
		Status:      domain.SessionStatusActive,
		CreatedAt:   time.Now(), // Approximate: tmux does not expose a creation time here.
	}

	if id := strings.TrimSpace(rec.AimanID); id != "" {
		session.ID = id
	}

	if rec.CWD != "" {
		session.WorkingDirectory = normalizePath(rec.CWD)
		if rec.GitRoot != "" {
			session.WorktreePath = normalizePath(rec.GitRoot)
		} else {
			session.WorktreePath = session.WorkingDirectory
		}
	}

	if session.ID == "" {
		session.ID = strings.TrimSpace(rec.FileAimanID)
	}

	session.IssueKey = domain.ExtractKey(rec.Name)
	if session.IssueKey == "" && session.WorktreePath != "" {
		session.IssueKey = domain.ExtractKey(session.WorktreePath)
	}

	if session.WorktreePath != "" {
		if url := strings.TrimSpace(rec.RemoteURL); url != "" {
			session.RepoName = extractRepoNameFromURL(url)
		}
		if session.RepoName == "" {
			parts := strings.Split(session.WorktreePath, "/")
			session.RepoName = parts[len(parts)-1]
		}
	}

	return session
}
