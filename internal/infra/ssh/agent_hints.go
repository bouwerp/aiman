package ssh

import (
	"context"
	"fmt"
	"strings"
)

// hintRecordTag tags a co-author-hint line in WorktreeCoAuthorHintsCmd's output.
const hintRecordTag = "HINT"

// WorktreeCoAuthorHintsCmd builds a single-round-trip command that reports
// every distinct git "Co-authored-by" commit trailer found in each of the
// given worktrees' recent history. Commit trailers are a per-worktree signal
// (unlike a vendor's project files, which are often committed to the repo
// and shared across every worktree of it) and naturally capture more than
// one agent having worked in the same directory over time.
//
// The last 50 commits is a deliberate bound: enough history to catch an
// abandoned worktree's whole life in the common case, cheap enough that
// scanning dozens of worktrees in one call stays fast.
func WorktreeCoAuthorHintsCmd(paths []string) string {
	if len(paths) == 0 {
		return ":"
	}
	quoted := make([]string, len(paths))
	for i, p := range paths {
		quoted[i] = shellQuote(p)
	}
	return `for wt in ` + strings.Join(quoted, " ") + `; do
  git -C "$wt" log --all -50 --format='%(trailers:key=Co-authored-by,valueonly)' 2>/dev/null | grep -v '^$' | sort -u | while IFS= read -r line; do
    [ -n "$line" ] || continue
    printf '` + hintRecordTag + `\t%s\t%s\n' "$wt" "$line"
  done
done`
}

// ParseWorktreeCoAuthorHints parses WorktreeCoAuthorHintsCmd's output into a
// worktree path -> raw trailer lines map. Callers turn each line into an
// agent name via agenthook.InferAgentNameFromText.
func ParseWorktreeCoAuthorHints(out string) map[string][]string {
	hints := make(map[string][]string)
	for _, line := range strings.Split(out, "\n") {
		parts := splitRecord(strings.TrimRight(line, "\r"), hintRecordTag, 3)
		if parts == nil {
			continue
		}
		path := strings.TrimSpace(parts[1])
		hint := strings.TrimSpace(parts[2])
		if path == "" || hint == "" {
			continue
		}
		hints[path] = append(hints[path], hint)
	}
	return hints
}

// WorktreeCoAuthorHints runs WorktreeCoAuthorHintsCmd for the given worktree
// paths. Called on demand when the revive-worktree screen opens, never from
// the background discovery path.
func (m *Manager) WorktreeCoAuthorHints(ctx context.Context, paths []string) (map[string][]string, error) {
	if len(paths) == 0 {
		return map[string][]string{}, nil
	}
	out, err := m.executeWithTimeout(ctx, WorktreeCoAuthorHintsCmd(paths), batchScanTimeout)
	if err != nil {
		return nil, fmt.Errorf("failed to scan worktree co-author hints: %w", err)
	}
	return ParseWorktreeCoAuthorHints(out), nil
}
