package ssh

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// batchScanTimeout budgets a whole-remote scan. One batch call replaces roughly
// two hundred individual commands, so it does correspondingly more work per
// round trip and needs more than sshCommandTimeout allows.
const batchScanTimeout = 2 * time.Minute

// Field tags for the batch scan wire format. Each record is one tab-separated
// line; anything that does not start with a known tag is ignored, which keeps
// the parsers immune to stderr noise folded in by Execute's CombinedOutput.
const worktreeRecordTag = "WT"

// shellQuote wraps s in single quotes for POSIX sh, escaping any single quote
// it contains. Unlike %q this produces shell syntax rather than Go syntax, so
// it is safe for paths containing backslashes or double quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// worktreeScanScript walks every repo under root and reports each registered
// worktree with its liveness and aiman id resolved on the remote.
//
// The per-repo `worktree prune` keeps stale registrations from being reported
// at all, which is what the old per-worktree cleanup calls were for.
// `--absolute-git-dir` matters: plain `--git-dir` returns a bare ".git" for a
// main worktree, so an aiman-id lookup built on it would resolve against the
// SSH login directory instead of the repo.
func worktreeScanScript(root string) string {
	return `root=` + shellQuote(root) + `
find "$root" -maxdepth 3 -name .git -type d -prune 2>/dev/null | while IFS= read -r gd; do
  repo=${gd%/.git}
  [ -n "$repo" ] || continue
  git -C "$repo" worktree prune --expire=now >/dev/null 2>&1 || true
  git -C "$repo" worktree list --porcelain 2>/dev/null | sed -n 's/^worktree //p' | while IFS= read -r wt; do
    [ -n "$wt" ] || continue
    if [ ! -d "$wt" ]; then
      printf '` + worktreeRecordTag + `\t%s\t%s\tmissing\t\n' "$repo" "$wt"
      continue
    fi
    adir=$(git -C "$wt" rev-parse --absolute-git-dir 2>/dev/null)
    if [ -z "$adir" ]; then
      printf '` + worktreeRecordTag + `\t%s\t%s\tbroken\t\n' "$repo" "$wt"
      continue
    fi
    id=''
    if [ -f "$adir/aiman-id" ]; then
      id=$(cat "$adir/aiman-id" 2>/dev/null)
    elif [ -f "$wt/.aiman-id" ]; then
      id=$(cat "$wt/.aiman-id" 2>/dev/null)
      mv "$wt/.aiman-id" "$adir/aiman-id" 2>/dev/null || true
    fi
    printf '` + worktreeRecordTag + `\t%s\t%s\tok\t%s\n' "$repo" "$wt" "$id"
  done
done`
}

// ScanWorktreeTree implements domain.BatchDiscovery. It answers in one round
// trip what the per-item path asks in one call per repo plus two per worktree.
func (m *Manager) ScanWorktreeTree(ctx context.Context) ([]domain.WorktreeRecord, error) {
	if m.config.Root == "" {
		return nil, nil
	}
	out, err := m.executeWithTimeout(ctx, worktreeScanScript(m.config.Root), batchScanTimeout)
	if err != nil {
		return nil, fmt.Errorf("failed to scan worktree tree: %w", err)
	}
	return parseWorktreeRecords(out), nil
}

// splitRecord returns the fields of a tagged record line, or nil when the line
// does not carry the wanted tag or has too few fields.
func splitRecord(line, tag string, wantFields int) []string {
	parts := strings.Split(line, "\t")
	if len(parts) < wantFields || parts[0] != tag {
		return nil
	}
	return parts
}

func parseWorktreeRecords(out string) []domain.WorktreeRecord {
	var records []domain.WorktreeRecord
	for _, line := range strings.Split(out, "\n") {
		parts := splitRecord(strings.TrimRight(line, "\r"), worktreeRecordTag, 5)
		if parts == nil {
			continue
		}
		repo, wt := strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
		if repo == "" || wt == "" {
			continue
		}
		state := domain.WorktreeState(strings.TrimSpace(parts[3]))
		switch state {
		case domain.WorktreeOK, domain.WorktreeMissing, domain.WorktreeBroken:
		default:
			continue
		}
		records = append(records, domain.WorktreeRecord{
			RepoPath:     repo,
			WorktreePath: wt,
			State:        state,
			AimanID:      strings.TrimSpace(parts[4]),
		})
	}
	return records
}
