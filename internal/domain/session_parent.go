package domain

import (
	"errors"
	"strings"
)

var (
	ErrParentSelf    = errors.New("session cannot be its own parent")
	ErrParentUnknown = errors.New("parent session not found")
	ErrParentCycle   = errors.New("parent would create a cycle")
)

// ValidateParent reports whether parentID is a legal parent for id.
// An empty parentID is a root.
func ValidateParent(id, parentID string, sessions []Session) error {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return nil
	}
	id = strings.TrimSpace(id)
	if id != "" && parentID == id {
		return ErrParentSelf
	}
	byID := make(map[string]Session, len(sessions))
	for _, s := range sessions {
		if s.ID == "" {
			continue
		}
		byID[s.ID] = s
	}
	if _, ok := byID[parentID]; !ok {
		return ErrParentUnknown
	}
	seen := map[string]bool{}
	if id != "" {
		seen[id] = true
	}
	cur := parentID
	for cur != "" {
		if seen[cur] {
			return ErrParentCycle
		}
		seen[cur] = true
		p, ok := byID[cur]
		if !ok {
			return nil
		}
		cur = strings.TrimSpace(p.ParentID)
	}
	return nil
}

// ResolveCreateParent picks the parent for a new session.
// orphan wins. explicit --parent wins over the RPC caller (AIMAN_ID).
func ResolveCreateParent(orphan bool, explicit, caller, newID string, sessions []Session) (string, error) {
	if orphan {
		return "", nil
	}
	explicit = strings.TrimSpace(explicit)
	if explicit != "" {
		if err := ValidateParent(newID, explicit, sessions); err != nil {
			return "", err
		}
		return explicit, nil
	}
	caller = strings.TrimSpace(caller)
	if caller == "" {
		return "", nil
	}
	if err := ValidateParent(newID, caller, sessions); err != nil {
		// AIMAN_ID may be set on a machine that is not this session's parent
		// (tests, a laptop CLI, a deleted parent). Do not fail the create.
		return "", nil
	}
	return caller, nil
}
