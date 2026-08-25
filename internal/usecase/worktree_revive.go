package usecase

import (
	"strings"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
)

// ResolveWorktreeAgentCandidates returns the ranked, deduplicated set of
// agent names that plausibly worked in session's worktree, for the
// "revive worktree" flow: the already-known AgentName first (persisted, or
// inferred earlier from a hook sidecar's transcript path — see
// enrichHookReports), then whatever each git commit-trailer hint resolves
// to (see ssh.WorktreeCoAuthorHints). A worktree touched by more than one
// agent over its lifetime naturally produces more than one candidate here;
// an empty result means nothing recognizable was found at all, and callers
// must fall back to the manual agent picker rather than guess.
func ResolveWorktreeAgentCandidates(session domain.Session, hints []string) []string {
	var candidates []string
	seen := make(map[string]bool)
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		candidates = append(candidates, name)
	}

	add(session.AgentName)
	for _, hint := range hints {
		add(agenthook.InferAgentNameFromText(hint))
	}
	return candidates
}
