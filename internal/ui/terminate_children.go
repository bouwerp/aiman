package ui

import (
	"strings"

	"fmt"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
)

// terminationChildren returns every session descended from s, deepest first.
//
// Order matters on the way out: a child torn down before its parent cannot be
// re-adopted by a parent that is already gone, and the deepest sessions are the
// ones with nothing depending on them.
//
// Sessions already being terminated are left out — they are on their way down
// anyway, and registering them twice would run every step against them twice.
func (m *Model) terminationChildren(s domain.Session) []domain.Session {
	if s.ID == "" {
		return nil
	}
	byParent := map[string][]domain.Session{}
	for _, c := range m.allSessions {
		if c.ParentID != "" && c.ID != c.ParentID {
			byParent[c.ParentID] = append(byParent[c.ParentID], c)
		}
	}
	seen := map[string]bool{s.ID: true}
	var collect func(string) []domain.Session
	collect = func(id string) []domain.Session {
		var out []domain.Session
		for _, kid := range byParent[id] {
			if seen[kid.ID] {
				continue
			}
			seen[kid.ID] = true
			// Depth first, so descendants land ahead of the child they hang off.
			out = append(out, collect(kid.ID)...)
			if _, terminating := m.terminatingSessions[kid.ID]; !terminating {
				out = append(out, kid)
			}
		}
		return out
	}
	return collect(s.ID)
}

// startTerminationBatch tears down the selected session, and its children too
// when the confirm dialog's tick box was set.
//
// Children run first for the same reason they are collected deepest-first: the
// parent's teardown removes the record they hang off.
func (m *Model) startTerminationBatch(s domain.Session, forced bool) tea.Cmd {
	var cmds []tea.Cmd
	if m.terminateWithChildren {
		for _, kid := range m.terminationChildren(s) {
			if cmd := m.startBackgroundTerminate(kid, forced); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}
	m.terminateWithChildren = false
	if cmd := m.startBackgroundTerminate(s, forced); cmd != nil {
		cmds = append(cmds, cmd)
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

func pluralSessions(n int) string {
	if n == 1 {
		return "1 child session"
	}
	return fmt.Sprintf("%d child sessions", n)
}

// worktreeStillInUse reports whether a session other than s — and one that is
// not itself being torn down — works in the same directory, naming it.
//
// A child session created by an in-pane agent usually shares its parent's
// worktree, so without this a single teardown could delete the tree a live
// sibling is working in.
func (m *Model) worktreeStillInUse(s domain.Session) (string, bool) {
	path := strings.TrimSpace(s.WorktreePath)
	if path == "" {
		return "", false
	}
	for _, other := range m.allSessions {
		if other.ID == s.ID || strings.TrimSpace(other.WorktreePath) != path {
			continue
		}
		if _, terminating := m.terminatingSessions[other.ID]; terminating {
			continue
		}
		name := other.Name
		if name == "" {
			name = other.TmuxSession
		}
		if name == "" {
			name = other.ID
		}
		return name, true
	}
	return "", false
}
