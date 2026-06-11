package ui

import (
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
)

// terminatingSession tracks a session being terminated in the background.
// The session stays in the list marked "terminating…" while the cleanup
// steps run; its preview panel shows step-by-step progress.
type terminatingSession struct {
	session domain.Session
	forced  bool
	steps   []string
	errs    []string // one slot per step; "" = no error
	idx     int      // index of the step currently running
}

// isTerminatingSession reports whether the session is being terminated in
// the background.
func (m *Model) isTerminatingSession(id string) bool {
	_, ok := m.terminatingSessions[id]
	return ok
}

// renderTerminatingPanel renders the preview panel for a session whose
// termination is running in the background.
func (m *Model) renderTerminatingPanel(ts *terminatingSession, contentW int) string {
	var b strings.Builder
	b.WriteString(activeStyle.Render("Session") + "  " + failStyle.Render("terminating in background…") + "\n")

	var meta []string
	if ts.session.RepoName != "" {
		meta = append(meta, ts.session.RepoName)
	}
	if ts.session.RemoteHost != "" {
		meta = append(meta, ts.session.RemoteHost)
	}
	if ts.session.IssueKey != "" {
		meta = append(meta, ts.session.IssueKey)
	}
	if len(meta) > 0 {
		b.WriteString(truncateRunes(strings.Join(meta, " · "), contentW) + "\n")
	}

	b.WriteString(statusStyle.Render(strings.Repeat("─", max(1, contentW))) + "\n")

	for i, step := range ts.steps {
		status := "[ ]"
		if i < ts.idx {
			status = successStyle.Render("[✓]")
		} else if i == ts.idx {
			status = activeStyle.Render("[→]")
		}
		if i < len(ts.errs) && ts.errs[i] != "" {
			status = failStyle.Render("[✗]")
		}
		b.WriteString(fmt.Sprintf("%s %s\n", status, truncateRunes(step, max(10, contentW-4))))
		if i < len(ts.errs) && ts.errs[i] != "" {
			b.WriteString("    " + failStyle.Render(truncateRunes(ts.errs[i], max(10, contentW-4))) + "\n")
		}
	}

	b.WriteString("\n" + statusStyle.Render("You can switch to and use other sessions while this one is terminated.") + "\n")
	return b.String()
}

// terminateStepLabels returns the cleanup step labels for a termination.
func terminateStepLabels(forced bool) []string {
	if forced {
		return []string{
			"Stopping mutagen sync",
			"Discarding changes (force)",
			"Killing tmux session",
			"Stopping agent process",
			"Removing git worktree",
			"Cleaning local files",
			"Cleaning up AWS credentials",
			"Updating session status",
		}
	}
	return []string{
		"Stopping mutagen sync",
		"Killing tmux session",
		"Stopping agent process",
		"Removing git worktree",
		"Cleaning local files",
		"Cleaning up AWS credentials",
		"Updating session status",
	}
}

// startBackgroundTerminate registers the termination, keeps the user on the
// dashboard, and kicks off the first cleanup step in the background.
func (m *Model) startBackgroundTerminate(s domain.Session, forced bool) tea.Cmd {
	ts := &terminatingSession{
		session: s,
		forced:  forced,
		steps:   terminateStepLabels(forced),
	}
	ts.errs = make([]string, len(ts.steps))
	m.terminatingSessions[s.ID] = ts
	m.applyRemoteFilter() // refresh list so the item shows "terminating…"
	m.state = viewStateMain
	return m.runTerminateStepCmd(s, forced, 0)
}
