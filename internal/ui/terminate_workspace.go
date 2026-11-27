package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/usecase"
)

// The TUI's own worktree teardown keys on RepoName, which a workspace session
// leaves empty, and a workspace holds one worktree per member repository. Serve
// knows the members, checks each for unsaved work before anything stops, stops
// the terminal, removes the member worktrees and the workspace directory, and
// drops its row. So a workspace session is terminated by serve's
// session.terminate, and the TUI keeps only its laptop-side steps.

// terminateServeTimeout is past serve's SessionTerminateTimeout (3 minutes).
const terminateServeTimeout = 4 * time.Minute

// workspaceServeStep is the index of the session.terminate step in
// workspaceTerminateStepLabels.
const workspaceServeStep = 1

// terminateViaServe reports whether s is torn down by serve's session.terminate
// rather than the TUI's own steps: true only for a workspace session.
func terminateViaServe(s domain.Session) bool {
	return s.IsWorkspace()
}

// terminateServeParams is the session.terminate call for s. A forced terminate
// discards unsaved work. Children are kept because the TUI's batch tears each
// one down itself.
func terminateServeParams(s domain.Session, forced bool) map[string]any {
	return map[string]any{"session": s.ID, "discard": forced, "keep_children": true}
}

// workspaceTerminateStepLabels are the steps of a workspace teardown. Serve's
// call comes before the mutagen stop, so a refusal for unsaved work leaves the
// session as it was.
func workspaceTerminateStepLabels(forced bool) []string {
	serve := "Terminating on host (terminal, member worktrees)"
	if forced {
		serve = "Terminating on host, discarding changes (force)"
	}
	return []string{
		"Saving session context",
		serve,
		"Stopping mutagen sync",
		"Cleaning local files",
		"Cleaning up AWS credentials",
		"Updating session status",
	}
}

// runWorkspaceTerminateStep performs one step of a workspace teardown.
func (m *Model) runWorkspaceTerminateStep(index int, s domain.Session, forced bool) error {
	ctx := context.Background()
	switch index {
	case 0:
		// Serve's session.terminate writes the forget handover itself, while
		// the worktrees still exist, so only the pane snapshot is taken here.
		return m.terminateSnapshotPane(ctx, s)
	case workspaceServeStep:
		return m.terminateOnServe(ctx, s, forced)
	case 2:
		return m.terminateStopSync(ctx, s)
	case 3:
		return m.terminateRemoveLocalFiles(s)
	case 4:
		return m.terminateCleanCredentials(ctx, s)
	case 5:
		// Serve dropped its row in session.terminate; only the laptop's is left.
		if s.ID == "" || m.db == nil {
			return nil
		}
		return m.db.Delete(ctx, s.ID)
	}
	return nil
}

// terminateServeResult is the part of session.terminate's answer the TUI reads.
type terminateServeResult struct {
	WorktreeRemoved bool   `json:"worktree_removed"`
	WorktreeNote    string `json:"worktree_note"`
}

// terminateOnServe calls session.terminate for s. A worktree serve left in
// place is a skip note; any refusal (worktree_dirty included) is an error
// carrying serve's message.
func (m *Model) terminateOnServe(ctx context.Context, s domain.Session, forced bool) error {
	remote, ok := resolveRemote(m.cfg, s)
	if !ok {
		return fmt.Errorf("no remote configured for host %q", s.RemoteHost)
	}
	callCtx, cancel := context.WithTimeout(ctx, terminateServeTimeout)
	defer cancel()
	raw, err := sessionIdentityRPC(callCtx, remote, "session.terminate", terminateServeParams(s, forced))
	if err != nil {
		if errors.Is(err, usecase.ErrRPCUnsupported) {
			return fmt.Errorf("the host's aiman is too old to terminate a workspace session; update it from the Agent API page")
		}
		return err
	}
	var res terminateServeResult
	if json.Unmarshal(raw, &res) == nil && !res.WorktreeRemoved && res.WorktreeNote != "" {
		return skipReason(res.WorktreeNote)
	}
	return nil
}

// abortWorkspaceTerminate ends a workspace teardown whose session.terminate
// step failed: serve stopped nothing, so the session stays listed and the
// later laptop steps do not run. It reports whether it handled msg.
func (m *Model) abortWorkspaceTerminate(ts *terminatingSession, msg terminateStepMsg) (tea.Cmd, bool) {
	if !terminateViaServe(ts.session) || msg.index != workspaceServeStep || msg.err == nil {
		return nil, false
	}
	var skip skipReason
	if errors.As(msg.err, &skip) {
		return nil, false
	}
	delete(m.terminatingSessions, msg.sessionID)
	m.applyRemoteFilter()
	return m.showToast(fmt.Sprintf("❌ Session %s not terminated: %v", sessionDisplayName(ts.session), msg.err), true, 10*time.Second), true
}
