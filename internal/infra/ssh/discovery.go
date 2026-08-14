package ssh

import (
	"context"
	"fmt"
	"strconv"
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
const (
	worktreeRecordTag = "WT"
	tmuxRecordTag     = "TS"
)

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

// tmuxScanScript reports every tmux session with the facts discovery needs:
// the AIMAN_ID from the session environment, the pane's working directory, the
// git root above it, any aiman id stored in git metadata, and the origin URL.
func tmuxScanScript() string {
	return `tmux ls -F '#S' 2>/dev/null | while IFS= read -r s; do
  [ -n "$s" ] || continue
  aid=$(tmux show-environment -t "$s" AIMAN_ID 2>/dev/null | sed -n 's/^AIMAN_ID=//p')
  cwd=$(tmux display-message -p -F '#{pane_current_path}' -t "$s" 2>/dev/null)
  root=''
  url=''
  fid=''
  if [ -n "$cwd" ] && [ -d "$cwd" ]; then
    root=$(git -C "$cwd" rev-parse --show-toplevel 2>/dev/null)
    if [ -n "$root" ]; then
      url=$(git -C "$root" remote get-url origin 2>/dev/null)
      adir=$(git -C "$root" rev-parse --absolute-git-dir 2>/dev/null)
      if [ -n "$adir" ] && [ -f "$adir/aiman-id" ]; then
        fid=$(cat "$adir/aiman-id" 2>/dev/null)
      elif [ -f "$root/.aiman-id" ]; then
        fid=$(cat "$root/.aiman-id" 2>/dev/null)
        if [ -n "$adir" ]; then mv "$root/.aiman-id" "$adir/aiman-id" 2>/dev/null || true; fi
      fi
    fi
  fi
  printf '` + tmuxRecordTag + `\t%s\t%s\t%s\t%s\t%s\t%s\n' "$s" "$aid" "$cwd" "$root" "$fid" "$url"
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

// ScanTmuxSessionDetails implements domain.BatchDiscovery.
func (m *Manager) ScanTmuxSessionDetails(ctx context.Context) ([]domain.TmuxSessionRecord, error) {
	out, err := m.executeWithTimeout(ctx, tmuxScanScript(), batchScanTimeout)
	if err != nil {
		// A remote with no tmux server running exits non-zero; that is an empty
		// result, not a failure worth aborting discovery over.
		return nil, nil
	}
	return parseTmuxSessionRecords(out), nil
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

func parseTmuxSessionRecords(out string) []domain.TmuxSessionRecord {
	var records []domain.TmuxSessionRecord
	for _, line := range strings.Split(out, "\n") {
		parts := splitRecord(strings.TrimRight(line, "\r"), tmuxRecordTag, 7)
		if parts == nil {
			continue
		}
		name := strings.TrimSpace(parts[1])
		if name == "" {
			continue
		}
		records = append(records, domain.TmuxSessionRecord{
			Name:        name,
			AimanID:     strings.TrimSpace(parts[2]),
			CWD:         strings.TrimSpace(parts[3]),
			GitRoot:     strings.TrimSpace(parts[4]),
			FileAimanID: strings.TrimSpace(parts[5]),
			RemoteURL:   strings.TrimSpace(parts[6]),
		})
	}
	return records
}

// SessionActivityAges returns, per tmux session, how long since it last
// produced output. tmux tracks this itself, so one round trip answers for every
// session at once — no pane capture, no per-session command.
//
// This is the cheapest signal available for "is anything happening", and the
// only one that scales: classifying fifteen sessions previously meant fifteen
// captures.
func (m *Manager) SessionActivityAges(ctx context.Context) (map[string]time.Duration, error) {
	const script = `now=$(date +%s); tmux ls -F '#{session_name}	#{session_activity}' 2>/dev/null | while IFS='	' read -r n a; do
  [ -n "$n" ] || continue
  printf 'SA\t%s\t%s\n' "$n" "$(( now - a ))"
done`
	out, err := m.Execute(ctx, script)
	if err != nil {
		// No tmux server is an empty result, not a failure.
		return map[string]time.Duration{}, nil
	}
	return parseSessionActivityAges(out), nil
}

func parseSessionActivityAges(out string) map[string]time.Duration {
	ages := make(map[string]time.Duration)
	for _, line := range strings.Split(out, "\n") {
		parts := splitRecord(strings.TrimRight(line, "\r"), "SA", 3)
		if parts == nil {
			continue
		}
		name := strings.TrimSpace(parts[1])
		secs, err := strconv.Atoi(strings.TrimSpace(parts[2]))
		if name == "" || err != nil || secs < 0 {
			continue
		}
		ages[name] = time.Duration(secs) * time.Second
	}
	return ages
}
