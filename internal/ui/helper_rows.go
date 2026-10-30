package ui

import (
	"context"
	"regexp"

	"github.com/bouwerp/aiman/internal/domain"
)

// handoverChildTerminalRe matches the terminal of a handover child: the
// branch handover-<first 8 hex of the run id>, with the suffix labelling adds
// when two share it.
var handoverChildTerminalRe = regexp.MustCompile(`^handover-[0-9a-f]{8}(-[0-9]+)?$`)

// staleHandoverRow reports whether a stored row is a handover child that was
// announced before serve knew its role. Such rows carry no role, so the role
// filter cannot hide them; they are recognised by the child's terminal name.
func staleHandoverRow(s domain.Session) bool {
	if s.Role != "" {
		return false
	}
	return handoverChildTerminalRe.MatchString(s.Name) || handoverChildTerminalRe.MatchString(s.TerminalName)
}

// dropGoneHelperRow removes a hidden helper's row, in memory and in the
// laptop DB, once serve reports its terminal gone. Any other row, a visible
// child included, stays listed for a scan to judge: the user may revive it.
func (m *Model) dropGoneHelperRow(id string) {
	idx := -1
	for i, s := range m.allSessions {
		if s.ID == id && s.Role.Hidden() {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	m.allSessions = append(m.allSessions[:idx], m.allSessions[idx+1:]...)
	if m.db != nil {
		if err := m.db.Delete(context.Background(), id); err != nil {
			m.log("session_gone: delete helper row %s: %v", id, err)
		}
	}
	m.maybeRefreshSessionList()
}
