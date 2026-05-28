package ui

import "github.com/bouwerp/aiman/internal/domain"

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
