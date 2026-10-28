package ui

import (
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
)

func shouldMergeDiscoveredSession(s domain.Session, dbSessions map[string]domain.Session) bool {
	if s.Status != domain.SessionStatusInactive {
		return true
	}
	if s.ID == "" {
		return false
	}
	_, ok := dbSessions[s.ID]
	return ok
}

// previewKey identifies a session in the preview, activity and git status
// messages. Terminal names are not unique (two children can share one), so the
// id wins and the name is only the fallback for a session without one.
func previewKey(s domain.Session) string {
	if s.ID != "" {
		return s.ID
	}
	return s.TerminalName
}

// sameTerminal reports whether two rows with the same host and terminal name
// are one session. PTY names are labels, so two PTY sessions with ids of their
// own stay distinct.
func sameTerminal(a, b domain.Session) bool {
	if a.Backend == domain.BackendPTY && b.Backend == domain.BackendPTY && a.ID != "" && b.ID != "" {
		return a.ID == b.ID
	}
	return true
}

func sessionTerminalKey(s domain.Session) string {
	if s.TerminalName == "" {
		return ""
	}
	return s.RemoteHost + "\x00" + s.TerminalName
}

// creatingPlaceholderMatchesLive reports whether a discovered session is the
// in-flight create represented by placeholder. Placeholder IDs are ephemeral
// (`pending-*`) and terminal names are often unsanitized branch labels, so ID
// and exact terminal-name equality both miss.
func creatingPlaceholderMatchesLive(placeholder, live domain.Session) bool {
	if placeholder.RemoteHost == "" || live.RemoteHost == "" || placeholder.RemoteHost != live.RemoteHost {
		return false
	}
	if live.ID == "" || live.ID == placeholder.ID {
		return false
	}
	phTerminal := domain.TerminalNameFromBranch(placeholder.TerminalName)
	liveTerminal := domain.TerminalNameFromBranch(live.TerminalName)
	if phTerminal != "" && phTerminal == liveTerminal {
		return true
	}
	if placeholder.Branch == "" || placeholder.Branch != live.Branch {
		return false
	}
	if placeholder.RepoName == "" || live.RepoName == "" {
		return true
	}
	return placeholder.RepoName == live.RepoName
}

func upsertSessionReplacing(sessions []domain.Session, placeholderID string, live domain.Session) []domain.Session {
	out := make([]domain.Session, 0, len(sessions))
	replacedLive := false
	for _, s := range sessions {
		if s.ID == placeholderID {
			continue
		}
		if live.ID != "" && s.ID == live.ID {
			if replacedLive {
				continue
			}
			merged := overlayPersistedSessionFields(live, s)
			merged.ID = live.ID
			out = append(out, merged)
			replacedLive = true
			continue
		}
		out = append(out, s)
	}
	if !replacedLive {
		out = append(out, live)
	}
	return out
}

func persistedByTerminal(sessions map[string]domain.Session) map[string]domain.Session {
	out := make(map[string]domain.Session, len(sessions))
	for _, s := range sessions {
		if k := sessionTerminalKey(s); k != "" {
			out[k] = s
		}
	}
	return out
}

func lookupPersistedSession(live domain.Session, byID, byTerminal map[string]domain.Session) (domain.Session, bool) {
	if s, ok := byID[live.ID]; ok {
		return s, true
	}
	if k := sessionTerminalKey(live); k != "" {
		if s, ok := byTerminal[k]; ok && sameTerminal(live, s) {
			return s, true
		}
	}
	return domain.Session{}, false
}

// overlayPersistedSessionFields copies identity the live remote scan cannot
// see. Discovery never reads Name or Group; without this, a restart flattens
// every session into ungrouped until the user edits a group again.
func overlayPersistedSessionFields(live, stored domain.Session) domain.Session {
	if live.Backend == "" {
		live.Backend = stored.Backend
	}
	if live.Name == "" {
		live.Name = stored.Name
	}
	if live.Group == "" {
		live.Group = stored.Group
	}
	if live.ParentID == "" {
		live.ParentID = stored.ParentID
	}
	if live.Role == "" {
		live.Role = stored.Role
	}
	if stored.WorkingDirectory != "" {
		live.WorkingDirectory = stored.WorkingDirectory
	}
	if stored.RepoName != "" && (live.RepoName == "" || (!strings.Contains(live.RepoName, "/") && strings.Contains(stored.RepoName, "/"))) {
		live.RepoName = stored.RepoName
	}
	if live.IssueKey == "" {
		live.IssueKey = stored.IssueKey
	}
	if live.Branch == "" {
		live.Branch = stored.Branch
	}
	if live.AgentName == "" {
		live.AgentName = stored.AgentName
	}
	if live.AgentModel == "" {
		live.AgentModel = stored.AgentModel
	}
	if live.WorktreePath == "" {
		live.WorktreePath = stored.WorktreePath
	}
	if live.LocalPath == "" {
		live.LocalPath = stored.LocalPath
	}
	if live.MutagenSyncID == "" {
		live.MutagenSyncID = stored.MutagenSyncID
	}
	sameAgent := live.AgentName == "" || stored.AgentName == "" ||
		strings.EqualFold(live.AgentName, stored.AgentName)
	if sameAgent {
		if live.AgentSessionID == "" {
			live.AgentSessionID = stored.AgentSessionID
		}
		if live.AgentSessionPath == "" {
			live.AgentSessionPath = stored.AgentSessionPath
		}
		if live.LaunchModel == "" && live.LaunchEffort == "" {
			live.LaunchModel = stored.LaunchModel
			live.LaunchEffort = stored.LaunchEffort
		}
		if live.HookState == "" && live.HookStateAt.IsZero() && !live.AgentEnded {
			live.AgentTitle = stored.AgentTitle
			live.AgentEnded = stored.AgentEnded
			live.HookState = stored.HookState
			live.HookStateMessage = stored.HookStateMessage
			live.HookStateSource = stored.HookStateSource
			live.HookStateSeq = stored.HookStateSeq
			live.HookStateAt = stored.HookStateAt
		} else if live.AgentTitle == "" {
			live.AgentTitle = stored.AgentTitle
		}
	}
	return live
}
