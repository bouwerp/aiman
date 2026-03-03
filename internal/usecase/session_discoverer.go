package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

type SessionDiscoverer struct {
	remoteExecutor domain.RemoteExecutor
	syncEngine     domain.SyncEngine
}

func NewSessionDiscoverer(remoteExecutor domain.RemoteExecutor, syncEngine domain.SyncEngine) *SessionDiscoverer {
	return &SessionDiscoverer{
		remoteExecutor: remoteExecutor,
		syncEngine:     syncEngine,
	}
}

func (d *SessionDiscoverer) Discover(ctx context.Context, host string) ([]domain.Session, error) {
	// 1. Scan tmux sessions
	tmuxSessions, err := d.remoteExecutor.ScanTmuxSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to scan tmux sessions: %w", err)
	}

	// 2. Get mutagen sessions
	mutagenSessions, _ := d.syncEngine.ListSyncSessions(ctx)

	sessions := []domain.Session{}

	for _, name := range tmuxSessions {
		session := domain.Session{
			TmuxSession: name,
			RemoteHost:  host,
			Status:      domain.SessionStatusActive,
			CreatedAt:   time.Now(), // Approximate
		}

		// 3. Get CWD
		cwd, err := d.remoteExecutor.GetTmuxSessionCWD(ctx, name)
		if err == nil {
			session.WorktreePath = cwd
		}

		// 4. Extract JIRA key from session name
		key := domain.ExtractKey(name)
		if key == "" && cwd != "" {
			// Try extracting from CWD path
			key = domain.ExtractKey(cwd)
		}
		session.IssueKey = key

		// 5. Try to determine repo name from CWD
		if cwd != "" {
			parts := strings.Split(cwd, "/")
			if len(parts) > 0 {
				session.RepoName = parts[len(parts)-1]
			}
		}

		// 6. Cross-reference with mutagen
		if cwd != "" {
			for _, ms := range mutagenSessions {
				// Mutagen remote path might be user@host:path or just path depending on how it was created
				// We do a simple suffix check or contains check for now
				if strings.Contains(ms.RemotePath, cwd) {
					session.MutagenSyncID = ms.ID
					session.LocalPath = ms.LocalPath
					session.Status = domain.SessionStatusSyncing
					break
				}
			}
		}

		sessions = append(sessions, session)
	}

	return sessions, nil
}
