package usecase

import (
	"context"
	"fmt"
	"path/filepath"
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
	seenWorktrees := make(map[string]bool)
	seenTmuxNames := make(map[string]bool)
	seenMutagenIDs := make(map[string]bool)

	// Process tmux sessions
	for _, name := range tmuxSessions {
		session := d.discoverSession(ctx, host, name, mutagenSessions)
		if session.WorktreePath != "" {
			seenWorktrees[session.WorktreePath] = true
		}
		if session.TmuxSession != "" {
			seenTmuxNames[session.TmuxSession] = true
		}
		if session.MutagenSyncID != "" {
			seenMutagenIDs[session.MutagenSyncID] = true
		}
		sessions = append(sessions, session)
	}

	// 3. Scan for orphaned worktrees
	repos, err := d.remoteExecutor.ScanGitRepos(ctx)
	if err == nil {
		for _, repoPath := range repos {
			worktrees, err := d.remoteExecutor.ScanWorktrees(ctx, repoPath)
			if err == nil {
				for _, wtPath := range worktrees {
					normalizedWT := normalizePath(wtPath)
					wtBase := filepath.Base(normalizedWT)
					if !seenWorktrees[normalizedWT] && !seenTmuxNames[wtBase] {
						// Found an orphaned worktree
						session := domain.Session{
							TmuxSession:  wtBase,
							RemoteHost:   host,
							Status:       domain.SessionStatusInactive,
							WorktreePath: normalizedWT,
							WorkingDirectory: normalizedWT,
							CreatedAt:    time.Now(),
						}

						// Try to read .aiman-id from worktree root
						id, err := d.remoteExecutor.Execute(ctx, fmt.Sprintf("cat %s/.aiman-id", normalizedWT))
						if err == nil {
							session.ID = strings.TrimSpace(id)
						}

						// Try to determine repo name
						parts := strings.Split(repoPath, "/")
						if len(parts) > 0 {
							session.RepoName = parts[len(parts)-1]
						}

						// Extract JIRA key
						session.IssueKey = domain.ExtractKey(session.TmuxSession)
						if session.IssueKey == "" {
							session.IssueKey = domain.ExtractKey(normalizedWT)
						}

						// Cross-reference with mutagen
						for _, ms := range mutagenSessions {
							if !seenMutagenIDs[ms.ID] && d.isSessionMatch(session, ms, normalizedWT) {
								if !strings.Contains(ms.LocalPath, ":") {
									session.LocalPath = normalizePath(ms.LocalPath)
								} else if !strings.Contains(ms.RemotePath, ":") {
									session.LocalPath = normalizePath(ms.RemotePath)
								}
								session.MutagenSyncID = ms.ID
								seenMutagenIDs[ms.ID] = true
								break
							}
						}

						sessions = append(sessions, session)
						seenWorktrees[normalizedWT] = true
						seenTmuxNames[wtBase] = true
					}
				}
			}
		}
	}

	// 4. Scan for orphaned mutagen syncs that don't match any worktree
	for _, ms := range mutagenSessions {
		if !seenMutagenIDs[ms.ID] {
			// This sync session doesn't match any tmux session or worktree we found
			remotePath := ""
			if strings.Contains(ms.RemotePath, ":") {
				// ms.RemotePath is the remote URL
				parts := strings.SplitN(ms.RemotePath, ":", 2)
				if len(parts) > 1 {
					remotePath = normalizePath(parts[1])
				}
			} else if strings.Contains(ms.LocalPath, ":") {
				// ms.LocalPath is the remote URL
				parts := strings.SplitN(ms.LocalPath, ":", 2)
				if len(parts) > 1 {
					remotePath = normalizePath(parts[1])
				}
			}

			if remotePath != "" {
				session := domain.Session{
					TmuxSession:   ms.Name,
					RemoteHost:    host,
					Status:        domain.SessionStatusInactive,
					WorktreePath:  remotePath,
					WorkingDirectory: remotePath,
					MutagenSyncID: ms.ID,
					CreatedAt:     time.Now(),
				}
				if !strings.Contains(ms.LocalPath, ":") {
					session.LocalPath = normalizePath(ms.LocalPath)
				} else if !strings.Contains(ms.RemotePath, ":") {
					session.LocalPath = normalizePath(ms.RemotePath)
				}
				session.IssueKey = domain.ExtractKey(ms.Name)
				sessions = append(sessions, session)
			}
		}
	}

	return sessions, nil
}

func (d *SessionDiscoverer) discoverSession(ctx context.Context, host string, name string, mutagenSessions []domain.SyncSession) domain.Session {
	session := domain.Session{
		TmuxSession: name,
		RemoteHost:  host,
		Status:      domain.SessionStatusActive,
		CreatedAt:   time.Now(), // Approximate
	}

	// 2. Get AIMAN_ID from tmux env
	aimanID, _ := d.remoteExecutor.GetTmuxSessionEnv(ctx, name, "AIMAN_ID")
	if aimanID != "" {
		session.ID = aimanID
	}

	// 3. Get CWD and Git Root
	cwd, err := d.remoteExecutor.GetTmuxSessionCWD(ctx, name)
	if err == nil {
		normalizedCWD := normalizePath(cwd)
		session.WorkingDirectory = normalizedCWD
		// Try to find the git root of the CWD
		gitRoot, err := d.remoteExecutor.GetGitRoot(ctx, normalizedCWD)
		if err == nil {
			session.WorktreePath = normalizePath(gitRoot)
		} else {
			session.WorktreePath = normalizedCWD
		}
	}

	// 4. Try reading .aiman-id from worktree root if not found in env
	if session.WorktreePath != "" && session.ID == "" {
		id, err := d.remoteExecutor.Execute(ctx, fmt.Sprintf("cat %s/.aiman-id", session.WorktreePath))
		if err == nil {
			session.ID = strings.TrimSpace(id)
		}
	}

	// 5. Extract JIRA key from session name
	key := domain.ExtractKey(name)
	if key == "" && session.WorktreePath != "" {
		// Try extracting from WorktreePath
		key = domain.ExtractKey(session.WorktreePath)
	}
	session.IssueKey = key

	// 6. Try to determine repo name from WorktreePath
	if session.WorktreePath != "" {
		parts := strings.Split(session.WorktreePath, "/")
		if len(parts) > 0 {
			session.RepoName = parts[len(parts)-1]
		}
	}

	// 7. Cross-reference with mutagen
	if session.WorktreePath != "" {
		normalizedPath := session.WorktreePath
		for _, ms := range mutagenSessions {
			if d.isSessionMatch(session, ms, normalizedPath) {
				// We need to identify which one is actually local
				if !strings.Contains(ms.LocalPath, ":") {
					session.LocalPath = normalizePath(ms.LocalPath)
				} else if !strings.Contains(ms.RemotePath, ":") {
					session.LocalPath = normalizePath(ms.RemotePath)
				}
				session.MutagenSyncID = ms.ID
				session.Status = domain.SessionStatusSyncing
				break
			}
		}
	}

	return session
}

func (d *SessionDiscoverer) isSessionMatch(session domain.Session, ms domain.SyncSession, normalizedPath string) bool {
	// 1. Explicit ID match via labels
	if session.ID != "" && ms.Labels != nil {
		if aid, ok := ms.Labels["aiman-id"]; ok && aid != "" {
			return aid == session.ID
		}
	}

	// 2. Stable name match
	if session.ID != "" && ms.Name == "aiman-sync-"+session.ID {
		return true
	}

	// 3. Fallback for older sessions: name-based match
	if session.TmuxSession != "" {
		if ms.Name == session.TmuxSession || strings.HasPrefix(ms.Name, session.TmuxSession+"-") {
			return true
		}
	}

	// Normalized remote path from mutagen
	normalizedRemote := normalizePath(ms.RemotePath)
	normalizedLocal := normalizePath(ms.LocalPath)

	// In Mutagen, either Alpha or Beta could be the remote.
	return normalizedRemote == normalizedPath || normalizedLocal == normalizedPath ||
		strings.HasSuffix(normalizedRemote, normalizedPath) || strings.HasSuffix(normalizedLocal, normalizedPath)
}

func normalizePath(p string) string {
	if p == "" {
		return ""
	}
	// Use forward slashes for remote paths regardless of local OS
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimSpace(p)
	// Remove trailing slash if not root
	if len(p) > 1 && strings.HasSuffix(p, "/") {
		p = p[:len(p)-1]
	}
	return p
}
