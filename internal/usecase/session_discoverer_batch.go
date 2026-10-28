package usecase

import (
	"context"
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/google/uuid"
)

// isMainWorktree reports whether a record describes a repository's own checkout
// rather than a linked worktree. `git worktree list` includes the main worktree,
// and treating it as an orphan invents one session per repository.
func isMainWorktree(rec domain.WorktreeRecord) bool {
	return normalizePath(rec.RepoPath) == normalizePath(rec.WorktreePath)
}

// stableSessionID derives a deterministic id for a discovered thing that carries
// no aiman-id of its own, so repeated scans converge on one database row instead
// of accumulating a new one each time.
func stableSessionID(host, key string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("aiman://"+host+"/"+key)).String()
}

// aimanIDReadCmd reads a worktree's aiman id from git metadata, falling back to
// the legacy dot-file at the worktree root and migrating it when found.
func aimanIDReadCmd(worktreePath string) string {
	return fmt.Sprintf(
		"git_dir=$(git -C %q rev-parse --absolute-git-dir 2>/dev/null) && if [ -f \"$git_dir/aiman-id\" ]; then cat \"$git_dir/aiman-id\"; elif [ -f %q/.aiman-id ]; then cat %q/.aiman-id; fi",
		worktreePath, worktreePath, worktreePath)
}

// gatherWorktreeRecords collects every registered worktree under the remote's
// root, batched into one round trip when the executor supports it. A batch
// failure falls back to the per-item path; if that fails too, the error
// propagates — a failed sweep must not read as "this host has no worktrees",
// or every inactive session on it gets treated as confirmed dead.
func (d *SessionDiscoverer) gatherWorktreeRecords(ctx context.Context) ([]domain.WorktreeRecord, error) {
	if batch, ok := d.remoteExecutor.(domain.BatchDiscovery); ok {
		records, err := batch.ScanWorktreeTree(ctx)
		if err == nil {
			return records, nil
		}
	}
	return d.worktreeRecordsPerItem(ctx)
}

func (d *SessionDiscoverer) worktreeRecordsPerItem(ctx context.Context) ([]domain.WorktreeRecord, error) {
	repos, err := d.remoteExecutor.ScanGitRepos(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to scan git repos: %w", err)
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
	return records, nil
}
