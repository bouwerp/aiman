package verify

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"

	"github.com/bouwerp/aiman/internal/coordinator"
)

// TouchedProtected returns the protected files the patch changes, in the order
// of files. A rename away from a protected file counts, as does a delete.
func TouchedProtected(patch string, files []string) []string {
	if len(files) == 0 {
		return nil
	}
	touched := map[string]bool{}
	for _, line := range strings.Split(patch, "\n") {
		rest, ok := strings.CutPrefix(line, "diff --git a/")
		if !ok {
			continue
		}
		from, to, _ := strings.Cut(rest, " b/")
		touched[from] = true
		touched[to] = true
	}
	var out []string
	for _, f := range files {
		if touched[f] && !slices.Contains(out, f) {
			out = append(out, f)
		}
	}
	return out
}

// acceptancePlan reads the brief in the worker's task file and records what
// verify enforces on the review: the commands to run and the protected files
// the diff touches. A task with no brief leaves the review unchanged.
func acceptancePlan(ctx context.Context, r *Review, worker, task string, base string, diff Diff) {
	brief, ok := coordinator.BriefFromTaskFile(task)
	if !ok {
		return
	}
	files := brief.AcceptanceFiles()
	r.AcceptanceCommands = brief.AcceptanceCommands()
	patch := diff.Patch
	if diff.Truncated && len(files) > 0 {
		if full, err := DiffAgainst(ctx, worker, base, r.Commit, maxProtectedPatchBytes); err == nil {
			patch = full.Patch
		}
	}
	r.TouchedAcceptance = TouchedProtected(patch, files)
}

// maxProtectedPatchBytes bounds the patch re-read when the reviewer's copy was
// truncated, so a protected file past the cut is still found.
const maxProtectedPatchBytes = 64 << 20

// acceptanceFindings returns one REQUIRED finding per protected file the
// worker edited and, when runCommands is set, one per acceptance command that
// exits non-zero at the snapshot.
func (m *Manager) acceptanceFindings(ctx context.Context, r Review, runCommands bool) []Finding {
	var out []Finding
	add := func(title, loc, body string) {
		out = append(out, Finding{
			ID: fmt.Sprintf("acc%d", len(out)+1), Severity: SeverityRequired,
			Title: title, Location: loc, Body: body,
		})
	}
	for _, f := range r.TouchedAcceptance {
		add("Acceptance test edited: "+f, f,
			"The brief protects this acceptance test. Restore it and change the implementation instead.")
	}
	if !runCommands {
		return out
	}
	for _, c := range r.AcceptanceCommands {
		ev, err := runAt(ctx, r, r.Commit, "accept", c, m.d.Now)
		switch {
		case err != nil:
			log.Printf("verify: review %s: acceptance %q: %v", r.ID, c, err)
			add("Acceptance test failed: "+c, "", "The command could not run: "+err.Error())
		case ev.ExitCode != 0:
			add("Acceptance test failed: "+c, "", fmt.Sprintf("`%s` exited %d.\n\n```\n%s\n```", c, ev.ExitCode, ev.Output))
		}
	}
	return out
}
