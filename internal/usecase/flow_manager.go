package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/google/uuid"
)

type FlowManager struct {
	jiraProvider domain.IssueProvider
	jiraConfig   *config.JiraConfig
	gitManager   domain.RepositoryManager
	sshManager   domain.RemoteExecutor
	slugger      domain.Slugger
	SkillEngine  domain.SkillEngine
}

func geminiGlobalTrustCmd(workingDir string) string {
	return fmt.Sprintf(
		`cd %q && if command -v gemini >/dev/null 2>&1; then `+
			`mkdir -p "$HOME/.gemini"; `+
			`tf="$HOME/.gemini/trustedFolders.json"; `+
			`if [ ! -s "$tf" ]; then printf '{}' > "$tf"; fi; `+
			`if command -v node >/dev/null 2>&1; then `+
			`WORKDIR=%q TF="$tf" node -e "const fs=require('fs');const p=process.env.WORKDIR;const f=process.env.TF;let j={};try{j=JSON.parse(fs.readFileSync(f,'utf8')||'{}')}catch{j={}};j[p]='TRUST_FOLDER';fs.writeFileSync(f,JSON.stringify(j,null,2),{mode:0o600})" >/dev/null 2>&1 || true; `+
			`fi; `+
			`gemini config set --global security.folderTrust.enabled true >/dev/null 2>&1 || true; `+
			`fi`,
		workingDir, workingDir,
	)
}

func NewFlowManager(
	jiraProvider domain.IssueProvider,
	jiraConfig *config.JiraConfig,
	gitManager domain.RepositoryManager,
	sshManager domain.RemoteExecutor,
	slugger domain.Slugger,
	skillEngine domain.SkillEngine,
) *FlowManager {
	return &FlowManager{
		jiraProvider: jiraProvider,
		jiraConfig:   jiraConfig,
		gitManager:   gitManager,
		sshManager:   sshManager,
		slugger:      slugger,
		SkillEngine:  skillEngine,
	}
}

func (m *FlowManager) CreateSession(ctx context.Context, config domain.SessionConfig) (*domain.Session, error) {
	// Resolve which SSH manager to use (per-session remote overrides the default)
	sshMgr := m.sshManager
	if config.SSHManager != nil {
		sshMgr = config.SSHManager
	}

	// Step 2: Branch (Slugify if not provided, skip for existing branch sessions)
	branch := config.Branch
	if !config.ExistingBranch {
		if branch == "" && config.IssueKey != "" {
			issue, err := m.jiraProvider.GetIssue(ctx, config.IssueKey)
			if err == nil {
				branch = m.slugger.Slugify(issue.Key, issue.Summary)
				// Store the fetched issue so it can be used for initial prompt injection
				if config.Issue == nil {
					config.Issue = &issue
				}
			}
		} else if branch != "" {
			branch = domain.SanitizeBranchName(branch)
		}
	}

	// Ensure we have full issue context for task-file/prompt injection even when
	// a branch was already provided (e.g. restart/existing-branch flows).
	if config.Issue == nil && config.IssueKey != "" {
		if issue, err := m.jiraProvider.GetIssue(ctx, config.IssueKey); err == nil {
			config.Issue = &issue
		}
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
		if config.ExistingBranch {
			worktree, err = m.gitManager.SetupRemoteWorktreeFromBranch(ctx, sshMgr, config.Repo, branch)
		} else {
			worktree, err = m.gitManager.SetupRemoteWorktree(ctx, sshMgr, config.Repo, branch)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to setup worktree: %w", err)
		}
		session.WorktreePath = worktree.Path
	} else {
		session.WorktreePath = sshMgr.GetRoot()
	}

	// Ignore session-local task stub so it is not committed from the worktree.
	_ = m.gitManager.EnsureAimanTaskGitignored(ctx, sshMgr, session.WorktreePath)

	// Step 6.1: Persist Session ID in git metadata (safe from git status/commits)
	if _, err = sshMgr.Execute(ctx, fmt.Sprintf("id_file=$(git -C %q rev-parse --git-dir)/aiman-id && echo %q > \"$id_file\"", session.WorktreePath, session.ID)); err != nil {
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
	if _, err := sshMgr.Execute(ctx, fmt.Sprintf("mkdir -p %q", workingDir)); err != nil {
		return nil, fmt.Errorf("failed to create working directory: %w", err)
	}

	// Step 9 & 10: Skills & Agent
	agentCmd := config.Agent.Command
	var sendKeysPrompt string
	if m.SkillEngine != nil {
		prepared, err := m.SkillEngine.PrepareSession(ctx, sshMgr, workingDir, *config.Agent, config.Skills, config.PromptFree, config.Issue)
		if err == nil {
			agentCmd = prepared.Command
			sendKeysPrompt = prepared.InitialPrompt
		}
	}

	// Step 8: Session (Tmux)
	tmuxName := strings.ReplaceAll(branch, "/", "-")
	// Start the session and immediately set remain-on-exit in a single SSH call to avoid
	// a race condition: if the agent exits before the separate set-option call runs, the
	// session (and server) would already be gone.
	// We also append "; exec bash" so that if the agent exits for any reason, the pane
	// drops to an interactive shell instead of closing — the user can inspect the error.
	// Use tmux's -e flag to inject AIMAN_ID into the tmux session environment so
	// that `tmux show-environment` can reliably retrieve it during discovery.
	// Exporting it only inside the bash command would make it available to the shell
	// but invisible to tmux show-environment, causing discovery to generate a random
	// UUID and produce duplicate session entries.
	// The login shell (-l) ensures PATH is populated from ~/.bash_profile / ~/.profile
	// so tools like claude that are installed in ~/.local/bin are found.
	// We also append common user-local bin paths explicitly to avoid false
	// "command not found" failures for tools installed outside default login PATH.
	agentBootstrap := fmt.Sprintf("export PATH=\"$PATH:$HOME/.local/bin:$HOME/.npm-global/bin:$HOME/bin:$HOME/.bun/bin:$HOME/.local/share/pnpm:$HOME/.pnpm:$HOME/.yarn/bin:$HOME/.cargo/bin:/usr/local/bin:/opt/homebrew/bin\"; %s", agentCmd)
	startCmd := fmt.Sprintf(
		"tmux new-session -d -s %q -c %q -e AIMAN_ID=%s \"bash -l -c '%s; exec bash'\" && tmux set-option -p -t %q remain-on-exit on",
		tmuxName, workingDir, strings.TrimSpace(session.ID), agentBootstrap, tmuxName,
	)
	_, err = sshMgr.Execute(ctx, startCmd)
	if err != nil {
		return nil, fmt.Errorf("failed to start tmux session: %w", err)
	}

	// If the agent doesn't support an inline initial prompt (i.e. it would run
	// headlessly and exit), we send the prompt via tmux send-keys after a short
	// delay so the agent has time to start up interactively.
	if sendKeysPrompt != "" {
		sendCmd := fmt.Sprintf(
			"attempt=0; "+
				"while [ $attempt -lt 20 ]; do "+
				"pane_cmd=$(tmux display-message -p -t %q '#{pane_current_command}' 2>/dev/null || true); "+
				"if [ \"$pane_cmd\" != \"bash\" ] && [ \"$pane_cmd\" != \"sh\" ] && [ \"$pane_cmd\" != \"zsh\" ]; then break; fi; "+
				"attempt=$((attempt+1)); sleep 1; "+
				"done; "+
				"tmux send-keys -t %q -l %q && sleep 1 && tmux send-keys -t %q Enter",
			tmuxName,
			tmuxName, sendKeysPrompt,
			tmuxName,
		)
		// Fire-and-forget in the background; failure here is non-fatal.
		_, _ = sshMgr.Execute(ctx, fmt.Sprintf("nohup bash -c %q >/dev/null 2>&1 &", sendCmd))
	}

	session.TmuxSession = tmuxName

	// Trust the directory (Git safe.directory and Claude trust)
	// This ensures agents and tools can operate without permission prompts.
	trustCmd := fmt.Sprintf("git config --global --add safe.directory %q", workingDir)
	_, _ = sshMgr.Execute(ctx, trustCmd)

	claudeTrustCmd := fmt.Sprintf("cd %q && if command -v claude >/dev/null; then claude trust . >/dev/null 2>&1; fi", workingDir)
	_, _ = sshMgr.Execute(ctx, claudeTrustCmd)

	copilotTrustCmd := fmt.Sprintf("cd %q && if command -v copilot >/dev/null; then copilot trust . >/dev/null 2>&1 || copilot trust add . >/dev/null 2>&1; fi", workingDir)
	_, _ = sshMgr.Execute(ctx, copilotTrustCmd)

	ghCopilotTrustCmd := fmt.Sprintf("cd %q && if command -v gh >/dev/null; then gh copilot trust . >/dev/null 2>&1 || gh copilot trust add . >/dev/null 2>&1; fi", workingDir)
	_, _ = sshMgr.Execute(ctx, ghCopilotTrustCmd)

	_, _ = sshMgr.Execute(ctx, geminiGlobalTrustCmd(workingDir))

	// Transition JIRA issue if configured
	if session.IssueKey != "" && m.jiraConfig != nil && m.jiraConfig.TransitionStatus != "" {
		_ = m.jiraProvider.TransitionIssue(ctx, session.IssueKey, m.jiraConfig.TransitionStatus)
	}

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
