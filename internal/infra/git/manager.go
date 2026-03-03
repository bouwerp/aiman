package git

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
)

type Manager struct{}

func NewManager() *Manager {
	return &Manager{}
}

type ghRepo struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

func (m *Manager) ListRepos(ctx context.Context) ([]domain.Repo, error) {
	// Execute gh repo list --limit 100 --json name,url
	cmd := exec.CommandContext(ctx, "gh", "repo", "list", "--limit", "100", "--json", "name,url")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("failed to list repositories: %w, output: %s", err, string(output))
	}

	var ghRepos []ghRepo
	if err := json.Unmarshal(output, &ghRepos); err != nil {
		return nil, fmt.Errorf("failed to parse gh output: %w", err)
	}

	repos := make([]domain.Repo, len(ghRepos))
	for i, r := range ghRepos {
		repos[i] = domain.Repo{
			Name: r.Name,
			URL:  r.URL,
		}
	}

	return repos, nil
}

func (m *Manager) SetupWorktree(ctx context.Context, repo domain.Repo, branch string) (domain.Worktree, error) {
	// git worktree add ../<branch-name>
	// Step 6 of SPEC
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
