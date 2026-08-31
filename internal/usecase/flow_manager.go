package usecase

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
	"github.com/bouwerp/aiman/internal/infra/config"
	infraGit "github.com/bouwerp/aiman/internal/infra/git"
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

// joinPrompt appends user-entered prompt text to a base agent prompt. The base is
// typically the JIRA task trigger (empty for ad-hoc sessions). A single space
// separates the two parts so the combined prompt is delivered as one line via
// tmux send-keys (newlines are deliberately avoided — some agents submit on them).
func joinPrompt(base, user string) string {
	base = strings.TrimSpace(base)
	user = strings.TrimSpace(user)
	switch {
	case base == "":
		return user
	case user == "":
		return base
	default:
		return base + " " + user
	}
}

// promptDeliverer is the narrow slice of RemoteExecutor that deliverInitialPrompt
// needs. Keeping it small makes the delivery path independently testable.
type promptDeliverer interface {
	WriteFile(ctx context.Context, path string, content []byte) error
	Execute(ctx context.Context, cmd string) (string, error)
}

// sendKeysScript builds the remote shell script that waits for the agent to come
// up, types the prompt, submits it, then removes the prompt file. The prompt is
// read from promptPath via command substitution ("$(cat ...)") rather than
// interpolated into the command, so its contents are never parsed by any shell.
func sendKeysScript(tmuxName, promptPath string, acceptWorkspaceTrust bool) string {
	wait := fmt.Sprintf(
		"attempt=0; "+
			"while [ $attempt -lt 20 ]; do "+
			"pane_cmd=$(tmux display-message -p -t %q '#{pane_current_command}' 2>/dev/null || true); "+
			"if [ \"$pane_cmd\" != \"bash\" ] && [ \"$pane_cmd\" != \"sh\" ] && [ \"$pane_cmd\" != \"zsh\" ]; then break; fi; "+
			"attempt=$((attempt+1)); sleep 1; "+
			"done; "+
			"sleep 3; ",
		tmuxName,
	)
	if acceptWorkspaceTrust {
		// agy is launched through a wrapper shell, so pane_current_command stays
		// "bash" and the readiness loop above never breaks. Poll the rendered pane
		// instead: accept the workspace-trust dialog if it appears (agy's
		// --dangerously-skip-permissions flag does not dismiss it), otherwise break
		// once the chat input is ready.
		wait = fmt.Sprintf(
			"attempt=0; "+
				"while [ $attempt -lt 10 ]; do "+
				"pane=$(tmux capture-pane -t %q -p 2>/dev/null || true); "+
				"if printf '%%s' \"$pane\" | grep -qi 'trust this folder'; then "+
				"tmux send-keys -t %q Enter; sleep 3; break; "+
				"fi; "+
				"if printf '%%s' \"$pane\" | grep -qi '? for shortcuts'; then break; fi; "+
				"attempt=$((attempt+1)); sleep 1; "+
				"done; "+
				"sleep 1; ",
			tmuxName, tmuxName,
		)
	}
	if promptPath == "" {
		return wait
	}
	return wait + fmt.Sprintf(
		"tmux send-keys -t %q -l -- \"$(cat %q)\" && sleep 1 && tmux send-keys -t %q Enter; "+
			"rm -f %q",
		tmuxName, promptPath, tmuxName, promptPath,
	)
}

// detachCommand wraps a script to run in the background via bash. The script is
// single-quote escaped so the remote login shell passes it to bash verbatim
// without interpreting its $(...) substitutions first.
func detachCommand(script string) string {
	escaped := strings.ReplaceAll(script, "'", `'\''`)
	return fmt.Sprintf("nohup bash -c '%s' >/dev/null 2>&1 &", escaped)
}

// DeliverInitialPrompt sends the initial prompt to a freshly-started agent via
// tmux send-keys. The prompt is written to a temp file (raw bytes over stdin, no
// shell parsing) and read back on the remote, so arbitrary user-entered text —
// including shell metacharacters — can never be executed. Best-effort: a write
// failure aborts prompt delivery, and the background send itself is
// fire-and-forget.
//
// When acceptWorkspaceTrust is set (Antigravity CLI / agy), the delivery script
// also accepts agy's workspace-trust dialog before sending the prompt: agy shows
// "Do you trust this folder?" on first run in a directory and its
// --dangerously-skip-permissions flag does not dismiss it, so without this the
// prompt lands in the dialog instead of the agent. The script still runs when
// the prompt is empty in that case, so the dialog is cleared even for ad-hoc
// agy sessions.
func DeliverInitialPrompt(ctx context.Context, remote promptDeliverer, tmuxName, sessionID, prompt string, acceptWorkspaceTrust bool) {
	if prompt == "" && !acceptWorkspaceTrust {
		return
	}
	var promptPath string
	if prompt != "" {
		promptPath = fmt.Sprintf("/tmp/aiman-prompt-%s", strings.TrimSpace(sessionID))
		if err := remote.WriteFile(ctx, promptPath, []byte(prompt)); err != nil {
			if !acceptWorkspaceTrust {
				return
			}
			promptPath = "" // cannot deliver the prompt, but still clear the trust dialog
		}
	}
	_, _ = remote.Execute(ctx, detachCommand(sendKeysScript(tmuxName, promptPath, acceptWorkspaceTrust)))
}

// SendPrompt types text into an already-running tmux session using the
// file-backed send-keys path. It does not wait for agent startup and does
// not detach, so the caller can wait on pane state afterwards.
func SendPrompt(ctx context.Context, remote promptDeliverer, tmuxName, sessionID, prompt string) error {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil
	}
	promptPath := fmt.Sprintf("/tmp/aiman-prompt-%s", strings.TrimSpace(sessionID))
	if err := remote.WriteFile(ctx, promptPath, []byte(prompt)); err != nil {
		return err
	}
	script := fmt.Sprintf(
		"tmux send-keys -t %q -l -- \"$(cat %q)\" && sleep 1 && tmux send-keys -t %q Enter; rm -f %q",
		tmuxName, promptPath, tmuxName, promptPath,
	)
	_, err := remote.Execute(ctx, script)
	return err
}

// IsAntigravityAgent reports whether the agent is Antigravity CLI (agy). agy is
// special-cased for prompt delivery because it presents a workspace-trust dialog
// on first run that --dangerously-skip-permissions does not dismiss.
func IsAntigravityAgent(name, command string) bool {
	if strings.Contains(strings.ToLower(name), "antigravity") {
		return true
	}
	fields := strings.Fields(strings.TrimSpace(strings.ToLower(command)))
	return len(fields) > 0 && fields[0] == "agy"
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
		// Ad-hoc sessions use the Git-safe identifier, falling back to a timestamp.
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
	agentName := ""
	if config.Agent != nil {
		agentName = config.Agent.Name
	}
	mode := config.Mode
	if mode == "" {
		mode = domain.SessionModeInteractive
	}

	session := &domain.Session{
		ID:             uuid.New().String(),
		Name:           config.Name,
		Group:          config.Group,
		ParentID:       config.ParentID,
		IssueKey:       config.IssueKey,
		Branch:         branch,
		RepoName:       config.Repo.Name,
		AgentName:      agentName,
		Status:         domain.SessionStatusProvisioning,
		Mode:           mode,
		TriggerSource:  config.TriggerSource,
		TriggerEventID: config.TriggerEventID,
		CreatedAt:      time.Now(),
	}

	// Step 6: Isolate (Worktree)
	var worktree domain.Worktree
	var err error
	switch {
	case config.AdHoc:
		// Ad-hoc sessions get their own subdirectory under the repos root so
		// they never run in the root itself.  Use the branch label (already
		// sanitized by the UI) as the directory name.
		adHocDir := branch
		if adHocDir == "" {
			adHocDir = "adhoc-" + time.Now().Format("20060102-1504")
		}
		session.WorktreePath = fmt.Sprintf("%s/%s", strings.TrimRight(sshMgr.GetRoot(), "/"), adHocDir)
	case config.Repo.Name != "No Repository" && config.Repo.Name != "":
		switch {
		case config.AttachExisting:
			worktree, err = m.gitManager.FindExistingWorktree(ctx, sshMgr, config.Repo, branch)
		case config.ReuseWorkspace:
			worktree, err = m.gitManager.SetupSharedWorkspace(ctx, sshMgr, config.Repo, branch)
		case config.ExistingBranch:
			worktree, err = m.gitManager.SetupRemoteWorktreeFromBranch(ctx, sshMgr, config.Repo, branch)
		default:
			worktree, err = m.gitManager.SetupRemoteWorktree(ctx, sshMgr, config.Repo, branch, config.BaseBranch)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to setup worktree: %w", err)
		}
		// Safety: sessions must never run inside the main repository directory.
		if !config.ReuseWorkspace {
			mainRepo := infraGit.ComputeMainRepoPath(sshMgr.GetRoot(), config.Repo.Name)
			if filepath.Clean(worktree.Path) == mainRepo {
				return nil, fmt.Errorf("safety: session working directory %q is the main repository — sessions must use a git worktree (a sibling directory), not the main repo itself", worktree.Path)
			}
		}
		session.WorktreePath = worktree.Path

	default:
		// No repository selected — create a named subdirectory under the root
		// so the session never runs directly in the root.
		subDir := branch
		if subDir == "" {
			subDir = "session-" + time.Now().Format("20060102-1504")
		}
		session.WorktreePath = fmt.Sprintf("%s/%s", strings.TrimRight(sshMgr.GetRoot(), "/"), subDir)
	}

	if !config.AdHoc {
		// Ignore session-local task stub so it is not committed from the worktree.
		_ = m.gitManager.EnsureAimanSessionFilesGitignored(ctx, sshMgr, session.WorktreePath)

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
	sendKeysPrompt = InjectSharedContext(ctx, sshMgr, workingDir, session.Group, session.RepoName, sendKeysPrompt)
	// Append any free-text prompt entered in the summary dialog. For JIRA sessions
	// this follows the "Read .aiman_task.md…" trigger; for ad-hoc sessions it becomes
	// the entire prompt.
	sendKeysPrompt = joinPrompt(sendKeysPrompt, config.InitialPrompt)

	// Step 8: Session (Tmux). The display Name is a renameable alias and must
	// not become the tmux target; kill/capture/attach stay on the branch.
	tmuxName := tmuxNameForCreate(branch, session.Name)

	// Set AWS environment variables using the globally synced profile.
	awsEnv := map[string]string{}
	if config.AWSConfig != nil && sshMgr != nil {
		awsEnv = SharedSessionAWSEnv(config.AWSConfig.SourceProfile, config.AWSConfig.Region)
		session.AWSConfig = config.AWSConfig
	}

	// Trust the workspace before the agent boots. Claude Code reads
	// ~/.claude.json at startup; trusting afterwards leaves the dialog up
	// (and a prompt typed into it can quit the agent).
	infraGit.ReportProgress(ctx, "Trusting workspace…")
	m.trustWorkspaceBeforeLaunch(ctx, sshMgr, workingDir, session.WorktreePath)

	// PTY is the default runtime. Env travels in the create payload, so no
	// -e flags or shell escaping are needed; the command runs under bash -l
	// inside a real PTY. Explicit session_backend: tmux still uses tmux.
	config.SessionBackend = domain.ResolveSessionBackend(config.SessionBackend, "")
	if config.SessionBackend == domain.BackendPTY {
		return m.launchPTYSession(ctx, sshMgr, session, config, workingDir, tmuxName, agentCmd, sendKeysPrompt, awsEnv)
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
	agentBootstrap := fmt.Sprintf("export PATH=\"$PATH:$HOME/.local/bin:$HOME/.kilo/bin:$HOME/.npm-global/bin:$HOME/bin:$HOME/.bun/bin:$HOME/.local/share/pnpm:$HOME/.pnpm:$HOME/.yarn/bin:$HOME/.cargo/bin:/usr/local/bin:/opt/homebrew/bin\"; %s", agentCmd)
	// Escape single quotes for bash -c '...'
	agentBootstrap = strings.ReplaceAll(agentBootstrap, "'", "'\\''")

	extraEnvFlags := tmuxEnvFlags(awsEnv)
	kiloEnv := map[string]string{}
	ApplyKiloAllowEnv(ctx, sshMgr, agentCmd, kiloEnv)
	extraEnvFlags += tmuxEnvFlags(kiloEnv)
	for _, secret := range config.EnvSecrets {
		extraEnvFlags += fmt.Sprintf(" -e %s=%s", secret.Key, secret.Value)
	}
	// AIMAN_* is injected last so session secrets cannot override the gate.
	extraEnvFlags += tmuxEnvFlags(aimanRuntimeEnv(session))
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
			"tmux new-session -d -s %q -c %q -e AIMAN_ID=%s%s %q; "+
			"_RC=$?; "+
			"tmux set-window-option -t %q remain-on-exit on 2>/dev/null || true; "+
			"tmux set-window-option -g remain-on-exit off 2>/dev/null || true; "+
			"tmux set-option -g destroy-unattached off 2>/dev/null || true; "+
			"exit $_RC",
		tmuxName, workingDir, strings.TrimSpace(session.ID), extraEnvFlags, PaneShellCommand(agentBootstrap), tmuxName,
	)
	// Attaching means adopting whatever is already on the remote, tmux included.
	// An interrupted create leaves a live worktree and tmux session behind with
	// no database row; the retry resolved the worktree but then collided here
	// with "duplicate session", so a recoverable state failed on every attempt.
	adopted := config.AttachExisting && tmuxSessionExists(ctx, sshMgr, tmuxName)
	if adopted {
		infraGit.ReportProgress(ctx, fmt.Sprintf("Attaching to existing tmux session %s...", tmuxName))
	}

	if !adopted {
		infraGit.ReportProgress(ctx, "Launching agent in tmux...")
		_, err = sshMgr.Execute(ctx, startCmd)
		if err != nil {
			return nil, fmt.Errorf("failed to start tmux session: %w", err)
		}

		// If the agent doesn't support an inline initial prompt (i.e. it would run
		// headlessly and exit), we send the prompt via tmux send-keys after a short
		// delay so the agent has time to start up interactively.
		//
		// Skipped when adopting: that agent is mid-conversation, and injecting a
		// fresh task prompt would interrupt whatever it is doing.
		acceptTrust := config.Agent != nil && IsAntigravityAgent(config.Agent.Name, config.Agent.Command)
		DeliverInitialPrompt(ctx, sshMgr, tmuxName, session.ID, sendKeysPrompt, acceptTrust)
	}

	session.TmuxSession = tmuxName

	return m.finaliseCreate(ctx, sshMgr, session, config, workingDir)
}

// finaliseCreate runs the best-effort decoration and bookkeeping shared by both
// backends after the terminal process is up: agent model detection, JIRA
// transition, and the ACTIVE transition. Workspace trust already ran before
// the agent launched (see trustWorkspaceBeforeLaunch).
func (m *FlowManager) finaliseCreate(ctx context.Context, sshMgr domain.RemoteExecutor, session *domain.Session, config domain.SessionConfig, workingDir string) (*domain.Session, error) {
	// Model detection boots an agent CLI on the remote (~20s for Claude) and
	// only fills a display field, so it is bounded and best-effort.
	infraGit.ReportProgress(ctx, "Detecting agent model…")
	m.finaliseSessionBestEffort(ctx, sshMgr, session, config, workingDir)

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
// tmuxSessionExists reports whether the remote already has a session by this
// name. `tmux has-session` exits non-zero when it does not, which Execute
// surfaces as an error rather than as a value, so the result is echoed instead.
func tmuxSessionExists(ctx context.Context, remote domain.RemoteExecutor, name string) bool {
	if strings.TrimSpace(name) == "" {
		return false
	}
	out, err := remote.Execute(ctx, fmt.Sprintf("tmux has-session -t %q 2>/dev/null && echo YES || echo NO", name))
	return err == nil && strings.Contains(out, "YES")
}

func (m *FlowManager) StartNewFlow(ctx context.Context, issueKey string, repoName string) (*domain.Session, error) {
	return m.CreateSession(ctx, domain.SessionConfig{
		IssueKey:   issueKey,
		Repo:       domain.Repo{Name: repoName},
		Agent:      &domain.Agent{Name: "Claude Code", Command: "claude"}, // Default
		PromptFree: true,
	})
}

// detectAgentModel probes the remote for the LLM model the agent will use.
// It is best-effort: returns an empty string if the model cannot be determined.
func detectAgentModel(ctx context.Context, remote domain.RemoteExecutor, agentName string) string {
	name := strings.ToLower(agentName)
	var cmd string
	switch {
	case strings.Contains(name, "claude"):
		// ANTHROPIC_MODEL env var takes precedence; claude config is a fallback.
		cmd = `printenv ANTHROPIC_MODEL 2>/dev/null || claude config get model 2>/dev/null || echo ""`
	case strings.Contains(name, "kilo"):
		cmd = `printenv KILO_MODEL 2>/dev/null || echo ""`
	case strings.Contains(name, "copilot"):
		cmd = `printenv GITHUB_COPILOT_MODEL 2>/dev/null || echo ""`
	case strings.Contains(name, "ageni"):
		// Ageni stores its provider selection in ~/.ageni/.env.
		cmd = `grep -s '^MASTER_PROVIDER=' ~/.ageni/.env 2>/dev/null | cut -d= -f2- || echo ""`
	case strings.Contains(name, "codex"):
		// Codex stores its model selection in ~/.codex/config.toml.
		cmd = `printenv CODEX_MODEL 2>/dev/null || grep -s '^model[[:space:]]*=' ~/.codex/config.toml 2>/dev/null | cut -d= -f2- | tr -d ' "' || echo ""`
	default:
		return ""
	}
	out, err := remote.Execute(ctx, cmd)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// localAWSProfileNames is indirected so tests do not depend on the machine's ~/.aws.
var localAWSProfileNames = awsdelegation.ListLocalAWSProfileNames

// SanitizeSessionAWSProfile returns the profile name that is safe to export as
// AWS_PROFILE for a session, given the profiles that exist locally.
//
// It drops two kinds of unusable names so the session falls back to the default
// credential chain instead of pointing AWS_PROFILE at something that cannot resolve:
// legacy session-scoped "aiman-<id>" profiles (removed in v0.8.11 but still present in
// old session records), and any profile missing from ~/.aws. When known is empty the
// local profiles could not be enumerated, so only the legacy rule applies.
func SanitizeSessionAWSProfile(profileName string, known []string) string {
	p := strings.TrimSpace(profileName)
	if p == "" || domain.IsLegacyScopedAWSProfile(p) {
		return ""
	}
	if len(known) == 0 {
		return p
	}
	if slices.Contains(known, p) {
		return p
	}
	return ""
}

func SharedSessionAWSEnv(profileName, region string) map[string]string {
	env := map[string]string{}
	if p := strings.TrimSpace(profileName); p != "" {
		known, err := localAWSProfileNames()
		if err != nil {
			known = nil
		}
		if p = SanitizeSessionAWSProfile(p, known); p != "" {
			env["AWS_PROFILE"] = p
		}
	}
	if region = strings.TrimSpace(region); region != "" {
		env["AWS_REGION"] = region
		env["AWS_DEFAULT_REGION"] = region
	}
	return env
}

// SessionRuntimeEnv is the AIMAN_* tmux environment for a session. Secrets
// must not override these keys.
// tmuxNameForCreate is the tmux session name for a new Aiman session.
// displayName is ignored: renaming the session must not imply renaming tmux
// or the worktree, so create never seeds tmux from the alias.
func tmuxNameForCreate(branch, _ string) string {
	return domain.SanitizeTmuxSessionName(branch)
}

func SessionRuntimeEnv(session *domain.Session) map[string]string {
	return aimanRuntimeEnv(session)
}

func aimanRuntimeEnv(session *domain.Session) map[string]string {
	env := map[string]string{
		"AIMAN_ENV":        "1",
		"AIMAN_ID":         strings.TrimSpace(session.ID),
		"AIMAN_SESSION_ID": strings.TrimSpace(session.ID),
	}
	if session.Name != "" {
		env["AIMAN_SESSION_NAME"] = session.Name
	}
	if session.Group != "" {
		env["AIMAN_GROUP"] = session.Group
	}
	if session.ParentID != "" {
		env["AIMAN_PARENT_ID"] = session.ParentID
	}
	// Deliberately no AIMAN_SOCKET_PATH or AIMAN_BIN_PATH.
	//
	// Both used to be filled in from config.GetDir() and os.Executable() — but
	// those resolve on whichever machine is *creating* the session, which for
	// the TUI is the laptop, while the session itself runs on the remote. A
	// session on regent0 was therefore told the agent API lived at
	// /Users/pieter/.aiman/aiman.sock, a path that does not exist there, so
	// every in-session `aiman session ...` call reported server_not_running
	// even with serve healthy on /home/code/.aiman/aiman.sock.
	//
	// Leaving them unset is correct on both machines: the in-session binary
	// falls back to its own config.GetDir() (see cmd/aiman socketPath), and the
	// hook reporter resolves the binary itself. Anything that genuinely needs
	// to override them can still set them in the session environment.
	return env
}

// TmuxEnvFlags formats tmux -e flags with a shell-quoted NAME=value so a
// space in a session name cannot split respawn-pane and drop the pane command.
func TmuxEnvFlags(env map[string]string) string {
	return tmuxEnvFlags(env)
}

func tmuxEnvFlags(env map[string]string) string {
	if len(env) == 0 {
		return ""
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, key := range keys {
		if value := strings.TrimSpace(env[key]); value != "" {
			// Quoted, not interpolated: an env value with a space or a shell
			// metacharacter would otherwise split into extra tmux arguments.
			b.WriteString(fmt.Sprintf(" -e %s", shellQuote(key+"="+value)))
		}
	}
	return b.String()
}

// workspaceTrustRe matches the first-run "do you trust this directory" dialog
// every agent puts up before it will read a project. Codex phrases it "Do you
// trust the contents of this directory?", agy "Do you trust this folder?".
const workspaceTrustRe = `[Dd]o you trust|[Tt]rust this folder|[Tt]rust the contents`

// acceptWorkspaceTrustPTY is the shell fragment that clears a workspace-trust
// dialog before anything is typed at the agent.
//
// The dialog is a select list, not a text field: characters it does not
// recognise are treated as menu input, and a prompt typed into it selects "No,
// quit" and kills the agent — exit 0, empty pane, no worktree touched. Enter
// takes the highlighted default, which is "Yes, continue" in every agent that
// asks.
//
// It polls rather than sleeping a fixed time because the dialog only appears
// once the agent has finished booting, and gives up silently: an agent that
// never asks must not have its prompt delayed by the full budget.
func acceptWorkspaceTrustPTY(sessionID string) string {
	return fmt.Sprintf(
		"attempt=0; "+
			"while [ $attempt -lt 10 ]; do "+
			"pane=$(aiman pty capture %[1]q --lines 0 2>/dev/null || true); "+
			"if printf '%%s' \"$pane\" | grep -qE %[2]q; then "+
			"aiman pty input %[1]q --key enter >/dev/null 2>&1; sleep 3; break; "+
			"fi; "+
			"attempt=$((attempt+1)); sleep 1; "+
			"done; ",
		strings.TrimSpace(sessionID), workspaceTrustRe,
	)
}

// DeliverInitialPromptPTY clears any workspace-trust dialog, then sends the
// initial prompt to a freshly-created PTY session: write the prompt remotely,
// type it through `aiman pty input`, then press Enter. Fire-and-forget like the
// tmux path.
//
// The trust step runs even when there is no prompt to deliver, because an
// unanswered dialog leaves the agent blocked on a menu instead of ready for
// work.
//
// It takes the same narrow promptDeliverer as the tmux path: writing a file and
// running a command is all delivery needs, and the wide executor made this
// untestable.
func DeliverInitialPromptPTY(ctx context.Context, remote promptDeliverer, sessionID, prompt string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	prompt = strings.TrimSpace(prompt)
	promptPath := ""
	if prompt != "" {
		promptPath = fmt.Sprintf("/tmp/aiman-prompt-%s", sessionID)
		if err := remote.WriteFile(ctx, promptPath, []byte(prompt)); err != nil {
			promptPath = "" // cannot deliver the prompt, but still clear the dialog
		}
	}
	script := `export PATH="$HOME/.local/bin:$PATH"; sleep 3; ` + acceptWorkspaceTrustPTY(sessionID)
	if promptPath != "" {
		// Return goes in a second call, via --enter. `--data "\r"` was typing the
		// two literal characters backslash and r into the agent's input box: POSIX
		// shell does not interpret that escape inside double quotes. Keeping it a
		// separate write after a pause also matters on its own — an agent TUI that
		// receives text and Return in one read treats the lot as a paste and inserts
		// a newline instead of submitting.
		script += fmt.Sprintf(
			`aiman pty input %q --file %q >/dev/null 2>&1 && sleep 1 && aiman pty input %q --key enter >/dev/null 2>&1; rm -f %q`,
			sessionID, promptPath, sessionID, promptPath,
		)
	}
	_, _ = remote.Execute(ctx, detachCommand(script))
}

// launchPTYSession hands the agent to aiman serve's built-in PTY runtime
// instead of tmux. Env travels in the create payload, so no -e flags or shell
// escaping are needed; the command runs under bash -l inside a real PTY.
func (m *FlowManager) launchPTYSession(ctx context.Context, sshMgr domain.RemoteExecutor, session *domain.Session, config domain.SessionConfig, workingDir, tmuxName, agentCmd string, sendKeysPrompt string, awsEnv map[string]string) (*domain.Session, error) {
	if !PTYRuntimeAvailable(ctx, sshMgr) {
		return nil, fmt.Errorf(
			"the PTY backend needs aiman serve running on the remote (TUI: m → Agent API \u2192 i then s), or set session_backend back to tmux")
	}
	infraGit.ReportProgress(ctx, "Launching agent in built-in PTY...")
	env := make(map[string]string, len(awsEnv)+4)
	for k, v := range awsEnv {
		env[k] = v
	}
	ApplyKiloAllowEnv(ctx, sshMgr, agentCmd, env)
	for _, secret := range config.EnvSecrets {
		env[secret.Key] = secret.Value
	}
	for k, v := range aimanRuntimeEnv(session) {
		env[k] = v
	}
	if config.AttachExisting && PTYSessionExists(ctx, sshMgr, session.ID) {
		// Adoption: an interrupted create left a live PTY session behind with no
		// database row. Adopt it as-is — injecting a fresh task prompt would
		// interrupt whatever the agent is mid-way through.
		infraGit.ReportProgress(ctx, "Adopting existing PTY session...")
	} else {
		if err := CreatePTYSession(ctx, sshMgr, PTYSpec{
			ID:      session.ID,
			Name:    tmuxName,
			Dir:     workingDir,
			Command: agentCmd,
			Env:     env,
		}); err != nil {
			return nil, fmt.Errorf("failed to start PTY session: %w", err)
		}
		DeliverInitialPromptPTY(ctx, sshMgr, session.ID, sendKeysPrompt)
	}
	session.Backend = domain.BackendPTY
	session.TmuxSession = tmuxName // display handle; kill/capture route via Backend

	return m.finaliseCreate(ctx, sshMgr, session, config, workingDir)
}

// shellQuote wraps s for POSIX sh so a value containing spaces or shell
// metacharacters survives as a single argument.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
