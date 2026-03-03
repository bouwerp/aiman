package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/bouwerp/aiman/internal/domain"
)

type FlowManager struct {
	jiraProvider  domain.IssueProvider
	gitManager    domain.RepositoryManager
	sshManager    domain.RemoteExecutor
	slugger       domain.Slugger
}

func NewFlowManager(
	jiraProvider domain.IssueProvider,
	gitManager domain.RepositoryManager,
	sshManager domain.RemoteExecutor,
	slugger domain.Slugger,
) *FlowManager {
	return &FlowManager{
		jiraProvider: jiraProvider,
		gitManager:   gitManager,
		sshManager:   sshManager,
		slugger:      slugger,
	}
}

func (m *FlowManager) StartNewFlow(ctx context.Context, issueKey string, repoName string) (*domain.Session, error) {
	// Step 1: Issue
	issue, err := m.jiraProvider.GetIssue(ctx, issueKey)
	if err != nil {
		return nil, fmt.Errorf("step 1 failed: %w", err)
	}

	// Step 2: Branch
	branch := m.slugger.Slugify(issue.Key, issue.Summary)

	// Step 3: Repo (In a real implementation, we'd find the repo object)
	repo := domain.Repo{Name: repoName}

	// Create Session record
	session := &domain.Session{
		ID:        uuid.New().String(),
		IssueKey:  issue.Key,
		Branch:    branch,
		RepoName:  repo.Name,
		Status:    domain.SessionStatusProvisioning,
		CreatedAt: time.Now(),
	}

	// Step 4: Connect
	// if err := m.sshManager.Connect(ctx, "dev-box.internal"); err != nil {
	//	return nil, fmt.Errorf("step 4 failed: %w", err)
	// }

	// Step 6: Isolate (Worktree)
	worktree, err := m.gitManager.SetupWorktree(ctx, repo, branch)
	if err != nil {
		return nil, fmt.Errorf("step 6 failed: %w", err)
	}
	session.WorktreePath = worktree.Path

	// Step 8: Session (Tmux)
	tmuxName := strings.ReplaceAll(branch, "/", "-")
	if err := m.sshManager.StartTmuxSession(ctx, tmuxName); err != nil {
		return nil, fmt.Errorf("step 8 failed: %w", err)
	}
	session.TmuxSession = tmuxName

	if err := session.Transition(domain.SessionStatusActive); err != nil {
		return nil, err
	}

	return session, nil
}
