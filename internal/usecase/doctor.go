package usecase

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

type CheckResult struct {
	Name    string
	Passed  bool
	Message string
}

type Doctor struct {
	cfg          *config.Config
	jiraProvider domain.IssueProvider
	gitManager   domain.RepositoryManager
}

func NewDoctor(cfg *config.Config, jiraProvider domain.IssueProvider, gitManager domain.RepositoryManager) *Doctor {
	return &Doctor{
		cfg:          cfg,
		jiraProvider: jiraProvider,
		gitManager:   gitManager,
	}
}

func (d *Doctor) RunAll(ctx context.Context) []CheckResult {
	results := []CheckResult{}

	results = append(results,
		d.CheckJira(ctx),
		d.CheckGit(ctx),
		d.CheckSSH(ctx),
	)

	return results
}

func (d *Doctor) CheckJira(ctx context.Context) CheckResult {
	if d.cfg.Integrations.Jira.URL == "" {
		return CheckResult{Name: "JIRA", Passed: false, Message: "JIRA URL not configured"}
	}

	// Search with empty query to get recent issues (better connectivity test)
	issues, err := d.jiraProvider.SearchIssues(ctx, "")
	if err != nil {
		return CheckResult{Name: "JIRA", Passed: false, Message: fmt.Sprintf("Authentication failed: %v", err)}
	}

	return CheckResult{Name: "JIRA", Passed: true, Message: fmt.Sprintf("Authenticated successfully (%d recent issues)", len(issues))}
}

func (d *Doctor) CheckGit(ctx context.Context) CheckResult {
	// Check if gh is installed and authenticated
	cmd := exec.CommandContext(ctx, "gh", "auth", "status")
	if err := cmd.Run(); err != nil {
		return CheckResult{Name: "Git/GitHub", Passed: false, Message: "GitHub CLI (gh) not authenticated"}
	}

	repos, err := d.gitManager.ListRepos(ctx)
	if err != nil {
		return CheckResult{Name: "Git/GitHub", Passed: false, Message: fmt.Sprintf("Failed to list repositories: %v", err)}
	}

	return CheckResult{Name: "Git/GitHub", Passed: true, Message: fmt.Sprintf("Access to %d repositories verified", len(repos))}
}

func (d *Doctor) CheckSSH(ctx context.Context) CheckResult {
	if len(d.cfg.Remotes) == 0 {
		return CheckResult{Name: "SSH", Passed: false, Message: "No remote dev servers configured"}
	}

	passedCount := 0
	for _, remote := range d.cfg.Remotes {
		target := remote.Host
		if remote.User != "" {
			target = fmt.Sprintf("%s@%s", remote.User, remote.Host)
		}
		// Try to connect with a short timeout
		cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=2", target, "exit")
		if err := cmd.Run(); err == nil {
			passedCount++
		}
	}

	if passedCount == 0 {
		return CheckResult{Name: "SSH", Passed: false, Message: "None of the configured remote servers are accessible"}
	}

	return CheckResult{Name: "SSH", Passed: true, Message: fmt.Sprintf("%d/%d remote servers accessible", passedCount, len(d.cfg.Remotes))}
}
