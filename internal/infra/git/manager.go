package git

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

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
	NameWithOwner string `json:"nameWithOwner"`
}

type ghPR struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	URL     string `json:"url"`
	Reviews []struct {
		State string `json:"state"`
	} `json:"reviews"`
	Comments          []interface{} `json:"comments"`
	StatusCheckRollup []struct {
		Context string `json:"context"`
		State   string `json:"state"`
		Status  string `json:"status"`
	} `json:"statusCheckRollup"`
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

	return filteredRepos, nil
}

func (m *Manager) fetchPersonalRepos(ctx context.Context) ([]domain.Repo, error) {
	// No owner argument: lists repos for the authenticated GitHub user (not orgs).
	// --limit is above gh's default (30).
	cmd := exec.CommandContext(ctx, "gh", "repo", "list", "--limit", "200", "--json", "name,url,nameWithOwner")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to list personal repositories: %w, output: %s", err, string(output))
	}

	return m.parseGhRepos(output)
}

func (m *Manager) fetchOrgRepos(ctx context.Context, org string) ([]domain.Repo, error) {
	cmd := exec.CommandContext(ctx, "gh", "repo", "list", org, "--limit", "200", "--json", "name,url,nameWithOwner")
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
		repos[i] = domain.Repo{
			Name: displayName,
			URL:  r.URL,
		}
	}

	return repos, nil
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

	return domain.Worktree{
		Path:   worktreePath,
		Branch: branch,
	}, nil
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

	// Check for existing worktree for this branch via git worktree list
	listOut, _ := remote.Execute(ctx, fmt.Sprintf("git -C %s worktree list --porcelain", repoPath))
	for _, line := range strings.Split(listOut, "\n") {
		if strings.TrimSpace(line) == "branch refs/heads/"+branch {
			return domain.Worktree{}, fmt.Errorf("WORKTREE_EXISTS")
		}
	}

	// Check if worktree directory already exists
	checkOut, _ := remote.Execute(ctx, fmt.Sprintf("bash -c 'if [ -d %q ]; then echo EXISTS; fi'", worktreePath))
	if strings.Contains(checkOut, "EXISTS") {
		return domain.Worktree{}, fmt.Errorf("WORKTREE_EXISTS")
	}

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
	cmd := fmt.Sprintf("git -C %s status --porcelain=v2 --branch", path)
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
		cmd = fmt.Sprintf("git -C %s rev-list --count @{u}..HEAD", path)
		out, err := remote.Execute(ctx, cmd)
		if err == nil {
			_, _ = fmt.Sscanf(strings.TrimSpace(out), "%d", &status.UnpushedCommits)
		}
	}

	// 3. Get PR info via gh CLI
	cmd = fmt.Sprintf("gh -C %s pr view --json number,title,state,url,reviews,comments,statusCheckRollup", path)
	out, err := remote.Execute(ctx, cmd)
	if err == nil {
		var pr ghPR
		if err := json.Unmarshal([]byte(out), &pr); err == nil {
			status.PullRequest = &domain.PullRequest{
				Number:       pr.Number,
				Title:        pr.Title,
				State:        pr.State,
				URL:          pr.URL,
				CommentCount: len(pr.Comments),
			}

			// Determine review status
			if len(pr.Reviews) > 0 {
				approved := false
				changesRequested := false
				for _, r := range pr.Reviews {
					if r.State == "APPROVED" {
						approved = true
					} else if r.State == "CHANGES_REQUESTED" {
						changesRequested = true
					}
				}
				switch {
				case changesRequested:
					status.PullRequest.ReviewStatus = "changes_requested"
				case approved:
					status.PullRequest.ReviewStatus = "approved"
				default:
					status.PullRequest.ReviewStatus = "pending"
				}
			} else {
				status.PullRequest.ReviewStatus = "none"
			}

			// Determine check status
			if len(pr.StatusCheckRollup) > 0 {
				passed := 0
				failed := 0
				pending := 0
				for _, c := range pr.StatusCheckRollup {
					switch c.State {
					case "SUCCESS":
						passed++
					case "FAILURE", "ERROR":
						failed++
					case "PENDING", "IN_PROGRESS":
						pending++
					}
				}
				status.PullRequest.ChecksSummary = fmt.Sprintf("%d/%d passed", passed, len(pr.StatusCheckRollup))
				switch {
				case failed > 0:
					status.PullRequest.ChecksStatus = "failure"
				case pending > 0:
					status.PullRequest.ChecksStatus = "pending"
				default:
					status.PullRequest.ChecksStatus = "success"
				}
			} else {
				status.PullRequest.ChecksStatus = "none"
			}
		}
	}

	return status, nil
}
