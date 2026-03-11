package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/google/uuid"
)

type FlowManager struct {
	jiraProvider domain.IssueProvider
	gitManager   domain.RepositoryManager
	sshManager   domain.RemoteExecutor
	slugger      domain.Slugger
	SkillEngine  domain.SkillEngine
}

func NewFlowManager(
	jiraProvider domain.IssueProvider,
	gitManager domain.RepositoryManager,
	sshManager domain.RemoteExecutor,
	slugger domain.Slugger,
	skillEngine domain.SkillEngine,
) *FlowManager {
	return &FlowManager{
		jiraProvider: jiraProvider,
		gitManager:   gitManager,
		sshManager:   sshManager,
		slugger:      slugger,
		SkillEngine:  skillEngine,
	}
}

func (m *FlowManager) CreateSession(ctx context.Context, config domain.SessionConfig) (*domain.Session, error) {
	// Step 2: Branch (Slugify if not provided)
	branch := config.Branch
	if branch == "" && config.IssueKey != "" {
		issue, err := m.jiraProvider.GetIssue(ctx, config.IssueKey)
		if err == nil {
			branch = m.slugger.Slugify(issue.Key, issue.Summary)
		}
	} else if branch != "" {
		// Ensure manually provided branch is sanitized
		// Note: we don't have a shared sanitizer here yet, 
		// but we should at least remove commas as requested.
		branch = strings.ReplaceAll(branch, ",", "-")
	}

	// Create Session record
	session := &domain.Session{
		ID:        uuid.New().String(),
		IssueKey:  config.IssueKey,
		Branch:    branch,
		RepoName:  config.Repo.Name,
		Status:    domain.SessionStatusProvisioning,
		CreatedAt: time.Now(),
	}

	// Step 6: Isolate (Worktree)
	var worktree domain.Worktree
	var err error
	if config.Repo.Name != "No Repository" && config.Repo.Name != "" {
		worktree, err = m.gitManager.SetupRemoteWorktree(ctx, m.sshManager, config.Repo, branch)
		if err != nil {
			return nil, fmt.Errorf("failed to setup worktree: %w", err)
		}
		session.WorktreePath = worktree.Path
	} else {
		session.WorktreePath = m.sshManager.GetRoot()
	}

	// Step 6.1: Persist Session ID in worktree
	if _, err = m.sshManager.Execute(ctx, fmt.Sprintf("echo %q > %q/.aiman-id", session.ID, session.WorktreePath)); err != nil {
		return nil, fmt.Errorf("failed to write session ID: %w", err)
	}

	// Step 7: Scope (Directory)
	workingDir := session.WorktreePath
	if config.Directory != "" && config.Directory != "." {
		// Remove leading/trailing slashes from config.Directory to avoid path issues
		cleanDir := strings.Trim(config.Directory, "/")
		workingDir = fmt.Sprintf("%s/%s", session.WorktreePath, cleanDir)
	}
	session.WorkingDirectory = workingDir

	// Ensure working directory exists (it might be a new folder defined by user)
	if _, err := m.sshManager.Execute(ctx, fmt.Sprintf("mkdir -p %q", workingDir)); err != nil {
		return nil, fmt.Errorf("failed to create working directory: %w", err)
	}

	// Step 9 & 10: Skills & Agent
	agentCmd := config.Agent.Command
	if m.SkillEngine != nil {
		preparedCmd, err := m.SkillEngine.PrepareSession(ctx, m.sshManager, workingDir, *config.Agent, config.Skills, config.PromptFree)
		if err == nil {
			agentCmd = preparedCmd
		}
	}

	// Step 8: Session (Tmux)
	tmuxName := strings.ReplaceAll(branch, "/", "-")
	// Start tmux session with the agent command directly.
	// We use remain-on-exit so if the agent fails, the window stays open to see the error.
	// We set AIMAN_ID environment variable for the session.
	startCmd := fmt.Sprintf("tmux new-session -d -s %q -c %q \"export AIMAN_ID=%q; %s\"", tmuxName, workingDir, strings.TrimSpace(session.ID), agentCmd)
	_, err = m.sshManager.Execute(ctx, startCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to start tmux session: %w", err)
	}
	// Set remain-on-exit so the window doesn't disappear if the agent fails
	_, _ = m.sshManager.Execute(ctx, fmt.Sprintf("tmux set-option -t %q remain-on-exit on", tmuxName))
	
	session.TmuxSession = tmuxName

	if err := session.Transition(domain.SessionStatusActive); err != nil {
		return nil, err
	}

	return session, nil
}

// Deprecated: Use CreateSession instead
func (m *FlowManager) StartNewFlow(ctx context.Context, issueKey string, repoName string) (*domain.Session, error) {
	return m.CreateSession(ctx, domain.SessionConfig{
		IssueKey:   issueKey,
		Repo:       domain.Repo{Name: repoName},
		Agent:      &domain.Agent{Name: "Claude Code", Command: "claude"}, // Default
		PromptFree: true,
	})
}
