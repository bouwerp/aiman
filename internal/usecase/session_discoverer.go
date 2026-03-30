package usecase

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/google/uuid"
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
	seenIDs := make(map[string]bool)
	seenWorktrees := make(map[string]bool)
	seenTmuxNames := make(map[string]bool)
	seenMutagenIDs := make(map[string]bool)
	var tmuxPathPairs []domain.Session

	addSession := func(s domain.Session) {
		if seenIDs[s.ID] {
			return
		}
		if s.TmuxSession != "" && seenTmuxNames[s.TmuxSession] {
			return
		}
		seenIDs[s.ID] = true
		if s.WorktreePath != "" {
			seenWorktrees[s.WorktreePath] = true
		}
		if s.TmuxSession != "" {
			seenTmuxNames[s.TmuxSession] = true
		}
		sessions = append(sessions, s)
	}

	// Process tmux sessions
	for _, name := range tmuxSessions {
		session := d.discoverSession(ctx, host, name, mutagenSessions)
		if session.WorktreePath != "" || session.WorkingDirectory != "" {
			tmuxPathPairs = append(tmuxPathPairs, domain.Session{
				WorktreePath:     session.WorktreePath,
				WorkingDirectory: session.WorkingDirectory,
			})
		}
		if session.MutagenSyncID != "" {
			seenMutagenIDs[session.MutagenSyncID] = true
			// Also mark by the mutagen session's internal UUID so the orphaned-sync
			// section (which checks ms.ID) doesn't re-add the same sync.
			for _, ms := range mutagenSessions {
				if ms.Name == session.MutagenSyncID || ms.ID == session.MutagenSyncID {
					seenMutagenIDs[ms.ID] = true
					seenMutagenIDs[ms.Name] = true
					break
				}
			}
		}
		addSession(session)
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
							TmuxSession:      wtBase,
							RemoteHost:       host,
							Status:           domain.SessionStatusInactive,
							WorktreePath:     normalizedWT,
							WorkingDirectory: normalizedWT,
							CreatedAt:        time.Now(),
						}

						// Try to read session ID from git metadata or root
						idCmd := fmt.Sprintf("git_dir=$(git -C %q rev-parse --git-dir 2>/dev/null) && if [ -f \"$git_dir/aiman-id\" ]; then cat \"$git_dir/aiman-id\"; elif [ -f %q/.aiman-id ]; then cat %q/.aiman-id; fi",
							normalizedWT, normalizedWT, normalizedWT)
						id, err := d.remoteExecutor.Execute(ctx, idCmd)
						if err == nil && strings.TrimSpace(id) != "" {
							session.ID = strings.TrimSpace(id)

							// Auto-migration
							migrationCmd := fmt.Sprintf("git_dir=$(git -C %q rev-parse --git-dir 2>/dev/null) && if [ -f %q/.aiman-id ] && [ -d \"$git_dir\" ]; then mv %q/.aiman-id \"$git_dir/aiman-id\"; fi",
								normalizedWT, normalizedWT, normalizedWT)
							_, _ = d.remoteExecutor.Execute(ctx, migrationCmd)
						}

						if session.ID == "" {
							session.ID = uuid.New().String()
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
							if !seenMutagenIDs[ms.ID] && d.isSessionMatch(session, ms) {
								session.LocalPath = normalizePath(ms.LocalPath)
								if ms.Name != "" {
									session.MutagenSyncID = ms.Name
								} else {
									session.MutagenSyncID = ms.ID
								}
								seenMutagenIDs[ms.ID] = true
								break
							}
						}

						addSession(session)
					}
				}
			}
		}
	}

	// 4. Scan for orphaned mutagen syncs that don't match any tmux session.
	// If a sync has an aiman-id label it came from a managed session; if
	// the corresponding tmux session is gone the sync is a leftover from a
	// terminated session — auto-terminate it instead of surfacing a ghost.
	for _, ms := range mutagenSessions {
		if seenMutagenIDs[ms.ID] || seenMutagenIDs[ms.Name] {
			continue
		}

		// Never surface aiman-managed sync names as fake sessions; terminate orphans.
		if strings.TrimSpace(ms.Name) != "" && strings.HasPrefix(strings.TrimSpace(ms.Name), aimanSyncNamePrefix) {
			_ = d.handleOrphanAimanNamedSync(ctx, host, ms, tmuxPathPairs)
			continue
		}

		// If the sync has an aiman-id label but no tmux session exists for
		// it, this is a leftover from a terminated session. Clean it up.
		if aid, ok := ms.Labels["aiman-id"]; ok && aid != "" {
			d.syncEngine.TerminateSync(ctx, ms.Name)
			continue
		}

		remotePath := normalizePath(ms.RemotePath)
		if remotePath == "" {
			continue
		}

		syncName := ms.Name
		if syncName == "" {
			syncName = ms.ID
		}

		session := domain.Session{
			TmuxSession:      ms.Name,
			RemoteHost:       host,
			Status:           domain.SessionStatusInactive,
			WorktreePath:     remotePath,
			WorkingDirectory: remotePath,
			MutagenSyncID:    syncName,
			LocalPath:        normalizePath(ms.LocalPath),
			CreatedAt:        time.Now(),
			ID:               uuid.New().String(),
		}
		session.IssueKey = domain.ExtractKey(ms.Name)
		addSession(session)
	}

	return dedupeDiscoveredSessions(sessions), nil
}

const aimanSyncNamePrefix = "aiman-sync-"

func isAimanSyncTmuxName(name string) bool {
	return strings.HasPrefix(strings.TrimSpace(name), aimanSyncNamePrefix)
}

// dedupeDiscoveredSessions removes mutagen ghost rows (TmuxSession aiman-sync-*), collapses
// duplicate tmux entries, and drops orphan worktrees already covered by a live tmux session.
func dedupeDiscoveredSessions(sessions []domain.Session) []domain.Session {
	if len(sessions) == 0 {
		return sessions
	}
	scored := make([]domain.Session, len(sessions))
	copy(scored, sessions)
	sort.SliceStable(scored, func(i, j int) bool {
		return sessionDedupePriority(scored[i]) > sessionDedupePriority(scored[j])
	})

	seenTmux := make(map[string]bool)
	coveredWT := make(map[string]bool)
	coverPaths := func(s domain.Session) {
		h := s.RemoteHost
		for _, p := range []string{s.WorktreePath, s.WorkingDirectory} {
			if p == "" {
				continue
			}
			coveredWT[h+"\x00"+normalizePath(p)] = true
		}
	}

	var out []domain.Session
	for _, s := range scored {
		if isAimanSyncTmuxName(s.TmuxSession) {
			continue
		}
		if s.TmuxSession != "" {
			key := s.RemoteHost + "\x00" + s.TmuxSession
			if seenTmux[key] {
				continue
			}
			seenTmux[key] = true
			out = append(out, s)
			coverPaths(s)
			continue
		}
		if s.WorktreePath != "" {
			wk := s.RemoteHost + "\x00" + normalizePath(s.WorktreePath)
			if coveredWT[wk] {
				continue
			}
		}
		out = append(out, s)
		coverPaths(s)
	}
	return out
}

func sessionDedupePriority(s domain.Session) int {
	if isAimanSyncTmuxName(s.TmuxSession) {
		return -1
	}
	if s.TmuxSession == "" {
		return 0
	}
	if s.Status == domain.SessionStatusActive {
		return 3
	}
	return 2
}

// handleOrphanAimanNamedSync handles unmatched mutagen sessions created by aiman
// (name aiman-sync-<session-id>). Returns true if this list entry must not become
// a synthetic dashboard row. Terminates the sync when no tmux session matches the
// same remote path (mutagen list usually omits Labels:, so aiman-id cleanup alone
// never runs).
func (d *SessionDiscoverer) handleOrphanAimanNamedSync(ctx context.Context, discoverHost string, ms domain.SyncSession, tmuxPathPairs []domain.Session) bool {
	syncName := strings.TrimSpace(ms.Name)
	if syncName == "" || !strings.HasPrefix(syncName, aimanSyncNamePrefix) {
		return false
	}
	// Mutagen list is machine-global; tmux scan is per SSH host. Never terminate
	// or relabel another remote's sync when viewing this host.
	if discoverHost != "" && ms.RemoteEndpoint != "" {
		if h := sshHostFromEndpoint(ms.RemoteEndpoint); h != "" && h != discoverHost {
			return true
		}
	}
	matched := false
	for _, ts := range tmuxPathPairs {
		if d.isSessionMatch(ts, ms) {
			matched = true
			break
		}
	}
	if !matched && normalizePath(ms.RemotePath) != "" {
		d.syncEngine.TerminateSync(ctx, syncName)
	}
	return true
}

func sshHostFromEndpoint(endpoint string) string {
	i := strings.Index(endpoint, "@")
	if i < 0 {
		return ""
	}
	return endpoint[i+1:]
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
		session.ID = strings.TrimSpace(aimanID)
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

	// 4. Try reading session ID from git metadata or worktree root
	if session.WorktreePath != "" && session.ID == "" {
		// New robust location: inside .git metadata
		// Old fallback: root of worktree
		cmd := fmt.Sprintf("git_dir=$(git -C %q rev-parse --git-dir 2>/dev/null) && if [ -f \"$git_dir/aiman-id\" ]; then cat \"$git_dir/aiman-id\"; elif [ -f %q/.aiman-id ]; then cat %q/.aiman-id; fi",
			session.WorktreePath, session.WorktreePath, session.WorktreePath)

		id, err := d.remoteExecutor.Execute(ctx, cmd)
		if err == nil && strings.TrimSpace(id) != "" {
			session.ID = strings.TrimSpace(id)

			// Auto-migration: Move old file to new location if it exists at root
			migrationCmd := fmt.Sprintf("git_dir=$(git -C %q rev-parse --git-dir 2>/dev/null) && if [ -f %q/.aiman-id ] && [ -d \"$git_dir\" ]; then mv %q/.aiman-id \"$git_dir/aiman-id\"; fi",
				session.WorktreePath, session.WorktreePath, session.WorktreePath)
			_, _ = d.remoteExecutor.Execute(ctx, migrationCmd)
		}
	}

	// 5. Extract JIRA key from session name
	key := domain.ExtractKey(name)
	if key == "" && session.WorktreePath != "" {
		// Try extracting from WorktreePath
		key = domain.ExtractKey(session.WorktreePath)
	}
	session.IssueKey = key

	// 6. Try to determine repo name from remote URL (most accurate for worktrees)
	if session.WorktreePath != "" {
		remoteURL, err := d.remoteExecutor.Execute(ctx, fmt.Sprintf("git -C %q remote get-url origin 2>/dev/null", session.WorktreePath))
		if err == nil && strings.TrimSpace(remoteURL) != "" {
			session.RepoName = extractRepoNameFromURL(strings.TrimSpace(remoteURL))
		}
		if session.RepoName == "" {
			parts := strings.Split(session.WorktreePath, "/")
			if len(parts) > 0 {
				session.RepoName = parts[len(parts)-1]
			}
		}
	}

	// 7. Cross-reference with mutagen
	if session.WorktreePath != "" {
		for _, ms := range mutagenSessions {
			if d.isSessionMatch(session, ms) {
				// After ListSyncSessions post-processing, LocalPath is the
				// local filesystem path (no `:`) and RemotePath is the remote path.
				session.LocalPath = normalizePath(ms.LocalPath)
				if ms.Name != "" {
					session.MutagenSyncID = ms.Name
				} else {
					session.MutagenSyncID = ms.ID
				}
				session.Status = domain.SessionStatusSyncing
				break
			}
		}
	}

	// 8. If session ID is still empty (e.g., legacy session), generate a new one
	if session.ID == "" {
		session.ID = uuid.New().String()
	}

	return session
}

func (d *SessionDiscoverer) isSessionMatch(session domain.Session, ms domain.SyncSession) bool {
	// 1. Explicit ID match via labels (most reliable)
	if session.ID != "" && ms.Labels != nil {
		if aid, ok := ms.Labels["aiman-id"]; ok && aid != "" {
			return aid == session.ID
		}
	}

	// 2. Stable name match
	if session.ID != "" && ms.Name == aimanSyncNamePrefix+session.ID {
		return true
	}

	// 3. Fallback for older sessions: name-based match
	if session.TmuxSession != "" {
		if ms.Name == session.TmuxSession || strings.HasPrefix(ms.Name, session.TmuxSession+"-") {
			return true
		}
	}

	// 4. Path-based matching against mutagen's remote path.
	// After ListSyncSessions post-processing, RemotePath is the remote
	// filesystem path with the host: prefix already stripped.
	normalizedRemote := normalizePath(ms.RemotePath)
	if normalizedRemote == "" {
		return false
	}

	// Compare against both WorktreePath (git root) and WorkingDirectory
	// (user-chosen subdirectory scope). Mutagen syncs are created pointing
	// to WorkingDirectory, which may differ from WorktreePath.
	for _, sessionPath := range []string{
		normalizePath(session.WorktreePath),
		normalizePath(session.WorkingDirectory),
	} {
		if sessionPath == "" {
			continue
		}
		if normalizedRemote == sessionPath {
			return true
		}
		// Mutagen syncs a subdirectory within the session's scope
		if strings.HasPrefix(normalizedRemote, sessionPath+"/") {
			return true
		}
		// Session scope is a subdirectory of what mutagen syncs
		if strings.HasPrefix(sessionPath, normalizedRemote+"/") {
			return true
		}
	}

	return false
}

func normalizePath(p string) string {
	if p == "" {
		return ""
	}
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimSpace(p)
	p = path.Clean(p)
	if p == "." {
		return ""
	}
	if len(p) > 1 && strings.HasSuffix(p, "/") {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// extractRepoNameFromURL parses a git remote URL and returns the "org/repo" name.
// Handles both SSH (git@github.com:org/repo.git) and HTTPS (https://github.com/org/repo.git).
func extractRepoNameFromURL(remoteURL string) string {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return ""
	}

	// HTTPS URL: https://github.com/org/repo.git
	if strings.Contains(remoteURL, "://") {
		cleaned := strings.TrimSuffix(remoteURL, ".git")
		parts := strings.Split(cleaned, "/")
		if len(parts) >= 2 {
			return parts[len(parts)-2] + "/" + parts[len(parts)-1]
		}
		return ""
	}

	// SSH URL: git@github.com:org/repo.git
	if idx := strings.Index(remoteURL, ":"); idx > 0 {
		name := remoteURL[idx+1:]
		name = strings.TrimSuffix(name, ".git")
		return name
	}

	return ""
}
