package usecase

import (
	"context"
	"fmt"
	"os/exec"
	"sync"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
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
	// `gh auth status` is the whole question this check answers. It used to also
	// call ListRepos, which fetches every personal and org repository over the
	// network purely to report a count, then discards the list — the repo picker
	// fetches them all again when it is actually opened. That one call was ~3 s
	// of a ~3.7 s check.
	cmd := exec.CommandContext(ctx, "gh", "auth", "status")
	if err := cmd.Run(); err != nil {
		return CheckResult{Name: "Git/GitHub", Passed: false, Message: "GitHub CLI (gh) not authenticated"}
	}

	return CheckResult{Name: "Git/GitHub", Passed: true, Message: "GitHub CLI authenticated"}
}

func (d *Doctor) CheckSSH(ctx context.Context) CheckResult {
	if len(d.cfg.Remotes) == 0 {
		return CheckResult{Name: "SSH", Passed: false, Message: "No remote dev servers configured"}
	}

	// Probe remotes concurrently and over the shared ControlMaster socket. A
	// fresh connection per remote measured ~1.25 s against ~0.25 s multiplexed,
	// and doing them one after another multiplied that by the remote count.
	var wg sync.WaitGroup
	results := make([]bool, len(d.cfg.Remotes))
	for i, remote := range d.cfg.Remotes {
		wg.Add(1)
		go func(i int, remote config.Remote) {
			defer wg.Done()
			mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
			results[i] = mgr.Connect(ctx) == nil
		}(i, remote)
	}
	wg.Wait()

	passedCount := 0
	for _, ok := range results {
		if ok {
			passedCount++
		}
	}

	if passedCount == 0 {
		return CheckResult{Name: "SSH", Passed: false, Message: "None of the configured remote servers are accessible"}
	}

	return CheckResult{Name: "SSH", Passed: true, Message: fmt.Sprintf("%d/%d remote servers accessible", passedCount, len(d.cfg.Remotes))}
}
