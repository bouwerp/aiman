package ui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/usecase"
)

// verifyStartedMsg is the answer to verify.start.
type verifyStartedMsg struct {
	sessionID string
	status    string
	err       error
}

// reviewerRunning reports a live reviewer child for workerID.
func reviewerRunning(workerID string, all []domain.Session) bool {
	for _, s := range all {
		if s.Role == domain.SessionRoleReviewer && s.ParentID == workerID && !s.AgentEnded {
			return true
		}
	}
	return false
}

// startVerify asks serve to review the selected worker.
func (m *Model) startVerify() (tea.Model, tea.Cmd, bool) {
	it, ok := m.selectedSessionItem()
	if !ok {
		return m, nil, true
	}
	s := it.session
	if s.Role == domain.SessionRoleReviewer {
		return m, m.showToast("⚠️  A reviewer session cannot start another review.", true, 4*time.Second), true
	}
	remote, ok := resolveRemote(m.cfg, s)
	if !ok {
		return m, m.showToast("⚠️  No remote is configured for this session.", true, 4*time.Second), true
	}
	mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		status, err := usecase.StartVerifyOnRemote(ctx, mgr, s.ID)
		return verifyStartedMsg{sessionID: s.ID, status: status, err: err}
	}, true
}

// applyVerifyStarted acknowledges a successful start or reports the error.
func (m *Model) applyVerifyStarted(msg verifyStartedMsg) tea.Cmd {
	if msg.err != nil {
		return m.showToast(fmt.Sprintf("⚠️  Verify: %v", msg.err), true, 6*time.Second)
	}
	switch msg.status {
	case "starting", "running":
		return m.showToast("Review starting…", false, 3*time.Second)
	case "disabled":
		return m.showToast("Verify is disabled on this host.", false, 3*time.Second)
	default:
		return m.showToast("Existing review: "+msg.status, false, 3*time.Second)
	}
}

// dropReviewerRows removes finished reviewer children for workerID so the
// reviewing marker clears even when the reviewer emits no session_gone.
func (m *Model) dropReviewerRows(workerID string) {
	if workerID == "" {
		return
	}
	kept := m.allSessions[:0]
	var removed []string
	for _, s := range m.allSessions {
		if s.Role == domain.SessionRoleReviewer && s.ParentID == workerID {
			removed = append(removed, s.ID)
			continue
		}
		kept = append(kept, s)
	}
	m.allSessions = kept
	if m.db != nil {
		for _, id := range removed {
			if err := m.db.Delete(context.Background(), id); err != nil {
				m.log("verify: delete reviewer row %s: %v", id, err)
			}
		}
	}
	if m.list.Height() > 0 || m.list.Width() > 0 {
		m.applyRemoteFilter()
	}
}
