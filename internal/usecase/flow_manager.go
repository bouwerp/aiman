package usecase

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/coordinator"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
	"github.com/bouwerp/aiman/internal/infra/config"
	infraGit "github.com/bouwerp/aiman/internal/infra/git"
	"github.com/bouwerp/aiman/internal/infra/graphify"
	"github.com/bouwerp/aiman/internal/infra/remotefile"
	"github.com/bouwerp/aiman/internal/infra/skills"
	"github.com/google/uuid"
)

type FlowManager struct {
	jiraProvider domain.IssueProvider
	jiraConfig   *config.JiraConfig
	gitManager   domain.RepositoryManager
	sshManager   domain.RemoteExecutor
	slugger      domain.Slugger
	SkillEngine  domain.SkillEngine
	// repos holds the per-repository worktree bootstrap from config.
	repos map[string]config.RepoBootstrap
	// roles holds the per-role agent, model and effort overrides.
	roles config.RolesConfig
	// userChecklistDir is the laptop's checklist directory pushed to the
	// remote at create; empty where the process already runs on the remote.
	userChecklistDir string
	// doneItems adds the Done-when met to-do for sessions with conditions.
	doneItems DoneItems
	// noUpdateGuard launches agents with their own updaters left on.
	noUpdateGuard bool
	// sessions finds stored sessions, so a caller-chosen id cannot reuse one;
	// nil skips that check.
	sessions sessionGetter
}

// sessionGetter reads one stored session.
type sessionGetter interface {
	Get(ctx context.Context, id string) (*domain.Session, error)
}

// SetSessionStore sets the store a caller-chosen SessionID is checked
// against.
func (m *FlowManager) SetSessionStore(s sessionGetter) {
	m.sessions = s
}

// checkSessionID accepts an empty id (create picks one) or a UUID no stored
// session has.
func (m *FlowManager) checkSessionID(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	if parsed, err := uuid.Parse(id); err != nil || parsed.String() != id {
		return fmt.Errorf("session id %q is not a UUID", id)
	}
	if m.sessions != nil {
		if _, err := m.sessions.Get(ctx, id); err == nil {
			return fmt.Errorf("session id %s is already in use", id)
		}
	}
	return nil
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
		doneItems:    RemoteDoneItems{},
	}
}

// SetUserChecklistDir sets the local checklist directory pushed to the
// remote before each repository create. Leave it unset when the process runs
// on the remote, whose home already holds the user checklists.
func (m *FlowManager) SetUserChecklistDir(dir string) {
	m.userChecklistDir = dir
}

// SetDoneItems replaces how the Done-when met to-do is added. The default
// goes through the remote serve; serve itself passes its direct store path.
func (m *FlowManager) SetDoneItems(d DoneItems) {
	if d != nil {
		m.doneItems = d
	}
}

// SetUpdateGuards sets whether created sessions disable the agent's own
// updater (config.AgentUpdateGuards). The default is on.
func (m *FlowManager) SetUpdateGuards(on bool) {
	m.noUpdateGuard = !on
}

// SetRoles sets the per-role overrides.
func (m *FlowManager) SetRoles(r config.RolesConfig) {
	m.roles = r
}

// Roles returns the per-role overrides.
func (m *FlowManager) Roles() config.RolesConfig {
	return m.roles
}

// buildKit pushes the user's checklists, reports required tools the remote
// lacks, and builds the session's quality kit. Every failure is a progress
// line; none fails the create.
func (m *FlowManager) buildKit(ctx context.Context, sshMgr domain.RemoteExecutor, cfg domain.SessionConfig, worktree, agentCommand string) domain.QualityKit {
	kit := domain.QualityKit{Repo: cfg.Repo.Name, Role: cfg.Role}
	if !shouldBuildKit(cfg) {
		return kit
	}
	repoCfg, _ := config.LookupRepo(m.repos, cfg.Repo.Name)
	if m.userChecklistDir != "" {
		if _, err := PushUserChecklists(ctx, sshMgr, m.userChecklistDir); err != nil {
			infraGit.ReportProgress(ctx, fmt.Sprintf("Checklists not pushed: %v", err))
		}
	}
	if len(repoCfg.Requires) > 0 {
		infraGit.ReportProgress(ctx, "Checking required tools…")
		missing, err := CheckRequires(ctx, sshMgr, worktree, agentCommand, repoCfg.Requires)
		if err != nil {
			infraGit.ReportProgress(ctx, fmt.Sprintf("Required tools not checked: %v", err))
		}
		for _, e := range missing {
			infraGit.ReportProgress(ctx, "Missing on remote: "+e)
		}
	}
	infraGit.ReportProgress(ctx, "Building quality kit…")
	built, notes, err := BuildQualityKit(ctx, sshMgr, KitInput{
		Repo:     cfg.Repo.Name,
		Worktree: worktree,
		Role:     cfg.Role,
		Issue:    cfg.Issue,
		RepoCfg:  repoCfg,
	})
	for _, n := range notes {
		infraGit.ReportProgress(ctx, n)
	}
	if err != nil {
		infraGit.ReportProgress(ctx, fmt.Sprintf("Quality kit not built: %v", err))
		return kit
	}
	return built
}

// SetRepoBootstrap sets the per-repository bootstrap applied to worktrees
// CreateSession makes. Call it once while wiring, before any create.
func (m *FlowManager) SetRepoBootstrap(repos map[string]config.RepoBootstrap) {
	m.repos = repos
}

// shouldBootstrap reports whether this create makes a fresh worktree of a
// repository. Attached, reused, prepared, ad-hoc and no-repo directories
// already exist or are not checkouts, so they are left as they are.
func shouldBootstrap(config domain.SessionConfig) bool {
	if config.Repo.Name == "" || config.Repo.Name == "No Repository" {
		return false
	}
	return !config.AdHoc && !config.AttachExisting && !config.ReuseWorkspace && config.PreparedWorktree == ""
}

// bootstrapPromptNote tells the agent that setup is still running and where
// its log is.
func bootstrapPromptNote(setup, logPath string) string {
	return fmt.Sprintf("Worktree setup (`%s`) is running in the background; its log is %s. Check it has finished before building or running tests.", setup, logPath)
}

// applyBootstrap runs the configured bootstrap for a fresh worktree and
// returns the setup log path, empty when no setup started. Failures are
// reported as progress and never fail the create.
func (m *FlowManager) applyBootstrap(ctx context.Context, sshMgr domain.RemoteExecutor, cfg domain.SessionConfig, worktree string) (setup, logPath string) {
	if !shouldBootstrap(cfg) {
		return "", ""
	}
	b, ok := (&config.Config{Repos: m.repos}).RepoBootstrapFor(cfg.Repo.Name)
	if !ok {
		return "", ""
	}
	infraGit.ReportProgress(ctx, "Bootstrapping worktree…")
	mainRepo := infraGit.ComputeMainRepoPath(sshMgr.GetRoot(), cfg.Repo.Name)
	res, err := ApplyWorktreeBootstrap(ctx, sshMgr, mainRepo, worktree, b)
	if err != nil {
		infraGit.ReportProgress(ctx, fmt.Sprintf("Worktree bootstrap failed: %v", err))
		return "", ""
	}
	for _, p := range res.Skipped {
		infraGit.ReportProgress(ctx, fmt.Sprintf("Worktree bootstrap skipped %s (missing in the main checkout or already present)", p))
	}
	if res.SetupLog == "" {
		return "", ""
	}
	return b.Setup, res.SetupLog
}

// joinPrompt appends user-entered prompt text to a base agent prompt. The base is
// typically the JIRA task trigger (empty for ad-hoc sessions). A single space
// separates the two parts so the combined prompt is typed as one line
// (newlines are deliberately avoided: some agents submit on them).
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

// taskBriefMarker separates login-shell noise from the task file in
// WriteTaskBrief's read.
const taskBriefMarker = "__AIMAN_TASK__"

// WriteTaskBrief puts brief at the top of dir's task file, after the scaffold
// header and before any JIRA task already written there, between the brief
// markers (coordinator.BriefFromTaskFile reads it back). A file that already
// starts with the marked brief is left as it is.
func WriteTaskBrief(ctx context.Context, remote remotefile.Writer, dir, brief string) error {
	path := filepath.Join(dir, domain.AimanTaskFileName)
	out, err := remote.Execute(ctx, fmt.Sprintf("echo %s; cat -- %q 2>/dev/null || true", taskBriefMarker, path))
	if err != nil {
		return fmt.Errorf("read %s: %w", domain.AimanTaskFileName, err)
	}
	_, existing, ok := strings.Cut(out, taskBriefMarker+"\n")
	if !ok {
		return fmt.Errorf("read %s: marker missing from output", domain.AimanTaskFileName)
	}
	body := strings.TrimPrefix(existing, domain.AimanScaffoldHeader)
	marked := coordinator.BriefStartMarker + "\n" + strings.TrimRight(brief, "\n") + "\n" + coordinator.BriefEndMarker + "\n"
	if !strings.HasPrefix(body, marked) {
		body = joinTaskSections(marked, body)
	}
	_, err = remotefile.WriteIfChanged(ctx, remote, path, []byte(domain.AimanScaffoldHeader+body))
	return err
}

// designFirstPrompt appends skills.DesignFirstSuffix to a non-empty initial
// prompt. Hidden roles, briefed children, autonomous sessions and PR reviews
// skip it: a brief is the child's design, the autonomous prompt carries its
// own, and a review designs nothing.
func designFirstPrompt(prompt string, cfg domain.SessionConfig) string {
	if strings.TrimSpace(prompt) == "" || cfg.Role.Hidden() || cfg.Brief != "" || cfg.Mode == domain.SessionModeAutonomous || cfg.ReviewPR != "" {
		return prompt
	}
	return joinPrompt(prompt, skills.DesignFirstSuffix)
}

// editsGitignore reports whether create adds the aiman ignore rule to the
// worktree's .gitignore. A review worktree has it in info/exclude instead,
// so the PR's own .gitignore shows no change of aiman's.
func editsGitignore(cfg domain.SessionConfig) bool {
	return cfg.ReviewPR == ""
}

func joinTaskSections(first, rest string) string {
	if strings.TrimSpace(rest) == "" {
		return first
	}
	return strings.TrimRight(first, "\n") + "\n\n" + rest
}

// promptDeliverer is the narrow slice of RemoteExecutor that prompt delivery
// needs. Keeping it small makes the delivery path independently testable.
type promptDeliverer interface {
	WriteFile(ctx context.Context, path string, content []byte) error
	Execute(ctx context.Context, cmd string) (string, error)
}

// sessionBaseBranch is the base ref recorded on a session: the one the caller
// asked for, else the one the worktree setup detected.
func sessionBaseBranch(configured, detected string) string {
	if configured != "" {
		return configured
	}
	return detected
}

// resolveWorktree provisions (or locates) the directory a session runs in.
// Ad-hoc and no-repository sessions get a named subdirectory of the remote
// root; repository sessions get a git worktree.
func (m *FlowManager) resolveWorktree(ctx context.Context, sshMgr domain.RemoteExecutor, config domain.SessionConfig, branch string) (domain.Worktree, error) {
	switch {
	case config.PreparedWorktree != "":
		return domain.Worktree{Path: config.PreparedWorktree}, nil
	case config.AdHoc:
		// Ad-hoc sessions get their own subdirectory under the repos root so
		// they never run in the root itself.  Use the branch label (already
		// sanitized by the UI) as the directory name.
		adHocDir := branch
		if adHocDir == "" {
			adHocDir = domain.AdHocLabel("adhoc-", time.Now(), rand.Reader)
		}
		return domain.Worktree{Path: fmt.Sprintf("%s/%s", strings.TrimRight(sshMgr.GetRoot(), "/"), adHocDir)}, nil
	case config.Repo.Name != "No Repository" && config.Repo.Name != "":
		var worktree domain.Worktree
		var err error
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
			return domain.Worktree{}, fmt.Errorf("failed to setup worktree: %w", err)
		}
		// Safety: sessions must never run inside the main repository directory.
		if !config.ReuseWorkspace {
			mainRepo := infraGit.ComputeMainRepoPath(sshMgr.GetRoot(), config.Repo.Name)
			if filepath.Clean(worktree.Path) == mainRepo {
				return domain.Worktree{}, fmt.Errorf("safety: session working directory %q is the main repository — sessions must use a git worktree (a sibling directory), not the main repo itself", worktree.Path)
			}
		}
		return worktree, nil
	default:
		// No repository selected — create a named subdirectory under the root
		// so the session never runs directly in the root.
		subDir := branch
		if subDir == "" {
			subDir = domain.AdHocLabel("session-", time.Now(), rand.Reader)
		}
		return domain.Worktree{Path: fmt.Sprintf("%s/%s", strings.TrimRight(sshMgr.GetRoot(), "/"), subDir)}, nil
	}
}

func (m *FlowManager) CreateSession(ctx context.Context, config domain.SessionConfig) (*domain.Session, error) {
	if config.Agent == nil {
		return nil, errors.New("agent is required")
	}
	if strings.TrimSpace(config.ReviewPR) != "" && strings.TrimSpace(config.PreparedWorktree) == "" {
		return nil, errors.New("a review session must be created through the agent API")
	}
	if err := m.checkSessionID(ctx, config.SessionID); err != nil {
		return nil, err
	}

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
			branch = domain.AdHocLabel("adhoc-", time.Now(), rand.Reader)
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
	var agentName, launchModel, launchEffort string
	if config.Agent != nil {
		agentName = config.Agent.Name
		launchModel, launchEffort = config.Agent.LaunchModel, config.Agent.LaunchEffort
	}
	mode := config.Mode
	if mode == "" {
		mode = domain.SessionModeInteractive
	}

	id := config.SessionID
	if id == "" {
		id = uuid.New().String()
	}
	session := &domain.Session{
		ID:             id,
		Name:           config.Name,
		Group:          config.Group,
		ParentID:       config.ParentID,
		Role:           config.Role,
		IssueKey:       config.IssueKey,
		IssueType:      issueType(config.Issue),
		Branch:         branch,
		RepoName:       config.Repo.Name,
		AgentName:      agentName,
		AgentModel:     launchModel,
		LaunchModel:    launchModel,
		LaunchEffort:   launchEffort,
		Tier:           config.Tier,
		Status:         domain.SessionStatusProvisioning,
		Mode:           mode,
		TriggerSource:  config.TriggerSource,
		TriggerEventID: config.TriggerEventID,
		BudgetTokens:   config.BudgetTokens,
		ReviewPR:       config.ReviewPR,
		CreatedAt:      time.Now(),
	}

	// Step 6: Isolate (Worktree)
	wt, err := m.resolveWorktree(ctx, sshMgr, config, branch)
	if err != nil {
		return nil, err
	}
	session.WorktreePath = wt.Path
	if err := domain.CheckLaunchDir(wt.Path); err != nil {
		return nil, err
	}
	session.BaseBranch = sessionBaseBranch(config.BaseBranch, wt.Base)
	setup, setupLog := m.applyBootstrap(ctx, sshMgr, config, session.WorktreePath)

	if !config.AdHoc {
		// Ignore session-local task stub so it is not committed from the worktree.
		if editsGitignore(config) {
			_ = m.gitManager.EnsureAimanSessionFilesGitignored(ctx, sshMgr, session.WorktreePath)
		}

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
	if err := domain.CheckLaunchDir(workingDir); err != nil {
		return nil, err
	}

	// Ensure working directory exists (it might be a new folder defined by user)
	if _, err := sshMgr.Execute(ctx, fmt.Sprintf("mkdir -p %q", workingDir)); err != nil {
		return nil, fmt.Errorf("failed to create working directory: %w", err)
	}

	// Step 9 & 10: Skills & Agent
	agentCmd := config.Agent.Command
	var sendKeysPrompt string
	// kit stays empty unless the skill engine writes it into the task file,
	// so the Done-when met to-do exists only alongside its conditions.
	var kit domain.QualityKit
	if m.SkillEngine != nil {
		built := m.buildKit(ctx, sshMgr, config, session.WorktreePath, config.Agent.Command)
		prepared, err := m.SkillEngine.PrepareSession(ctx, sshMgr, workingDir, *config.Agent, config.Skills, config.PromptFree, config.Issue, config.PriorSnapshot, built)
		if err == nil {
			agentCmd = prepared.Command
			sendKeysPrompt = prepared.InitialPrompt
			kit = built
		}
	}
	if config.Brief != "" {
		if err := WriteTaskBrief(ctx, sshMgr, workingDir, config.Brief); err != nil {
			return nil, fmt.Errorf("failed to write the brief: %w", err)
		}
		if !strings.Contains(sendKeysPrompt, domain.AimanTaskFileName) {
			sendKeysPrompt = joinPrompt(sendKeysPrompt, "Read your brief in "+domain.AimanTaskFileName+" first.")
		}
	}
	sendKeysPrompt = InjectSharedContext(ctx, sshMgr, workingDir, session.Group, session.RepoName, sendKeysPrompt)
	// Append any free-text prompt entered in the summary dialog. For JIRA sessions
	// this follows the "Read .aiman_task.md…" trigger; for ad-hoc sessions it becomes
	// the entire prompt.
	sendKeysPrompt = joinPrompt(sendKeysPrompt, config.InitialPrompt)
	if setupLog != "" {
		sendKeysPrompt = joinPrompt(sendKeysPrompt, bootstrapPromptNote(setup, setupLog))
	}
	sendKeysPrompt = designFirstPrompt(sendKeysPrompt, config)
	WriteMuseSessionID(ctx, sshMgr, workingDir, agentCmd, session.ID)

	// Step 8: Terminal. The display Name is a renameable alias and must not
	// become the terminal name; the name stays on the branch.
	terminalName := terminalNameForCreate(branch, session.Name)

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

	// Every session runs in the PTY runtime.
	return m.launchPTYSession(ctx, sshMgr, session, config, kit, workingDir, terminalName, agentCmd, sendKeysPrompt, awsEnv)
}

// finaliseCreate runs the best-effort decoration and bookkeeping after the
// terminal process is up: agent model detection, JIRA
// transition, and the ACTIVE transition. Workspace trust already ran before
// the agent launched (see trustWorkspaceBeforeLaunch).
func (m *FlowManager) finaliseCreate(ctx context.Context, sshMgr domain.RemoteExecutor, session *domain.Session, config domain.SessionConfig, kit domain.QualityKit, workingDir string) (*domain.Session, error) {
	// The graph build runs detached once the agent is up, so it never holds
	// the create; the script is a no-op without graphify or a git checkout.
	if session.WorktreePath != "" {
		if _, err := sshMgr.Execute(ctx, graphify.BackgroundUpdateScript(session.WorktreePath)); err != nil {
			infraGit.ReportProgress(ctx, fmt.Sprintf("graphify update not started: %v", err))
		}
	}

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

	// The task file names the Done-when met to-do, so it is added only when
	// the kit carries conditions.
	if len(kit.DoneWhen) > 0 && m.doneItems != nil {
		if err := m.doneItems.Ensure(ctx, sshMgr, *session); err != nil {
			infraGit.ReportProgress(ctx, fmt.Sprintf("Done-when to-do not added: %v", err))
		}
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

// terminalNameForCreate is the terminal name for a new Aiman session.
// displayName is ignored: renaming the session must not imply renaming the
// terminal or the worktree, so create never seeds the name from the alias.
func terminalNameForCreate(branch, _ string) string {
	return domain.TerminalNameFromBranch(branch)
}

// SessionRuntimeEnv is the AIMAN_* environment for a session. Secrets must not
// override these keys.
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

// workspaceTrustRe matches the first-run "do you trust this directory" dialog
// every agent puts up before it will read a project. Codex phrases it "Do you
// trust the contents of this directory?", agy "Do you trust this folder?", Pi
// "Trust project folder?".
const workspaceTrustRe = `[Dd]o you trust|[Tt]rust this folder|[Tt]rust the contents|[Tt]rust project folder`

// composerReadyRe matches a screen where the agent is past boot and ready for
// input: a prompt glyph, a Codex or Grok placeholder, Claude's shortcut hint,
// or Pi's footer context gauge ("0.0%/1.0M"), which Pi draws only once its
// composer is up.
const composerReadyRe = `[›❯]|Ask Codex|Ask Grok|\? for shortcuts|[0-9]%/[0-9.]+[kKM]`

// bootWaitBudget is how many polls the PTY boot wait makes before it gives up.
const bootWaitBudget = 20

// acceptWorkspaceTrustPTY is the shell fragment that clears startup dialogs
// before anything is typed at the agent: first an update dialog, then a
// workspace-trust dialog.
//
// Both are select lists, not text fields: characters they do not recognise
// are treated as menu input. A prompt typed into the trust dialog selects "No,
// quit" and kills the agent (exit 0, empty pane, no worktree touched); Enter
// takes the highlighted default, which is "Yes, continue" in every agent that
// asks. An update dialog's default is "Update now" or "Install", so it gets
// its skip key instead (bootUpdateDialogBranch) and the loop keeps polling.
// Update dialogs are checked first because Codex's menu rows start with "›",
// which composerReadyRe would read as a ready composer.
//
// It polls rather than sleeping a fixed time because the dialogs only appear
// once the agent has finished booting, and gives up silently: an agent that
// never asks must not have its prompt delayed by the full budget.
func acceptWorkspaceTrustPTY(sessionID string) string {
	// A ready composer breaks the loop early so an already-trusted worktree
	// does not burn the full poll budget before the initial prompt is typed.
	// The final echo is the only visibility into this loop's outcome: an
	// AIMAN_BOOTWAIT:<reason>:<attempts> line that DeliverInitialPromptPTY logs,
	// so a boot slow enough to exhaust the budget (reason=timeout) leaves
	// evidence instead of a silent, unexplained stuck composer. reason=update
	// means an update dialog was skipped on the way.
	id := strings.TrimSpace(sessionID)
	return fmt.Sprintf(
		"attempt=0; reason=timeout; upd=0; "+
			"while [ $attempt -lt %[4]d ]; do "+
			"%[5]s"+
			"pane=$(aiman pty capture %[1]q --lines 0 2>/dev/null || true); "+
			"if printf '%%s' \"$pane\" | grep -qE %[2]q; then "+
			"aiman pty input %[1]q --key enter >/dev/null 2>&1; sleep 2; reason=trust; break; "+
			"fi; "+
			"if printf '%%s' \"$pane\" | grep -qE %[3]q; then reason=ready; break; fi; "+
			"attempt=$((attempt+1)); sleep 1; "+
			"done; "+
			"if [ $upd = 1 ]; then reason=update; fi; "+
			"echo \"AIMAN_BOOTWAIT:$reason:$attempt\"; ",
		id, workspaceTrustRe, composerReadyRe, bootWaitBudget, bootUpdateDialogBranch(id),
	)
}

// logBootWait reports acceptWorkspaceTrustPTY's outcome so a boot slow enough
// to exhaust the poll budget (reason=timeout) is visible in serve.log instead
// of only showing up later as an unexplained stuck composer.
func logBootWait(sessionID, out string) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "AIMAN_BOOTWAIT:")
		if !ok {
			continue
		}
		reason, attempts, _ := strings.Cut(rest, ":")
		log.Printf("prompt-deliver session=%s boot-wait reason=%s attempts=%s", sessionID, reason, attempts)
		return
	}
}

// DeliverInitialPromptPTY clears any workspace-trust dialog, then sends the
// initial prompt to a freshly-created PTY session and confirms it left the
// composer. Child-session create (Codex/Grok spawned by a parent agent) used
// to detach a one-shot Enter; agents that are still booting drop that Return
// and leave the task typed but never submitted.
//
// The trust step runs even when there is no prompt to deliver, because an
// unanswered dialog leaves the agent blocked on a menu instead of ready for
// work. Delivery is synchronous so session.create does not return before the
// prompt has actually been submitted.
func DeliverInitialPromptPTY(ctx context.Context, remote promptDeliverer, sessionID, prompt string) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	prompt = strings.TrimSpace(prompt)

	// Trust clear is synchronous and bounded: detaching it raced the agent boot
	// so the prompt was typed into a still-drawing TUI.
	bootOut, _ := remote.Execute(ctx, remoteAimanPreamble+acceptWorkspaceTrustPTY(sessionID))
	logBootWait(sessionID, bootOut)
	if prompt == "" {
		return
	}

	promptPath := fmt.Sprintf("/tmp/aiman-prompt-%s", sessionID)
	if err := remote.WriteFile(ctx, promptPath, []byte(prompt)); err != nil {
		return
	}
	defer func() {
		_, _ = remote.Execute(ctx, fmt.Sprintf("rm -f %q", promptPath))
	}()

	te, ok := remote.(TerminalExecutor)
	if !ok {
		// Tests that only stub promptDeliverer still get a single typed Enter.
		_, _ = remote.Execute(ctx, remoteAimanPreamble+fmt.Sprintf(
			"aiman pty input %[1]q --file %[2]q && sleep 1 && aiman pty input %[1]q --key enter",
			sessionID, promptPath))
		return
	}
	_ = SendPTYFileConfirmed(ctx, te, sessionID, promptPath, prompt)
}

// launchPTYSession hands the agent to aiman serve's built-in PTY runtime. Env
// travels in the create payload, so no shell escaping is needed; the command
// runs under bash -l inside a real PTY.
func (m *FlowManager) launchPTYSession(ctx context.Context, sshMgr domain.RemoteExecutor, session *domain.Session, config domain.SessionConfig, kit domain.QualityKit, workingDir, terminalName, agentCmd string, sendKeysPrompt string, awsEnv map[string]string) (*domain.Session, error) {
	if !PTYRuntimeAvailable(ctx, sshMgr) {
		return nil, fmt.Errorf(
			"sessions need aiman serve running on the remote (TUI: m \u2192 Agent API \u2192 i then s)")
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
			ID:            session.ID,
			Name:          terminalName,
			Dir:           workingDir,
			Command:       agentCmd,
			Env:           env,
			NoUpdateGuard: m.noUpdateGuard,
			Role:          config.Role,
			ParentID:      config.ParentID,
		}); err != nil {
			return nil, fmt.Errorf("failed to start PTY session: %w", err)
		}
		DeliverInitialPromptPTY(ctx, sshMgr, session.ID, sendKeysPrompt)
	}
	session.Backend = domain.BackendPTY
	session.TerminalName = terminalName // display name; the PTY is addressed by session.ID

	return m.finaliseCreate(ctx, sshMgr, session, config, kit, workingDir)
}

// issueType is the JIRA type of the issue a session starts from, if any.
func issueType(issue *domain.Issue) string {
	if issue == nil {
		return ""
	}
	return issue.Type
}

// shellQuote wraps s for POSIX sh so a value containing spaces or shell
// metacharacters survives as a single argument.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
