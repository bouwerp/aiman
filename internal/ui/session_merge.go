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

func sessionTmuxKey(s domain.Session) string {
	if s.TmuxSession == "" {
		return ""
	}
	return s.RemoteHost + "\x00" + s.TmuxSession
}

// creatingPlaceholderMatchesLive reports whether a discovered session is the
// in-flight create represented by placeholder. Placeholder IDs are ephemeral
// (`pending-*`) and tmux names are often unsanitized branch labels, so ID and
// exact tmux equality both miss.
func creatingPlaceholderMatchesLive(placeholder, live domain.Session) bool {
	if placeholder.RemoteHost == "" || live.RemoteHost == "" || placeholder.RemoteHost != live.RemoteHost {
		return false
	}
	if live.ID == "" || live.ID == placeholder.ID {
		return false
	}
	phTmux := domain.SanitizeTmuxSessionName(placeholder.TmuxSession)
	liveTmux := domain.SanitizeTmuxSessionName(live.TmuxSession)
	if phTmux != "" && phTmux == liveTmux {
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

func persistedByTmux(sessions map[string]domain.Session) map[string]domain.Session {
	out := make(map[string]domain.Session, len(sessions))
	for _, s := range sessions {
		if k := sessionTmuxKey(s); k != "" {
			out[k] = s
		}
	}
	return out
}

func lookupPersistedSession(live domain.Session, byID, byTmux map[string]domain.Session) (domain.Session, bool) {
	if s, ok := byID[live.ID]; ok {
		return s, true
	}
	if k := sessionTmuxKey(live); k != "" {
		s, ok := byTmux[k]
		return s, ok
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
	if live.AgentSessionID == "" {
		live.AgentSessionID = stored.AgentSessionID
	}
	if live.AgentSessionPath == "" {
		live.AgentSessionPath = stored.AgentSessionPath
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
	return live
}
