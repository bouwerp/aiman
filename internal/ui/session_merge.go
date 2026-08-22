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

// overlayPersistedSessionFields copies identity the live remote scan cannot
// see. Discovery never reads Name or Group; without this, a restart flattens
// every session into ungrouped until the user edits a group again.
func overlayPersistedSessionFields(live, stored domain.Session) domain.Session {
	if live.Name == "" {
		live.Name = stored.Name
	}
	if live.Group == "" {
		live.Group = stored.Group
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
	return live
}
