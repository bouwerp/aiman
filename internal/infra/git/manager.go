package git

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

type Manager struct {
	cfg *config.GitConfig
}

func NewManager(cfg *config.GitConfig) *Manager {
	if cfg == nil {
		yes := true
		cfg = &config.GitConfig{IncludePersonal: &yes}
	}
	return &Manager{cfg: cfg}
}

type ghRepo struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	SSHUrl        string `json:"sshUrl"`
	NameWithOwner string `json:"nameWithOwner"`
	PushedAt      string `json:"pushedAt"`
	UpdatedAt     string `json:"updatedAt"`
}

func (m *Manager) ListRepos(ctx context.Context) ([]domain.Repo, error) {
	var allRepos []domain.Repo

	// Fetch repos owned by the authenticated user (default on; see config.PersonalReposEnabled).
	if config.PersonalReposEnabled(m.cfg) {
		personalRepos, err := m.fetchPersonalRepos(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch personal repos: %w", err)
		}
		allRepos = append(allRepos, personalRepos...)
	}

	// Fetch repos from configured orgs
	for _, org := range m.cfg.IncludeOrgs {
		orgRepos, err := m.fetchOrgRepos(ctx, org)
		if err != nil {
			// Log error but continue with other orgs
			fmt.Printf("Warning: failed to fetch repos for org %s: %v\n", org, err)
			continue
		}
		allRepos = append(allRepos, orgRepos...)
	}

	// Apply include/exclude filters
	filteredRepos := m.applyFilters(allRepos)
	sortReposByRecentActivity(filteredRepos)

	return filteredRepos, nil
}

func (m *Manager) fetchPersonalRepos(ctx context.Context) ([]domain.Repo, error) {
	// No owner argument: lists repos for the authenticated GitHub user (not orgs).
	// --limit is above gh's default (30).
	cmd := exec.CommandContext(ctx, "gh", "repo", "list", "--limit", "200", "--json", "name,url,sshUrl,nameWithOwner,pushedAt,updatedAt")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to list personal repositories: %w, output: %s", err, string(output))
	}

	return m.parseGhRepos(output)
}

func (m *Manager) fetchOrgRepos(ctx context.Context, org string) ([]domain.Repo, error) {
	cmd := exec.CommandContext(ctx, "gh", "repo", "list", org, "--limit", "200", "--json", "name,url,sshUrl,nameWithOwner,pushedAt,updatedAt")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to list org repositories for %s: %w, output: %s", org, err, string(output))
	}

	return m.parseGhRepos(output)
}

func (m *Manager) parseGhRepos(output []byte) ([]domain.Repo, error) {
	var ghRepos []ghRepo
	if err := json.Unmarshal(output, &ghRepos); err != nil {
		return nil, fmt.Errorf("failed to parse gh output: %w", err)
	}

	repos := make([]domain.Repo, len(ghRepos))
	for i, r := range ghRepos {
		// Use full name if available, otherwise just name
		displayName := r.NameWithOwner
		if displayName == "" {
			displayName = r.Name
		}
		// Prefer SSH URL if available, as it's often more reliable on remote hosts (assuming keys are set up)
		repoURL := r.SSHUrl
		if repoURL == "" {
			repoURL = r.URL
		}
		repos[i] = domain.Repo{
			Name:           displayName,
			URL:            repoURL,
			LastActivityAt: githubRepoActivityTime(r.PushedAt, r.UpdatedAt),
		}
	}

	return repos, nil
}

func githubRepoActivityTime(pushedAt, updatedAt string) time.Time {
	var pushed, updated time.Time
	if pushedAt != "" {
		if t, err := time.Parse(time.RFC3339, pushedAt); err == nil {
			pushed = t
		}
	}
	if updatedAt != "" {
		if t, err := time.Parse(time.RFC3339, updatedAt); err == nil {
			updated = t
		}
	}
	switch {
	case !pushed.IsZero() && (pushed.After(updated) || updated.IsZero()):
		return pushed
	case !updated.IsZero():
		return updated
	default:
		return time.Time{}
	}
}

func sortReposByRecentActivity(repos []domain.Repo) {
	slices.SortStableFunc(repos, func(a, b domain.Repo) int {
		if c := b.LastActivityAt.Compare(a.LastActivityAt); c != 0 {
			return c
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
}

func (m *Manager) applyFilters(repos []domain.Repo) []domain.Repo {
	if len(m.cfg.IncludePatterns) == 0 && len(m.cfg.ExcludePatterns) == 0 {
		return repos
	}

	var filtered []domain.Repo

	for _, repo := range repos {
		// Check exclude patterns first
		if m.matchesAny(repo.Name, m.cfg.ExcludePatterns) {
			continue
		}

		// If include patterns are specified, only include matching repos
		if len(m.cfg.IncludePatterns) > 0 {
			if m.matchesAny(repo.Name, m.cfg.IncludePatterns) {
				filtered = append(filtered, repo)
			}
		} else {
			// No include patterns, include everything not excluded
			filtered = append(filtered, repo)
		}
	}

	return filtered
}

func (m *Manager) matchesAny(s string, patterns []string) bool {
	for _, pattern := range patterns {
		// Try exact match first
		if s == pattern {
			return true
		}
		// Try regex match
		if matched, err := regexp.MatchString(pattern, s); err == nil && matched {
			return true
		}
	}
	return false
}

func (m *Manager) SetupWorktree(ctx context.Context, repo domain.Repo, branch string) (domain.Worktree, error) {
	worktreePath := fmt.Sprintf("../%s", strings.ReplaceAll(branch, "/", "-"))

	cmd := exec.CommandContext(ctx, "git", "worktree", "add", worktreePath, branch)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return domain.Worktree{}, fmt.Errorf("failed to setup worktree: %w, output: %s", err, string(output))
	}

	wt, err := filepath.Abs(worktreePath)
	if err != nil {
		wt = worktreePath
	}
	_ = m.ensureAimanTaskGitignoredLocal(ctx, wt)

	return domain.Worktree{
		Path:   worktreePath,
		Branch: branch,
	}, nil
}

const aimanTaskIgnoreLine = ".aiman_task.md"
const aimanTaskIgnoreComment = "# Aiman: session-local JIRA/task stub (do not commit)"

func aimanTaskGitignoreBashScript(worktreePath string) string {
	return fmt.Sprintf(`wt=%s
git -C "$wt" rev-parse --git-dir >/dev/null 2>&1 || exit 0
ign="$wt/.gitignore"
com=%s
line=%s
if [ -f "$ign" ] && grep -qxF "$line" "$ign"; then exit 0; fi
if [ ! -f "$ign" ]; then
  { echo "$com"; echo "$line"; } > "$ign"
else
  { echo ""; echo "$com"; echo "$line"; } >> "$ign"
fi`, strconv.Quote(worktreePath), strconv.Quote(aimanTaskIgnoreComment), strconv.Quote(aimanTaskIgnoreLine))
}

// EnsureAimanTaskGitignored appends .aiman_task.md to the worktree's .gitignore when absent.
func (m *Manager) EnsureAimanTaskGitignored(ctx context.Context, remote domain.RemoteExecutor, worktreePath string) error {
	if strings.TrimSpace(worktreePath) == "" {
		return nil
	}
	script := aimanTaskGitignoreBashScript(worktreePath)
	_, err := remote.Execute(ctx, "bash -ce "+strconv.Quote(script))
	return err
}

func (m *Manager) ensureAimanTaskGitignoredLocal(ctx context.Context, worktreePath string) error {
	script := aimanTaskGitignoreBashScript(worktreePath)
	cmd := exec.CommandContext(ctx, "bash", "-ce", script)
	_, err := cmd.CombinedOutput()
	return err
}

func (m *Manager) SetupRemoteWorktree(ctx context.Context, remote domain.RemoteExecutor, repo domain.Repo, branch string) (domain.Worktree, error) {
	repoName := extractRepoName(repo.Name)
	remoteRoot := remote.GetRoot()
	if remoteRoot == "" {
		return domain.Worktree{}, fmt.Errorf("remote root not configured")
	}

	var repoPath string
	cleanRoot := strings.TrimRight(remoteRoot, "/")
	if strings.HasSuffix(cleanRoot, "/"+repoName) || cleanRoot == repoName {
		repoPath = cleanRoot
	} else {
		repoPath = fmt.Sprintf("%s/%s", cleanRoot, repoName)
	}

	worktreeDir := strings.ReplaceAll(branch, "/", "-")
	// Worktree is placed alongside the main repository
	worktreePath := fmt.Sprintf("%s/../%s", repoPath, worktreeDir)

	// Safety: if worktreeDir == repoName the computed path resolves to the main repo.
	if filepath.Clean(filepath.Join(repoPath, "..", worktreeDir)) == filepath.Clean(repoPath) {
		return domain.Worktree{}, fmt.Errorf("branch name %q would place the worktree inside the main repository directory — choose a different name", branch)
	}

	// Ensure repo exists
	if err := remote.ValidateDir(ctx, repoPath); err != nil {
		if repo.URL != "" {
			// Get parent of repoPath for cloning
			parentDir := filepath.Dir(repoPath)
			_, cloneErr := remote.Execute(ctx, fmt.Sprintf("mkdir -p %s && cd %s && git clone %s %s", parentDir, parentDir, repo.URL, repoName))
			if cloneErr != nil {
				return domain.Worktree{}, fmt.Errorf("failed to clone repository: %w", cloneErr)
			}
		} else {
			return domain.Worktree{}, fmt.Errorf("repository %s not found on remote and no URL provided", repoName)
		}
	}

	// Fetch latest
	_, _ = remote.Execute(ctx, fmt.Sprintf("git -C %s fetch origin", repoPath))

	// Check if worktree already exists
	checkCmd := fmt.Sprintf("bash -c 'if [ -d %q ]; then echo EXISTS; fi'", worktreePath)
	checkOut, _ := remote.Execute(ctx, checkCmd)
	if strings.Contains(checkOut, "EXISTS") {
		// Validate that git is actually working in this directory.
		// A "stale" worktree (directory exists but .git/worktrees metadata is gone or corrupt)
		// would fail here — we repair it instead of returning a broken path.
		gitCheck, _ := remote.Execute(ctx, fmt.Sprintf("git -C %q rev-parse --git-dir 2>/dev/null || echo BROKEN", worktreePath))
		if strings.TrimSpace(gitCheck) != "BROKEN" {
			// Use realpath to resolve worktree path
			resolvedPath := worktreePath
			if out, err := remote.Execute(ctx, fmt.Sprintf("realpath %q", worktreePath)); err == nil {
				resolvedPath = strings.TrimSpace(out)
			}
			return domain.Worktree{
				Path:   resolvedPath,
				Branch: branch,
			}, nil
		}
		// Broken worktree directory — unlock, prune metadata, remove directory.
		_, _ = remote.Execute(ctx, fmt.Sprintf("git -C %s worktree unlock %q 2>/dev/null || true", repoPath, worktreePath))
		_, _ = remote.Execute(ctx, fmt.Sprintf("rm -rf %q", worktreePath))
	}

	// Prune stale worktree metadata before adding — ensures git worktree add succeeds
	// even if a previous termination left stale entries behind.
	_, _ = remote.Execute(ctx, fmt.Sprintf("git -C %s worktree prune --expire=now 2>/dev/null || true", repoPath))

	// Determine base branch
	var baseBranch string
	for _, b := range []string{"origin/main", "origin/master", "main", "master"} {
		if _, err := remote.Execute(ctx, fmt.Sprintf("git -C %s rev-parse --verify %s", repoPath, b)); err == nil {
			baseBranch = b
			break
		}
	}

	if baseBranch == "" {
		baseBranch = "main" // Fallback
	}

	// Create worktree
	worktreeCmd := fmt.Sprintf("git -C %s worktree add -B %s ../%s %s", repoPath, branch, worktreeDir, baseBranch)
	_, worktreeErr := remote.Execute(ctx, worktreeCmd)
	if worktreeErr != nil {
		return domain.Worktree{}, fmt.Errorf("failed to create worktree: %w", worktreeErr)
	}

	// Resolve worktree path
	resolvedPath := worktreePath
	if out, err := remote.Execute(ctx, fmt.Sprintf("realpath %q", worktreePath)); err == nil {
		resolvedPath = strings.TrimSpace(out)
	}

	return domain.Worktree{
		Path:   resolvedPath,
		Branch: branch,
	}, nil
}

func (m *Manager) ListRemoteBranches(ctx context.Context, remote domain.RemoteExecutor, repo domain.Repo) ([]string, error) {
	repoName := extractRepoName(repo.Name)
	remoteRoot := remote.GetRoot()
	if remoteRoot == "" {
		return nil, fmt.Errorf("remote root not configured")
	}

	cleanRoot := strings.TrimRight(remoteRoot, "/")
	var repoPath string
	if strings.HasSuffix(cleanRoot, "/"+repoName) || cleanRoot == repoName {
		repoPath = cleanRoot
	} else {
		repoPath = fmt.Sprintf("%s/%s", cleanRoot, repoName)
	}

	if err := remote.ValidateDir(ctx, repoPath); err != nil {
		return nil, fmt.Errorf("repository %s not found on remote", repoName)
	}

	// Fetch latest to ensure remote branches are up to date
	_, _ = remote.Execute(ctx, fmt.Sprintf("git -C %s fetch origin 2>/dev/null", repoPath))

	out, err := remote.Execute(ctx, fmt.Sprintf("git -C %s branch -r", repoPath))
	if err != nil {
		return nil, fmt.Errorf("failed to list remote branches: %w", err)
	}

	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "->") {
			continue
		}
		branch := strings.TrimPrefix(line, "origin/")
		branches = append(branches, branch)
	}
	return branches, nil
}

func (m *Manager) SetupRemoteWorktreeFromBranch(ctx context.Context, remote domain.RemoteExecutor, repo domain.Repo, branch string) (domain.Worktree, error) {
	repoName := extractRepoName(repo.Name)
	remoteRoot := remote.GetRoot()
	if remoteRoot == "" {
		return domain.Worktree{}, fmt.Errorf("remote root not configured")
	}

	cleanRoot := strings.TrimRight(remoteRoot, "/")
	var repoPath string
	if strings.HasSuffix(cleanRoot, "/"+repoName) || cleanRoot == repoName {
		repoPath = cleanRoot
	} else {
		repoPath = fmt.Sprintf("%s/%s", cleanRoot, repoName)
	}

	if err := remote.ValidateDir(ctx, repoPath); err != nil {
		return domain.Worktree{}, fmt.Errorf("repository %s not found on remote", repoName)
	}

	// Fetch latest
	_, _ = remote.Execute(ctx, fmt.Sprintf("git -C %s fetch origin", repoPath))

	worktreeDir := strings.ReplaceAll(branch, "/", "-")
	worktreePath := fmt.Sprintf("%s/../%s", repoPath, worktreeDir)

	// Safety: if worktreeDir == repoName the computed path resolves to the main repo.
	if filepath.Clean(filepath.Join(repoPath, "..", worktreeDir)) == filepath.Clean(repoPath) {
		return domain.Worktree{}, fmt.Errorf("branch name %q would place the worktree inside the main repository directory — choose a different name", branch)
	}

	// Check for existing worktree for this branch via git worktree list.
	// Parse the porcelain output to find the registered path so we can validate its health.
	listOut, _ := remote.Execute(ctx, fmt.Sprintf("git -C %s worktree list --porcelain", repoPath))
	var registeredWTPath string
	var currentWTPath string
	for _, line := range strings.Split(listOut, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "worktree ") {
			currentWTPath = strings.TrimPrefix(line, "worktree ")
		} else if line == "branch refs/heads/"+branch {
			registeredWTPath = currentWTPath
		}
	}
	if registeredWTPath != "" {
		// Branch is registered — check if git is healthy at that path.
		gitCheck, _ := remote.Execute(ctx, fmt.Sprintf("git -C %q rev-parse --git-dir 2>/dev/null || echo BROKEN", registeredWTPath))
		if strings.TrimSpace(gitCheck) != "BROKEN" {
			return domain.Worktree{}, fmt.Errorf("WORKTREE_EXISTS")
		}
		// Stale/broken worktree — unlock so prune can remove it.
		_, _ = remote.Execute(ctx, fmt.Sprintf("git -C %s worktree unlock %q 2>/dev/null || true", repoPath, registeredWTPath))
		_, _ = remote.Execute(ctx, fmt.Sprintf("rm -rf %q", registeredWTPath))
	}

	// Check if worktree directory already exists (not registered in git, but the dir is there)
	checkOut, _ := remote.Execute(ctx, fmt.Sprintf("bash -c 'if [ -d %q ]; then echo EXISTS; fi'", worktreePath))
	if strings.Contains(checkOut, "EXISTS") {
		// Validate git health
		gitCheck, _ := remote.Execute(ctx, fmt.Sprintf("git -C %q rev-parse --git-dir 2>/dev/null || echo BROKEN", worktreePath))
		if strings.TrimSpace(gitCheck) != "BROKEN" {
			return domain.Worktree{}, fmt.Errorf("WORKTREE_EXISTS")
		}
		// Broken directory — remove it so worktree add can recreate it.
		_, _ = remote.Execute(ctx, fmt.Sprintf("rm -rf %q", worktreePath))
	}

	// Prune stale worktree metadata before adding.
	_, _ = remote.Execute(ctx, fmt.Sprintf("git -C %s worktree prune --expire=now 2>/dev/null || true", repoPath))

	// Create worktree from existing remote branch.
	// First try creating a new local branch tracking the remote (-b). If the local
	// branch already exists (was fetched previously), fall back to checking it out
	// directly without -b.
	worktreeCmd := fmt.Sprintf("git -C %s worktree add -b %s ../%s origin/%s", repoPath, branch, worktreeDir, branch)
	if out, err := remote.Execute(ctx, worktreeCmd); err != nil {
		if !strings.Contains(out, "already exists") {
			return domain.Worktree{}, fmt.Errorf("failed to create worktree from branch %s: %w", branch, err)
		}
		// Local branch exists — check it out into the worktree directly
		worktreeCmd = fmt.Sprintf("git -C %s worktree add ../%s %s", repoPath, worktreeDir, branch)
		if _, err2 := remote.Execute(ctx, worktreeCmd); err2 != nil {
			return domain.Worktree{}, fmt.Errorf("failed to create worktree from branch %s: %w", branch, err2)
		}
	}

	// Resolve worktree path
	resolvedPath := worktreePath
	if out, err := remote.Execute(ctx, fmt.Sprintf("realpath %q", worktreePath)); err == nil {
		resolvedPath = strings.TrimSpace(out)
	}

	return domain.Worktree{
		Path:   resolvedPath,
		Branch: branch,
	}, nil
}

func extractRepoName(fullName string) string {
	parts := strings.Split(fullName, "/")
	return parts[len(parts)-1]
}

// FetchOrganizations returns a list of organizations the user has access to
func FetchOrganizations(ctx context.Context) ([]string, error) {
	cmd := exec.CommandContext(ctx, "gh", "org", "list", "--limit", "100")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to list organizations: %w, output: %s", err, string(output))
	}

	// Parse output - each line is an org name
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	var orgs []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			orgs = append(orgs, line)
		}
	}

	return orgs, nil
}

func (m *Manager) GetGitStatus(ctx context.Context, remote domain.RemoteExecutor, path string) (domain.GitStatus, error) {
	status := domain.GitStatus{}

	// 1. Get branch and counts via git status --porcelain=v2 --branch
	// v2 is easier to parse for these specific requirements
	cmd := fmt.Sprintf("git -C %q status --porcelain=v2 --branch", path)
	output, err := remote.Execute(ctx, cmd)
	if err != nil {
		return status, fmt.Errorf("failed to get git status: %w", err)
	}

	lines := strings.Split(output, "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			status.Branch = strings.TrimPrefix(line, "# branch.head ")
		case strings.HasPrefix(line, "# branch.upstream "):
			status.TrackingRemote = strings.TrimPrefix(line, "# branch.upstream ")
			status.HasUpstream = true
		case strings.HasPrefix(line, "# branch.ab "):
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				_, _ = fmt.Sscanf(parts[2], "+%d", &status.Ahead)
				_, _ = fmt.Sscanf(parts[3], "-%d", &status.Behind)
			}
		case strings.HasPrefix(line, "? "):
			status.UntrackedCount++
		case len(line) > 3:
			// v2 porcelain lines start with 1 (tracked) or 2 (renamed/copied)
			// Character at index 2 is staged change, index 3 is unstaged change
			if line[0] == '1' || line[0] == '2' {
				if line[2] != '.' {
					status.StagedCount++
				}
				if line[3] != '.' {
					status.UnstagedCount++
				}
			}
		}
	}

	// 2. Get unpushed commits count (more reliable than porcelain if multiple remotes involved)
	if status.HasUpstream {
		cmd = fmt.Sprintf("git -C %q rev-list --count @{u}..HEAD", path)
		out, err := remote.Execute(ctx, cmd)
		if err == nil {
			_, _ = fmt.Sscanf(strings.TrimSpace(out), "%d", &status.UnpushedCommits)
		}
	}

	// 3. Pull request via gh (current branch, with --repo/--head fallback)
	originOut, _ := remote.Execute(ctx, fmt.Sprintf("git -C %q remote get-url origin", path))
	owner, repo, ok := parseGitHubOwnerRepo(strings.TrimSpace(originOut))
	status.PullRequest = resolvePullRequestForBranch(ctx, remote, path, owner, repo, ok, status.Branch)

	return status, nil
}
