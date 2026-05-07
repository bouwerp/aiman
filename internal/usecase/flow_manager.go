package usecase

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
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

	// Step 2: Branch / label derivation
	branch := config.Branch
	if config.AdHoc {
		// Ad-hoc: use the label as-is (already sanitized by UI), fall back to timestamp.
		if branch == "" {
			branch = "adhoc-" + time.Now().Format("20060102-1504")
		}
	} else if !config.ExistingBranch {
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
	if !config.AdHoc && config.Issue == nil && config.IssueKey != "" {
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
	if config.AdHoc {
		// Ad-hoc sessions run in the SSH root; no git worktree needed.
		session.WorktreePath = sshMgr.GetRoot()
	} else if config.Repo.Name != "No Repository" && config.Repo.Name != "" {
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

	if !config.AdHoc {
		// Ignore session-local task stub so it is not committed from the worktree.
		_ = m.gitManager.EnsureAimanTaskGitignored(ctx, sshMgr, session.WorktreePath)

		// Step 6.1: Persist Session ID in git metadata (safe from git status/commits)
		if _, err = sshMgr.Execute(ctx, fmt.Sprintf("id_file=$(git -C %q rev-parse --git-dir)/aiman-id && echo %q > \"$id_file\"", session.WorktreePath, session.ID)); err != nil {
			return nil, fmt.Errorf("failed to write session ID: %w", err)
		}
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
		prepared, err := m.SkillEngine.PrepareSession(ctx, sshMgr, workingDir, *config.Agent, config.Skills, config.PromptFree, config.Issue, config.PriorSnapshot)
		if err == nil {
			agentCmd = prepared.Command
			sendKeysPrompt = prepared.InitialPrompt
		}
	}

	// Step 8: Session (Tmux)
	tmuxName := strings.ReplaceAll(branch, "/", "-")

	// Push session-scoped AWS credentials BEFORE starting tmux so AWS_PROFILE is
	// available to the agent from the very first command.
	var awsProfileName string
	if config.AWSConfig != nil && sshMgr != nil {
		if pn, pushErr := PushSessionAWSCredentials(ctx, sshMgr, session.ID, config.AWSConfig); pushErr == nil {
			awsProfileName = pn
			session.AWSProfileName = pn
			session.AWSConfig = config.AWSConfig
		}
		// Non-fatal — session starts without session-scoped credentials on error.
	}

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
	agentBootstrap := fmt.Sprintf("export PATH=\"$PATH:$HOME/.local/bin:$HOME/.npm-global/bin:$HOME/bin:$HOME/.bun/bin:$HOME/.local/share/pnpm:$HOME/.pnpm:$HOME/.yarn/bin:$HOME/.cargo/bin:/usr/local/bin:/opt/homebrew/bin:$HOME/.opencode/bin\"; %s", agentCmd)
	// Escape single quotes for bash -c '...'
	agentBootstrap = strings.ReplaceAll(agentBootstrap, "'", "'\\''")

	extraEnvFlags := ""
	if awsProfileName != "" {
		extraEnvFlags += fmt.Sprintf(" -e AWS_PROFILE=%s", awsProfileName)
	}
	// Ensure OpenCode runs in auto-approve mode. Two mechanisms are used for
	// maximum compatibility across versions:
	//   1. OPENCODE_CONFIG=/tmp/opencode-aiman.json — works with all versions but
	//      can be overridden by a project-level opencode.json (precedence 3 of 8).
	//   2. OPENCODE_CONFIG_CONTENT — newer OpenCode versions only; highest user
	//      precedence (position 6 of 8), overrides even project config.
	// The correct top-level format for all-permissions is the string "allow", not
	// an object like {"*":"allow"}.
	if strings.Contains(strings.ToLower(agentCmd), "opencode") {
		_ = sshMgr.WriteFile(ctx, "/tmp/opencode-aiman.json", []byte(`{"permission":"allow"}`))
		extraEnvFlags += ` -e OPENCODE_CONFIG=/tmp/opencode-aiman.json`
		extraEnvFlags += ` -e 'OPENCODE_CONFIG_CONTENT={"permission":"allow"}'`
	}
	if config.OpenRouterAPIKey != "" {
		extraEnvFlags += fmt.Sprintf(" -e OPENROUTER_API_KEY=%s", config.OpenRouterAPIKey)
	}
	for _, secret := range config.EnvSecrets {
		extraEnvFlags += fmt.Sprintf(" -e %s=%s", secret.Key, secret.Value)
	}
	// Ensure the tmux server is running before touching global options
	// (set-window-option -g fails silently if no server exists yet, leaving
	// the default remain-on-exit=off that would cause the session to vanish
	// the moment the pane process exits for any reason).
	//
	// Also temporarily disable destroy-unattached in case ~/.tmux.conf sets
	// it — that option kills sessions with no attached clients.
	startCmd := fmt.Sprintf(
		"tmux start-server 2>/dev/null || true; "+
			"tmux set-option -g destroy-unattached off 2>/dev/null || true; "+
			"tmux set-window-option -g remain-on-exit on 2>/dev/null || true; "+
			"tmux new-session -d -s %q -c %q -e AIMAN_ID=%s%s \"bash -l -c '%s'; exec bash -i\"; "+
			"_RC=$?; "+
			"tmux set-window-option -t %q remain-on-exit on 2>/dev/null || true; "+
			"tmux set-window-option -g remain-on-exit off 2>/dev/null || true; "+
			"tmux set-option -g destroy-unattached off 2>/dev/null || true; "+
			"exit $_RC",
		tmuxName, workingDir, strings.TrimSpace(session.ID), extraEnvFlags, agentBootstrap, tmuxName,
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
				"sleep 3; "+
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

// PushSessionAWSCredentials generates a session-scoped AWS profile name, obtains
// temporary credentials locally, and pushes them to the remote under that profile.
// Returns the profile name on success (e.g. "aiman-a1b2c3d4").
func PushSessionAWSCredentials(ctx context.Context, r domain.RemoteExecutor, sessionID string, cfg *domain.AWSConfig) (string, error) {
	profileName := "aiman-" + sessionID[:8]

	accountID := cfg.AccountID
	if accountID == "" {
		var err error
		accountID, err = awsdelegation.AccountIDFromLocalProfile(ctx, cfg.SourceProfile)
		if err != nil {
			return "", fmt.Errorf("aws: derive account ID: %w", err)
		}
	}

	roleARN, err := awsdelegation.RoleARNFromParts(accountID, cfg.RoleName)
	if err != nil {
		return "", fmt.Errorf("aws: build role ARN: %w", err)
	}

	creds, err := awsdelegation.GetTemporaryCredentials(ctx, cfg.SourceProfile, awsdelegation.CredentialOptions{
		RoleARN:         roleARN,
		SessionName:     profileName,
		SessionPolicy:   cfg.SessionPolicy,
		DurationSeconds: cfg.DurationSeconds,
	})
	if err != nil {
		return "", fmt.Errorf("aws: get temporary credentials: %w", err)
	}

	if err := awsdelegation.ApplyDelegatedCredentials(ctx, r, profileName, creds); err != nil {
		return "", fmt.Errorf("aws: push credentials: %w", err)
	}

	if err := awsdelegation.ApplyDelegatedProfile(ctx, r, profileName, roleARN, cfg.SourceProfile, cfg.Region); err != nil {
		return "", fmt.Errorf("aws: push profile: %w", err)
	}

	return profileName, nil
}
