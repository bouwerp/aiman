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
			normalizedCWD := strings.TrimSuffix(cwd, "/")
			for _, ms := range mutagenSessions {
				// Normalized remote path from mutagen
				normalizedRemote := strings.TrimSuffix(ms.RemotePath, "/")
				normalizedLocal := strings.TrimSuffix(ms.LocalPath, "/")

				// DEBUG: fmt.Printf("Checking CWD %s against Alpha: %s, Beta: %s\n", normalizedCWD, normalizedLocal, normalizedRemote)

				// In Mutagen, either Alpha or Beta could be the remote.
				if normalizedRemote == normalizedCWD || normalizedLocal == normalizedCWD || 
				   strings.HasSuffix(normalizedRemote, normalizedCWD) || strings.HasSuffix(normalizedLocal, normalizedCWD) {
					
					// We need to identify which one is actually local (starts with /Users or /) 
					// and doesn't look like a connection string
					if !strings.Contains(ms.LocalPath, ":") {
						session.LocalPath = ms.LocalPath
					} else if !strings.Contains(ms.RemotePath, ":") {
						session.LocalPath = ms.RemotePath
					}

					session.MutagenSyncID = ms.ID
					session.Status = domain.SessionStatusSyncing
					break
				}
			}
		}

		sessions = append(sessions, session)
	}

	return sessions, nil
}
