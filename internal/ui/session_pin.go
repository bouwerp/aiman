package ui

import (
	"context"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
)

// pinnedBucketKey is the Pinned section's collapse key. It starts with NUL,
// which no group name can, so it never collides with a group called "pinned"
// and sorts ahead of every group.
const pinnedBucketKey = "\x00pinned"

// pinnedHeaderLabel is the Pinned section's header text.
const pinnedHeaderLabel = "★ pinned"

// pinStore is the laptop repository side of pins. Save never writes the pin,
// so the TUI writes it through this.
type pinStore interface {
	SetPinned(ctx context.Context, id string, pinned bool) error
}

// underPin reports whether the session or one of its listed ancestors is
// pinned. Such a session lists in the Pinned section with its pinned ancestor.
func underPin(it item, byID map[string]item) bool {
	cur := it
	seen := map[string]bool{}
	for {
		if cur.session.Pinned {
			return true
		}
		if cur.session.ParentID == "" || seen[cur.session.ID] {
			return false
		}
		seen[cur.session.ID] = true
		p, ok := byID[cur.session.ParentID]
		if !ok {
			return false
		}
		cur = p
	}
}

// listBucketKey is the section a row lists under: Pinned, else its home group.
func listBucketKey(it item, byID map[string]item) string {
	if underPin(it, byID) {
		return pinnedBucketKey
	}
	return groupHomeKey(it, byID)
}

// pinnedFirst moves the Pinned section to the front of the sorted keys.
func pinnedFirst(order []string) []string {
	out := make([]string, 0, len(order))
	for _, k := range order {
		if k == pinnedBucketKey {
			out = append(out, k)
		}
	}
	for _, k := range order {
		if k != pinnedBucketKey {
			out = append(out, k)
		}
	}
	return out
}

// headerKey is the collapse key of a header row.
func (i item) headerKey() string {
	if i.pinned {
		return pinnedBucketKey
	}
	return groupCollapseKey(i.session.Group, i.remoteName)
}

// toggleSessionPin flips the session's pin in memory and the laptop DB, and
// returns the command that tells its serve. A session still being created has
// no row yet and is left alone.
func (m *Model) toggleSessionPin(id string) tea.Cmd {
	if id == "" || domain.IsEphemeralSessionID(id) {
		return nil
	}
	for i, s := range m.allSessions {
		if s.ID != id {
			continue
		}
		pinned := !s.Pinned
		if ps, ok := m.db.(pinStore); ok {
			if err := ps.SetPinned(context.Background(), id, pinned); err != nil {
				m.log("pin %s: %v", id, err)
			}
		}
		m.allSessions[i].Pinned = pinned
		m.applyRemoteFilter()
		return m.syncIdentityToServe(s.RemoteHost, []identityChange{{method: "session.pin", id: id, pinned: pinned}})
	}
	return nil
}

// applyServePin takes serve's pin for a session it announced. Serve's row is
// the shared one, so a pin set or cleared from another client lands here.
func (m *Model) applyServePin(id string, pinned bool) {
	for i, s := range m.allSessions {
		if s.ID != id {
			continue
		}
		if s.Pinned == pinned {
			return
		}
		m.allSessions[i].Pinned = pinned
		if ps, ok := m.db.(pinStore); ok && !domain.IsEphemeralSessionID(id) {
			if err := ps.SetPinned(context.Background(), id, pinned); err != nil {
				m.log("pin %s: %v", id, err)
			}
		}
		m.maybeRefreshSessionList()
		return
	}
}
