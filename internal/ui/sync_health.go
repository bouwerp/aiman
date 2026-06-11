package ui

import (
	"context"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/mutagen"
	tea "github.com/charmbracelet/bubbletea"
)

// syncHealth describes the observed state of a session's mutagen sync.
type syncHealth struct {
	found    bool   // a mutagen sync matching the session exists
	status   string // raw mutagen status line (e.g. "Watching for changes")
	endpoint string // remote endpoint (user@host) the sync points at
	stale    bool   // sync is missing, unhealthy, or points at the wrong host
	reason   string // human-readable explanation when stale
}

type syncTickMsg time.Time

type syncHealthMsg struct {
	health map[string]syncHealth // keyed by session ID
	err    error
}

const syncHealthInterval = 30 * time.Second

func tickSyncHealth() tea.Cmd {
	return tea.Tick(syncHealthInterval, func(t time.Time) tea.Msg {
		return syncTickMsg(t)
	})
}

// healthySyncStatusPrefixes are mutagen statuses that indicate a live sync:
// either steady-state watching or an in-progress propagation phase.
var healthySyncStatusPrefixes = []string{
	"Watching",
	"Scanning",
	"Staging",
	"Reconciling",
	"Applying",
	"Saving",
	"Transitioning",
}

// isHealthySyncStatus reports whether a mutagen status string represents a
// functioning sync. Statuses like "Halted on root deletion", "Waiting 5
// seconds to retry", "Paused", or "Connecting to beta" indicate the sync can
// no longer (or not yet) propagate changes and is considered stale.
func isHealthySyncStatus(status string) bool {
	s := strings.TrimSpace(status)
	if s == "" {
		return false
	}
	// Conflicted syncs are still propagating non-conflicting changes; they
	// are surfaced via the status text rather than flagged stale.
	if strings.Contains(strings.ToLower(s), "conflict") {
		return true
	}
	for _, p := range healthySyncStatusPrefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// matchSyncSession finds the mutagen sync belonging to a session, matching by
// canonical name, stored sync ID, or the aiman-id label.
func matchSyncSession(s domain.Session, syncs []domain.SyncSession) *domain.SyncSession {
	canonical := "aiman-sync-" + s.ID
	labelValue := mutagen.SanitizeLabelValue(s.ID)
	for i := range syncs {
		ms := &syncs[i]
		if ms.Name == canonical || (s.MutagenSyncID != "" && (ms.Name == s.MutagenSyncID || ms.ID == s.MutagenSyncID)) {
			return ms
		}
		if labelValue != "" && ms.Labels != nil && ms.Labels["aiman-id"] == labelValue {
			return ms
		}
	}
	return nil
}

// endpointHost extracts the host part from a "user@host" endpoint string.
func endpointHost(endpoint string) string {
	if i := strings.LastIndex(endpoint, "@"); i >= 0 {
		return endpoint[i+1:]
	}
	return endpoint
}

// classifySyncHealth derives the health of one session's sync from the full
// mutagen sync list. expectedHost is the host of the session's currently
// configured remote ("" when unknown).
func classifySyncHealth(s domain.Session, syncs []domain.SyncSession, expectedHost string) syncHealth {
	ms := matchSyncSession(s, syncs)
	if ms == nil {
		return syncHealth{
			stale:  true,
			reason: "sync session not found",
		}
	}
	h := syncHealth{
		found:    true,
		status:   ms.Status,
		endpoint: ms.RemoteEndpoint,
	}
	if expectedHost != "" && ms.RemoteEndpoint != "" {
		if got := endpointHost(ms.RemoteEndpoint); got != expectedHost {
			h.stale = true
			h.reason = "sync targets old host " + got
			return h
		}
	}
	if !isHealthySyncStatus(ms.Status) {
		h.stale = true
		h.reason = ms.Status
		if h.reason == "" {
			h.reason = "sync has no status"
		}
	}
	return h
}

// checkSyncHealth lists local mutagen syncs and classifies the health of each
// session's sync. Sessions without a recorded sync are skipped (they already
// render a "no sync" marker).
func checkSyncHealth(cfg *config.Config, sessions []domain.Session) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		syncs, err := mutagen.NewEngine().ListSyncSessions(ctx)
		if err != nil {
			return syncHealthMsg{err: err}
		}

		health := make(map[string]syncHealth, len(sessions))
		for _, s := range sessions {
			if s.ID == "" || s.MutagenSyncID == "" {
				continue
			}
			expectedHost := ""
			if remote, ok := resolveRemote(cfg, s); ok {
				expectedHost = remote.Host
			}
			health[s.ID] = classifySyncHealth(s, syncs, expectedHost)
		}
		return syncHealthMsg{health: health}
	}
}
