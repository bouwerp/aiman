package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/git"
	"github.com/bouwerp/aiman/internal/infra/jira"
	"github.com/bouwerp/aiman/internal/infra/mutagen"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/usecase"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"
)

var (
	docStyle     = lipgloss.NewStyle().Margin(1, 2)
	statusStyle  = lipgloss.NewStyle().PaddingLeft(2).Italic(true).Foreground(lipgloss.Color("#7D7D7D"))
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#00FF00"))
	failStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF0000"))
	activeStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Bold(true)
	promptRegex  = regexp.MustCompile(`^[\w\-@~/:\.]+\s*(\$|#|>)\s*$`)
)

type viewState int

const (
	viewStateMain viewState = iota
	viewStateMenu
	viewStateRemotes
	viewStateSetup
	viewStateGitSetup
	viewStateGeneralSettings
	viewStatePicker
	viewStateVSCodeError
	viewStateIssuePicker
	viewStateBranchInput
	viewStateRepoPicker
	viewStateDirPicker
	viewStateAgentPicker
	viewStateSummary
	viewStateLoading
	viewStateTerminateConfirm
	viewStateTerminateProgress
	viewStateWorktreeExists
	viewStateRestartAgentPicker
	viewStateRestartConfirm
)

type panelMode int

const (
	panelModePreview panelMode = iota
	panelModeTerminal
)

type menuItem struct {
	title, desc string
	action      viewState
}

func (i menuItem) Title() string       { return i.title }
func (i menuItem) Description() string { return i.desc }
func (i menuItem) FilterValue() string { return i.title }

type item struct {
	session    domain.Session
	needsInput bool
	activity   string
}

func (i item) Title() string {
	prefix := ""
	switch {
	case i.needsInput:
		prefix = "! "
	case i.activity == "idle":
		prefix = "o "
	case i.activity == "busy":
		prefix = "> "
	}
	activity := ""
	switch {
	case i.needsInput:
		activity = " ⚠ input"
	case i.activity == "idle":
		activity = " • idle"
	case i.activity == "busy":
		activity = " • busy"
	}
	if i.session.IssueKey != "" {
		return fmt.Sprintf("%s%s (%s)%s", prefix, i.session.IssueKey, i.session.TmuxSession, activity)
	}
	return fmt.Sprintf("%s%s%s", prefix, i.session.TmuxSession, activity)
}

func (i item) Description() string {
	if i.needsInput {
		return fmt.Sprintf("Repo: %s | Host: %s | State: input", i.session.RepoName, i.session.RemoteHost)
	}
	if i.activity != "" {
		return fmt.Sprintf("Repo: %s | Host: %s | State: %s", i.session.RepoName, i.session.RemoteHost, i.activity)
	}
	return fmt.Sprintf("Repo: %s | Host: %s", i.session.RepoName, i.session.RemoteHost)
}

func (i item) FilterValue() string {
	return i.session.IssueKey + " " + i.session.TmuxSession + " " + i.session.RepoName
}

type Model struct {
	cfg                    *config.Config
	db                     domain.SessionRepository
	state                  viewState
	panelMode              panelMode
	list                   list.Model
	menu                   list.Model
	remotes                RemotesModel
	setup                  SetupModel
	gitSetup               GitSetupModel
	generalSetup           GeneralSetupModel
	picker                 RepoPickerModel
	issuePicker            IssuePickerModel
	branchInput            BranchInputModel
	dirPicker              DirPickerModel
	agentPicker            AgentPickerModel
	summary                SummaryModel
	doctorResults          []usecase.CheckResult
	width, height          int
	viewport               viewport.Model
	terminal               *TerminalModel
	tmuxOutput             string
	activeSession          string
	termCloser             io.Closer
	lastError              string
	loadingMsg             string
	sessionCfg             SessionConfig
	loadingNext            viewState
	initialLoad            bool
	terminateSession       domain.Session
	terminateSteps         []string
	terminateErrors        []string
	terminateIndex         int
	terminatePrecheckError string
	consoleOpen            bool
	consoleLog             []string
	consoleViewport        viewport.Model
	gitStatus              domain.GitStatus
	lastGitStatusUpdate    time.Time
	restartingSession      *domain.Session
}

func NewModel(cfg *config.Config, doctorResults []usecase.CheckResult, initialSessions []domain.Session, db domain.SessionRepository) *Model {
	items := make([]list.Item, len(initialSessions))
	for i, s := range initialSessions {
		items[i] = item{session: s, needsInput: false, activity: ""}
	}

	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Aiman Dashboard - Active Sessions"
	l.AdditionalFullHelpKeys = func() []key.Binding {
		return []key.Binding{
			key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new session")),
			key.NewBinding(key.WithKeys("ctrl+r"), key.WithHelp("ctrl+r", "restart session")),
			key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "admin menu")),
			key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "attach full terminal")),
			key.NewBinding(key.WithKeys("ctrl+t"), key.WithHelp("ctrl+t", "toggle preview/terminal")),
			key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "open vscode")),
			key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("ctrl+l", "copy local path")),
			key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh status")),
			key.NewBinding(key.WithKeys("ctrl+y"), key.WithHelp("ctrl+y", "recreate mutagen sync")),
			key.NewBinding(key.WithKeys("ctrl+k"), key.WithHelp("ctrl+k", "terminate session")),
			key.NewBinding(key.WithKeys("`"), key.WithHelp("`", "toggle debug console")),
		}
	}

	menuItems := []list.Item{
		menuItem{title: "Manage Remote Servers", desc: "Select active, add new, or scan suggestions", action: viewStateRemotes},
		menuItem{title: "JIRA Configuration", desc: "Update URL, Email, and Token", action: viewStateSetup},
		menuItem{title: "Git Configuration", desc: "Configure repositories and organizations", action: viewStateGitSetup},
		menuItem{title: "General Settings", desc: "Experimental and general features", action: viewStateGeneralSettings},
	}
	m := list.New(menuItems, list.NewDefaultDelegate(), 0, 0)
	m.Title = "Administrative Menu"

	vp := viewport.New(0, 0)
	vp.Style = lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false). // Top border
		PaddingTop(1)

	return &Model{
		cfg:           cfg,
		db:            db,
		state:         viewStateMain,
		panelMode:     panelModePreview,
		list:          l,
		menu:          m,
		remotes:       NewRemotesModel(cfg),
		setup:         NewSetupModel(cfg),
		gitSetup:      NewGitSetupModel(cfg),
		generalSetup:  NewGeneralSetupModel(cfg),
		doctorResults: doctorResults,
		viewport:      vp,
	}
}

type tmuxTickMsg time.Time
type tmuxOutputMsg struct {
	session string
	output  string
	err     error
}

type inputHintMsg struct {
	session    string
	needsInput bool
	activity   string
}

type tmuxTerminalMsg struct {
	stream io.ReadWriteCloser
	err    error
}

type gitStatusMsg struct {
	session string
	status  domain.GitStatus
	err     error
}

type gitTickMsg time.Time

func (m *Model) validateTerminationPreconditions(s domain.Session) error {
	if s.WorktreePath == "" {
		return nil
	}

	remote, ok := resolveRemote(m.cfg, s)
	if !ok {
		return fmt.Errorf("no remote configured")
	}
	mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
	ctx := context.Background()

	statusOut, err := mgr.Execute(ctx, fmt.Sprintf("bash -c 'git -C %q status --porcelain --untracked-files=no'", s.WorktreePath))
	if err != nil {
		// If git is broken/corrupted (exit 128), allow cleanup to proceed
		// Check if error indicates corrupted repo
		errMsg := err.Error()
		if strings.Contains(errMsg, "exit status 128") ||
			strings.Contains(errMsg, "not a git repository") ||
			strings.Contains(errMsg, "fatal:") {
			// Repository is corrupted, allow cleanup
			return nil
		}
		return fmt.Errorf("failed to verify git status before termination: %w", err)
	}
	if strings.TrimSpace(statusOut) != "" {
		return fmt.Errorf("session has uncommitted changes; commit or stash before terminating")
	}

	aheadOut, err := mgr.Execute(ctx, fmt.Sprintf("bash -c 'if git -C %q rev-parse --abbrev-ref --symbolic-full-name @{upstream} >/dev/null 2>&1; then git -C %q rev-list --count @{upstream}..HEAD; else echo NO_UPSTREAM; fi'", s.WorktreePath, s.WorktreePath))
	if err != nil {
		// If git is broken/corrupted, allow cleanup to proceed
		errMsg := err.Error()
		if strings.Contains(errMsg, "exit status 128") ||
			strings.Contains(errMsg, "not a git repository") ||
			strings.Contains(errMsg, "fatal:") {
			return nil
		}
		return fmt.Errorf("failed to verify upstream commits before termination: %w", err)
	}
	ahead := strings.TrimSpace(aheadOut)
	if ahead == "NO_UPSTREAM" {
		return fmt.Errorf("branch has no upstream; push and set upstream before terminating")
	}
	aheadCount, convErr := strconv.Atoi(ahead)
	if convErr != nil {
		return fmt.Errorf("failed to parse upstream check output: %s", ahead)
	}
	if aheadCount > 0 {
		return fmt.Errorf("session has %d unpushed commit(s); push before terminating", aheadCount)
	}

	return nil
}

func tickTmux() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg {
		return tmuxTickMsg(t)
	})
}

func tickGit() tea.Cmd {
	return tea.Tick(30*time.Second, func(t time.Time) tea.Msg {
		return gitTickMsg(t)
	})
}

func fetchGitStatus(cfg *config.Config, s domain.Session) tea.Cmd {
	return func() tea.Msg {
		if s.WorktreePath == "" {
			return gitStatusMsg{session: s.TmuxSession, err: fmt.Errorf("no worktree path")}
		}

		remote, ok := resolveRemote(cfg, s)
		if !ok {
			return gitStatusMsg{session: s.TmuxSession, err: fmt.Errorf("no remote found")}
		}

		ctx := context.Background()
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		if err := mgr.Connect(ctx); err != nil {
			return gitStatusMsg{session: s.TmuxSession, err: err}
		}

		gitMgr := git.NewManager(&cfg.Git)
		status, err := gitMgr.GetGitStatus(ctx, mgr, s.WorktreePath)
		return gitStatusMsg{
			session: s.TmuxSession,
			status:  status,
			err:     err,
		}
	}
}

func fetchTmuxPane(cfg *config.Config, session domain.Session) tea.Cmd {
	return func() tea.Msg {
		remote, ok := resolveRemote(cfg, session)
		if !ok {
			return tmuxOutputMsg{session: session.TmuxSession, err: fmt.Errorf("no remote configured")}
		}

		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		// We use a background context as this is just a quick capture
		out, err := mgr.CaptureTmuxPane(context.Background(), session.TmuxSession)
		return tmuxOutputMsg{
			session: session.TmuxSession,
			output:  out,
			err:     err,
		}
	}
}

func checkInputHint(cfg *config.Config, session domain.Session) tea.Cmd {
	return func() tea.Msg {
		if !cfg.Features.InputPromptDetection {
			return inputHintMsg{session: session.TmuxSession, needsInput: false, activity: ""}
		}
		remote, ok := resolveRemote(cfg, session)
		if !ok {
			return inputHintMsg{session: session.TmuxSession, needsInput: false, activity: ""}
		}
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		out, err := mgr.CaptureTmuxPane(context.Background(), session.TmuxSession)
		if err != nil {
			return inputHintMsg{session: session.TmuxSession, needsInput: false, activity: ""}
		}
		activity, needs := detectSessionActivity(out)
		return inputHintMsg{session: session.TmuxSession, needsInput: needs, activity: activity}
	}
}

func detectSessionActivity(output string) (string, bool) {
	text := strings.ToLower(output)

	// Input prompt patterns
	inputPatterns := []string{
		"press any key",
		"press enter",
		"password:",
		"passphrase",
		"enter to continue",
		"allow execution",
		"action required",
		"allow once",
		"allow for this session",
		"no, suggest changes",
		"[y/n]",
		"(y/n)",
		"[y/n]",
		"(y/n)",
		"[y/n]",
		"(y/n)",
		"confirm",
		"are you sure",
	}

	for _, p := range inputPatterns {
		if strings.Contains(text, p) {
			return "input", true
		}
	}

	// Busy patterns (Claude/Gemini style status lines)
	busyPatterns := []string{
		"thinking",
		"tokens",
		"marinating",
		"processing",
		"generating",
	}
	for _, p := range busyPatterns {
		if strings.Contains(text, p) {
			return "busy", false
		}
	}

	// Idle prompt patterns (shell prompt characters)
	if promptRegex.MatchString(lastNonEmptyLine(output)) {
		return "idle", false
	}

	// Unknown/undetermined
	return "", false
}

func lastNonEmptyLine(output string) string {
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" {
			return line
		}
	}
	return ""
}

func resolveRemote(cfg *config.Config, session domain.Session) (config.Remote, bool) {
	if cfg == nil {
		return config.Remote{}, false
	}

	// Prefer active remote if set
	if cfg.ActiveRemote != "" {
		for _, r := range cfg.Remotes {
			if r.Host == cfg.ActiveRemote {
				return r, true
			}
		}
	}

	// Fallback to session's remote host
	if session.RemoteHost != "" {
		for _, r := range cfg.Remotes {
			if r.Host == session.RemoteHost {
				return r, true
			}
		}
	}

	return config.Remote{}, false
}

func (m *Model) initTerminal(session domain.Session) tea.Cmd {
	return func() tea.Msg {
		if m.termCloser != nil {
			m.termCloser.Close()
			m.termCloser = nil
		}

		remote, ok := resolveRemote(m.cfg, session)
		if !ok {
			return tmuxTerminalMsg{err: fmt.Errorf("no remote configured")}
		}

		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		stream, err := mgr.StreamTmuxSession(context.Background(), session.TmuxSession)
		if err != nil {
			return tmuxTerminalMsg{err: err}
		}

		return tmuxTerminalMsg{stream: stream}
	}
}

func (m *Model) searchJira(query string) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		jiraProvider := jira.NewProvider(jira.Config{
			URL:      m.cfg.Integrations.Jira.URL,
			Email:    m.cfg.Integrations.Jira.Email,
			APIToken: m.cfg.Integrations.Jira.APIToken,
		})

		issues, err := jiraProvider.SearchIssues(ctx, query)
		return jiraIssuesMsg{issues: issues, err: err}
	}
}

type jiraIssuesMsg struct {
	issues []domain.Issue
	err    error
}

func (m *Model) fetchRepos() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		gitManager := git.NewManager(&m.cfg.Git)
		repos, err := gitManager.ListRepos(ctx)
		return reposMsg{repos: repos, err: err}
	}
}

type reposMsg struct {
	repos []domain.Repo
	err   error
}

type dirsMsg struct {
	dirs []string
	err  error
}

type sessionCreateMsg struct {
	session domain.Session
	err     error
}

type attachMsg struct {
	cmd *exec.Cmd
}

type attachDoneMsg struct{}

type terminateStepMsg struct {
	index int
	err   error
}

type recreateMutagenMsg struct {
	session domain.Session
	err     error
}

func (m *Model) recreateMutagenSync(s domain.Session) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		remote, ok := resolveRemote(m.cfg, s)
		if !ok {
			return recreateMutagenMsg{err: fmt.Errorf("no remote configured")}
		}
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})

		remoteSyncDir := s.WorktreePath
		if s.TmuxSession != "" {
			if cwd, err := mgr.GetTmuxSessionCWD(ctx, s.TmuxSession); err == nil && strings.TrimSpace(cwd) != "" {
				remoteSyncDir = strings.TrimSpace(cwd)
			}
		}
		if remoteSyncDir == "" {
			return recreateMutagenMsg{err: fmt.Errorf("session has no remote working directory")}
		}

		target := remote.Host
		if remote.User != "" {
			target = fmt.Sprintf("%s@%s", remote.User, remote.Host)
		}

		syncName := s.TmuxSession
		if syncName == "" {
			syncName = filepath.Base(s.WorktreePath)
		}
		home, _ := os.UserHomeDir()
		localPath := fmt.Sprintf("%s/%s/work/%s", home, config.DirName, syncName)

		terminateCandidates := []string{
			s.MutagenSyncID,
			s.TmuxSession,
			filepath.Base(s.LocalPath),
			syncName,
		}
		terminated := map[string]bool{}
		for _, candidate := range terminateCandidates {
			if candidate == "" || terminated[candidate] {
				continue
			}
			terminated[candidate] = true
			_, _ = exec.CommandContext(ctx, "mutagen", "sync", "terminate", candidate).CombinedOutput()
		}

		mutagenEngine := mutagen.NewEngine()
		remoteSyncPath := fmt.Sprintf("%s:%s", target, remoteSyncDir)
		if err := mutagenEngine.StartSync(ctx, localPath, remoteSyncPath); err != nil {
			return recreateMutagenMsg{err: fmt.Errorf("failed to recreate mutagen sync: %w", err)}
		}

		s.LocalPath = localPath
		s.WorktreePath = remoteSyncDir
		s.MutagenSyncID = syncName
		return recreateMutagenMsg{session: s}
	}
}

func (m *Model) fetchRepoDirectories(repo *domain.Repo) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		// Find remote config
		var remote config.Remote
		for _, r := range m.cfg.Remotes {
			if r.Host == m.cfg.ActiveRemote {
				remote = r
				break
			}
		}

		if remote.Host == "" {
			return dirsMsg{err: fmt.Errorf("no active remote configured")}
		}

		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})

		var repoPath string
		if repo != nil && repo.URL != "" {
			// Extract just the repo name (not org/repo)
			repoName := extractRepoName(repo.Name)
			repoPath = fmt.Sprintf("%s/%s", remote.Root, repoName)

			// Check if repo exists on remote, clone if not
			if err := mgr.ValidateDir(ctx, repoPath); err != nil {
				// Repo doesn't exist, need to clone it
				_, cloneErr := mgr.Execute(ctx, fmt.Sprintf("cd %s && git clone %s %s", remote.Root, repo.URL, repoName))
				if cloneErr != nil {
					return dirsMsg{err: fmt.Errorf("failed to clone repository: %w", cloneErr)}
				}
			}
		} else {
			// No repository, scan from remote root
			repoPath = remote.Root
		}

		// Scan directories up to depth 3
		dirs, err := mgr.ScanDirectories(ctx, repoPath, 3)
		if err != nil {
			return dirsMsg{err: err}
		}

		// Always add root as an option
		dirs = append([]string{"."}, dirs...)

		return dirsMsg{dirs: dirs, err: nil}
	}
}

func (m *Model) fetchAgents() tea.Cmd {
	return func() tea.Msg {
		// Find remote config
		var remote config.Remote
		for _, r := range m.cfg.Remotes {
			if r.Host == m.cfg.ActiveRemote {
				remote = r
				break
			}
		}

		if remote.Host == "" {
			return agent.ScanAgentsMsg{Err: fmt.Errorf("no active remote configured")}
		}

		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		scanner := agent.NewScanner(mgr)
		return agent.ScanCmd(scanner)()
	}
}

func (m *Model) createSession() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		// Find remote config
		var remote config.Remote
		for _, r := range m.cfg.Remotes {
			if r.Host == m.cfg.ActiveRemote {
				remote = r
				break
			}
		}

		if remote.Host == "" {
			return sessionCreateMsg{err: fmt.Errorf("no active remote configured")}
		}

		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})

		var resolvedWorktreePath string
		var tmuxName string
		branch := m.sessionCfg.Branch
		worktreeDir := strings.ReplaceAll(branch, "/", "-")
		tmuxName = worktreeDir

		if m.sessionCfg.Repo.URL != "" {
			repoName := extractRepoName(m.sessionCfg.Repo.Name)
			repoPath := fmt.Sprintf("%s/%s", remote.Root, repoName)

			// Create repo if it's new
			if m.sessionCfg.Repo.IsNew {
				m.log("Creating new repository: %s", m.sessionCfg.Repo.Name)
				// gh repo create <name> --public --add-readme (or similar)
				// We'll create it and push an initial commit if needed
				createCmd := fmt.Sprintf("gh repo create %s --public", m.sessionCfg.Repo.Name)
				_, err := mgr.Execute(ctx, createCmd)
				if err != nil {
					return sessionCreateMsg{err: fmt.Errorf("failed to create repository: %w", err)}
				}

				// Get the URL of the new repo
				urlCmd := fmt.Sprintf("gh repo view %s --json url -q .url", m.sessionCfg.Repo.Name)
				url, err := mgr.Execute(ctx, urlCmd)
				if err != nil {
					return sessionCreateMsg{err: fmt.Errorf("failed to get repository URL: %w", err)}
				}
				m.sessionCfg.Repo.URL = strings.TrimSpace(url)

				// Initialize the repo locally on remote if it doesn't exist as a directory yet
				if err := mgr.ValidateDir(ctx, repoPath); err != nil {
					// Use environment variables for the initial commit to avoid "Author identity unknown" errors
					// We use the email from JIRA config as a reasonable default
					authorName := "Aiman Bot"
					authorEmail := m.cfg.Integrations.Jira.Email
					if authorEmail == "" {
						authorEmail = "aiman@example.com"
					}

					initCmd := fmt.Sprintf("mkdir -p %s && cd %s && git init && git remote add origin %s && touch README.md && git add README.md && GIT_AUTHOR_NAME=%q GIT_AUTHOR_EMAIL=%q GIT_COMMITTER_NAME=%q GIT_COMMITTER_EMAIL=%q git commit -m 'Initial commit' && git branch -M main && git push -u origin main",
						repoPath, repoPath, m.sessionCfg.Repo.URL, authorName, authorEmail, authorName, authorEmail)
					_, err := mgr.Execute(ctx, initCmd)
					if err != nil {
						return sessionCreateMsg{err: fmt.Errorf("failed to initialize repository: %w", err)}
					}
				}
			}

			// Ensure repo exists
			if err := mgr.ValidateDir(ctx, repoPath); err != nil {
				_, cloneErr := mgr.Execute(ctx, fmt.Sprintf("cd %s && git clone %s %s", remote.Root, m.sessionCfg.Repo.URL, repoName))
				if cloneErr != nil {
					return sessionCreateMsg{err: fmt.Errorf("failed to clone repository: %w", cloneErr)}
				}
			}

			// Fetch latest
			_, fetchErr := mgr.Execute(ctx, fmt.Sprintf("git -C %s fetch origin", repoPath))
			if fetchErr != nil {
				return sessionCreateMsg{err: fmt.Errorf("failed to fetch repo: %w", fetchErr)}
			}

			worktreePath := fmt.Sprintf("%s/../%s", repoPath, worktreeDir)

			// Check if worktree already exists
			checkCmd := fmt.Sprintf("bash -c 'if [ -d %q ]; then echo EXISTS; fi'", worktreePath)
			checkOut, _ := mgr.Execute(ctx, checkCmd)
			if strings.Contains(checkOut, "EXISTS") {
				return sessionCreateMsg{err: fmt.Errorf("WORKTREE_EXISTS")}
			}

			// Determine base branch
			var baseBranch string
			for _, b := range []string{"origin/main", "origin/master", "main", "master"} {
				if _, err := mgr.Execute(ctx, fmt.Sprintf("git -C %s rev-parse --verify %s", repoPath, b)); err == nil {
					baseBranch = b
					break
				}
			}

			if baseBranch == "" {
				// No branches found, this is likely an empty repository
				m.log("No base branch found, initializing main branch")
				authorName := "Aiman Bot"
				authorEmail := m.cfg.Integrations.Jira.Email
				if authorEmail == "" {
					authorEmail = "aiman@example.com"
				}

				// Initialize with an initial commit if repo is truly empty
				initCmd := fmt.Sprintf("cd %s && git checkout -b main && touch .gitignore && git add .gitignore && GIT_AUTHOR_NAME=%q GIT_AUTHOR_EMAIL=%q GIT_COMMITTER_NAME=%q GIT_COMMITTER_EMAIL=%q git commit -m 'Initial commit' && git push -u origin main",
					repoPath, authorName, authorEmail, authorName, authorEmail)
				if _, err := mgr.Execute(ctx, initCmd); err == nil {
					baseBranch = "main"
				} else {
					// If that also fails, we're in trouble, but let's try one last thing
					baseBranch = "master"
				}
			}

			// Create worktree from determined base
			worktreeCmd := fmt.Sprintf("git -C %s worktree add -B %s ../%s %s", repoPath, branch, worktreeDir, baseBranch)
			_, worktreeErr := mgr.Execute(ctx, worktreeCmd)
			if worktreeErr != nil {
				return sessionCreateMsg{err: fmt.Errorf("failed to create worktree: %w", worktreeErr)}
			}

			// Resolve worktree path on remote to avoid ../ in paths
			resolvedWorktreePath = worktreePath
			if out, err := mgr.Execute(ctx, fmt.Sprintf("realpath %q", worktreePath)); err == nil {
				resolvedWorktreePath = strings.TrimSpace(out)
			} else if out, err := mgr.Execute(ctx, fmt.Sprintf("readlink -f %q", worktreePath)); err == nil {
				resolvedWorktreePath = strings.TrimSpace(out)
			}
		} else {
			// Ad-hoc session without a repository
			resolvedWorktreePath = remote.Root
		}

		// Working directory
		workingDir := resolvedWorktreePath
		if m.sessionCfg.Directory != "" && m.sessionCfg.Directory != "." {
			if m.sessionCfg.Repo.URL != "" {
				workingDir = fmt.Sprintf("%s/%s", resolvedWorktreePath, m.sessionCfg.Directory)
			} else {
				// For no-repo sessions, Directory is relative to remote root
				workingDir = fmt.Sprintf("%s/%s", remote.Root, m.sessionCfg.Directory)
			}
		}

		// Ensure working directory exists (it might be a new folder defined by user)
		_, mkdirErr := mgr.Execute(ctx, fmt.Sprintf("mkdir -p %q", workingDir))
		if mkdirErr != nil {
			return sessionCreateMsg{err: fmt.Errorf("failed to create working directory: %w", mkdirErr)}
		}

		// Start tmux session and agent
		agentCmd := m.sessionCfg.Agent.Command
		startCmd := fmt.Sprintf("tmux new-session -d -s %q -c %q %q", tmuxName, workingDir, agentCmd)
		_, tmuxErr := mgr.Execute(ctx, startCmd)
		if tmuxErr != nil {
			return sessionCreateMsg{err: fmt.Errorf("failed to start tmux session: %w", tmuxErr)}
		}

		// Start mutagen sync
		mutagenEngine := mutagen.NewEngine()
		home, _ := os.UserHomeDir()
		localSyncPath := fmt.Sprintf("%s/%s/work/%s", home, config.DirName, tmuxName)
		target := remote.Host
		if remote.User != "" {
			target = fmt.Sprintf("%s@%s", remote.User, remote.Host)
		}
		remoteSyncPath := fmt.Sprintf("%s:%s", target, workingDir)
		if err := mutagenEngine.StartSync(ctx, localSyncPath, remoteSyncPath); err != nil {
			return sessionCreateMsg{err: fmt.Errorf("failed to start mutagen sync: %w", err)}
		}

		session := domain.Session{
			ID:            uuid.New().String(),
			IssueKey:      m.sessionCfg.IssueKey,
			Branch:        branch,
			RepoName:      m.sessionCfg.Repo.Name,
			RemoteHost:    remote.Host,
			WorktreePath:  resolvedWorktreePath,
			TmuxSession:   tmuxName,
			MutagenSyncID: tmuxName,
			LocalPath:     localSyncPath,
			AgentName:     m.sessionCfg.Agent.Name,
			Status:        domain.SessionStatusSyncing,
			CreatedAt:     time.Now(),
			UpdatedAt:     time.Now(),
		}

		// Persist session
		if m.db != nil {
			_ = m.db.Save(ctx, &session)
		}

		return sessionCreateMsg{session: session}
	}
}

func (m *Model) runTerminateStepCmd(index int) tea.Cmd {
	return func() tea.Msg {
		return terminateStepMsg{index: index, err: m.runTerminateStep(index)}
	}
}

func (m *Model) runTerminateStep(index int) error {
	ctx := context.Background()
	s := m.terminateSession

	switch index {
	case 0: // Stop mutagen sync
		name := s.MutagenSyncID
		if name == "" {
			name = s.TmuxSession
		}
		if name == "" {
			return nil
		}
		cmd := exec.CommandContext(ctx, "mutagen", "sync", "terminate", name) // #nosec G204
		if out, err := cmd.CombinedOutput(); err != nil {
			// Try fallback using tmux session name if different
			if s.TmuxSession != "" && s.TmuxSession != name {
				fallback := exec.CommandContext(ctx, "mutagen", "sync", "terminate", s.TmuxSession) // #nosec G204
				if _, fbErr := fallback.CombinedOutput(); fbErr == nil {
					return nil
				}
			}
			return fmt.Errorf("mutagen terminate failed: %w, output: %s", err, string(out))
		}
		return nil
	case 1: // Kill tmux session
		if s.TmuxSession == "" {
			return nil
		}
		remote, ok := resolveRemote(m.cfg, s)
		if !ok {
			return fmt.Errorf("no remote configured")
		}
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		_, err := mgr.Execute(ctx, fmt.Sprintf("tmux kill-session -t %q", s.TmuxSession))
		return err
	case 2: // Stop agent process (tmux kill already handles this)
		return nil
	case 3: // Remove git worktree
		if s.WorktreePath == "" {
			return nil
		}
		remote, ok := resolveRemote(m.cfg, s)
		if !ok {
			return fmt.Errorf("no remote configured")
		}
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})

		m.log("Terminating session: removing worktree %s", s.WorktreePath)

		// Try to remove via git worktree (needs to run from main repo)
		if s.RepoName != "" {
			repoName := extractRepoName(s.RepoName)
			mainRepoPath := fmt.Sprintf("%s/%s", remote.Root, repoName)
			out, err := mgr.Execute(ctx, fmt.Sprintf("bash -c 'git -C %q worktree remove --force %q'", mainRepoPath, s.WorktreePath))
			if err != nil {
				m.log("Warning: git worktree remove failed: %v, output: %s", err, out)
			}
		}

		// Force remove the directory regardless (worktree remove might fail if corrupted)
		out, err := mgr.Execute(ctx, fmt.Sprintf("rm -rf %q", s.WorktreePath))
		if err != nil {
			m.log("Error: rm -rf worktree failed: %v, output: %s", err, out)
		}
		return err
	case 4: // Clean up local files
		if s.LocalPath == "" {
			return nil
		}
		return os.RemoveAll(s.LocalPath)
	case 5: // Delete session from database
		if s.ID == "" {
			return nil
		}
		if m.db != nil {
			return m.db.Delete(ctx, s.ID)
		}
		return nil
	default:
		return nil
	}
}

func extractRepoName(fullName string) string {
	// Extract just the repo name from "org/repo" format
	parts := strings.Split(fullName, "/")
	if len(parts) > 1 {
		return parts[len(parts)-1]
	}
	return fullName
}

func (m *Model) Init() tea.Cmd {
	m.initialLoad = true
	return tea.Batch(
		tickTmux(),
		tickGit(),
	)
}

func (m *Model) log(format string, args ...interface{}) {
	msg := fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
	m.consoleLog = append(m.consoleLog, msg)
	// Keep only last 100 messages
	if len(m.consoleLog) > 100 {
		m.consoleLog = m.consoleLog[len(m.consoleLog)-100:]
	}
}

func (m *Model) renderWithConsole(baseView string) string {
	consoleHeight := m.height / 3
	if consoleHeight < 5 {
		consoleHeight = 5
	}
	if consoleHeight > 20 {
		consoleHeight = 20
	}

	// Update viewport content with latest logs
	m.consoleViewport.SetContent(strings.Join(m.consoleLog, "\n"))

	// Build console content
	consoleStyle := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(lipgloss.Color("240")).
		Padding(0, 1).
		Width(m.width - 4)

	var consoleContent strings.Builder
	consoleContent.WriteString(activeStyle.Render("Debug Console") + " (` to close, ↑↓ to scroll)\n\n")
	consoleContent.WriteString(m.consoleViewport.View())

	consoleBox := consoleStyle.Render(consoleContent.String())

	// Split baseView into lines
	baseLines := strings.Split(baseView, "\n")

	// Truncate base view to make room for console
	maxBaseLines := m.height - consoleHeight - 2
	if len(baseLines) > maxBaseLines {
		baseLines = baseLines[:maxBaseLines]
	}

	return strings.Join(baseLines, "\n") + "\n" + consoleBox
}

func (m *Model) SetSize(width, height int) {
	m.width = width
	m.height = height
	h, v := docStyle.GetFrameSize()

	mainHeight := height - v - len(m.doctorResults) - 10

	m.list.SetSize(width/3-h, mainHeight) // Sidebar width
	m.menu.SetSize(width-h, height-v)
	m.remotes.list.SetSize(width-h-4, height-v-14)
	m.remotes.width = width
	m.remotes.height = height

	// Viewport takes up the bottom part of the main panel
	m.viewport.Width = width - (width / 3) - h - 4
	m.viewport.Height = mainHeight - 15 // Reserve more lines for split details panel

	if m.issuePicker.list.Title != "" {
		m.issuePicker.SetSize(width, height)
	}

	if m.agentPicker.list.Title != "" {
		m.agentPicker.SetSize(width, height)
	}

	m.summary.SetSize(width, height)
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	// Global window size handling
	if msg, ok := msg.(tea.WindowSizeMsg); ok {
		m.SetSize(msg.Width, msg.Height)
		if m.terminal != nil {
			m.terminal.w = m.viewport.Width
			m.terminal.h = m.viewport.Height
			m.terminal.term.Resize(m.viewport.Width, m.viewport.Height)
		}

		// Propagate to sub-models
		var subCmd tea.Cmd
		var tm tea.Model

		tm, subCmd = m.remotes.Update(msg)
		m.remotes = tm.(RemotesModel)
		cmds = append(cmds, subCmd)

		tm, subCmd = m.setup.Update(msg)
		m.setup = tm.(SetupModel)
		cmds = append(cmds, subCmd)

		m.gitSetup.SetSize(msg.Width, msg.Height)
	}

	switch m.state {
	case viewStateMain:
		return m.handleMainUpdate(msg)

	case viewStateMenu:
		return m.handleMenuUpdate(msg)

	case viewStateRemotes:
		return m.handleRemotesUpdate(msg)

	case viewStateSetup:
		return m.handleSetupUpdate(msg)

	case viewStateGitSetup:
		return m.handleGitSetupUpdate(msg)

	case viewStateGeneralSettings:
		return m.handleGeneralSetupUpdate(msg)

	case viewStateVSCodeError:
		if _, ok := msg.(tea.KeyMsg); ok {
			m.state = viewStateMain
		}
		return m, nil

	case viewStateIssuePicker:
		return m.handleIssuePickerUpdate(msg)

	case viewStateBranchInput:
		return m.handleBranchInputUpdate(msg)

	case viewStateRepoPicker:
		return m.handleRepoPickerUpdate(msg)

	case viewStateDirPicker:
		return m.handleDirPickerUpdate(msg)

	case viewStateAgentPicker:
		return m.handleAgentPickerUpdate(msg)

	case viewStateRestartAgentPicker:
		return m.handleRestartAgentPickerUpdate(msg)

	case viewStateRestartConfirm:
		return m.handleRestartConfirmUpdate(msg)

	case viewStateSummary:
		return m.handleSummaryUpdate(msg)

	case viewStateTerminateConfirm:
		return m.handleTerminateConfirmUpdate(msg)

	case viewStateWorktreeExists:
		return m.handleWorktreeExistsUpdate(msg)

	case viewStateTerminateProgress:
		return m.handleTerminateProgressUpdate(msg)

	case viewStateLoading:
		return m.handleLoadingUpdate(msg)
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) View() string {
	baseView := m.renderView()

	// Overlay console if open
	if m.consoleOpen {
		return m.renderWithConsole(baseView)
	}

	return baseView
}

func (m *Model) renderView() string {
	switch m.state {
	case viewStateMain:
		return m.renderMainView()

	case viewStateMenu:
		return docStyle.Render(m.menu.View())

	case viewStateRemotes:
		return docStyle.Render(m.remotes.View())

	case viewStateSetup:
		return docStyle.Render(m.setup.View())

	case viewStateGitSetup:
		return docStyle.Render(m.gitSetup.View())

	case viewStateGeneralSettings:
		return m.generalSetup.View()

	case viewStateVSCodeError:
		var b strings.Builder
		b.WriteString(activeStyle.Render("VS Code CLI Error") + "\n\n")
		b.WriteString(m.lastError + "\n\n")
		b.WriteString("To fix this on macOS:\n")
		b.WriteString("1. Open VS Code.\n")
		b.WriteString("2. Press Cmd+Shift+P.\n")
		b.WriteString("3. Type 'shell command' and select:\n")
		b.WriteString("   'Shell Command: Install \"code\" command in PATH'.\n\n")
		b.WriteString("Press any key to return.")

		dialog := lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(lipgloss.Color("205")).
			Padding(1, 2).
			Width(60)

		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(b.String()))

	case viewStateIssuePicker:
		return docStyle.Render(m.issuePicker.View())

	case viewStateBranchInput:
		return m.branchInput.View()

	case viewStateRepoPicker:
		return docStyle.Render(m.picker.View())

	case viewStateDirPicker:
		return docStyle.Render(m.dirPicker.View())

	case viewStateAgentPicker:
		return docStyle.Render(m.agentPicker.View())

	case viewStateRestartAgentPicker:
		return docStyle.Render(m.agentPicker.View())

	case viewStateRestartConfirm:
		var b strings.Builder
		b.WriteString(activeStyle.Render("Confirm Session Restart") + "\n\n")
		b.WriteString(fmt.Sprintf("Session %q is currently active.\n", m.restartingSession.TmuxSession))
		b.WriteString("Restarting will kill the existing tmux session and agent.\n\n")
		b.WriteString("Do you want to proceed?\n\n")
		b.WriteString(activeStyle.Render("[y]") + " Yes  " + activeStyle.Render("[n]") + " No")

		dialog := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(1, 2).
			Width(60)

		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(b.String()))

	case viewStateSummary:
		return m.summary.View()

	case viewStateTerminateConfirm:
		if sel := m.list.SelectedItem(); sel != nil {
			s := sel.(item).session
			var b strings.Builder
			b.WriteString(failStyle.Render("Terminate session?") + "\n\n")
			b.WriteString(fmt.Sprintf("Session: %s\n", s.TmuxSession))
			b.WriteString(fmt.Sprintf("Host: %s\n", s.RemoteHost))
			b.WriteString(fmt.Sprintf("Repo: %s\n", s.RepoName))
			b.WriteString(fmt.Sprintf("Branch: %s\n\n", s.Branch))
			if m.terminatePrecheckError != "" {
				b.WriteString(failStyle.Render("Blocked: "+m.terminatePrecheckError) + "\n\n")
			}
			b.WriteString("This will:\n")
			b.WriteString("  - Stop mutagen sync\n")
			b.WriteString("  - Kill tmux session\n")
			b.WriteString("  - Remove git worktree\n")
			b.WriteString("  - Clean up local files\n\n")
			b.WriteString(activeStyle.Render("[y]") + " Confirm  " + activeStyle.Render("[n]") + " Cancel")

			dialog := lipgloss.NewStyle().
				Border(lipgloss.DoubleBorder()).
				BorderForeground(lipgloss.Color("196")).
				Padding(1, 2).
				Width(60)

			return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(b.String()))
		}
		return ""

	case viewStateTerminateProgress:
		var b strings.Builder
		b.WriteString(activeStyle.Render("Terminating session...") + "\n\n")
		for i, step := range m.terminateSteps {
			status := "[ ]"
			if i < m.terminateIndex {
				status = successStyle.Render("[✓]")
			} else if i == m.terminateIndex {
				status = activeStyle.Render("[→]")
			}
			b.WriteString(fmt.Sprintf("%s %s\n", status, step))
			if m.terminateErrors != nil && i < len(m.terminateErrors) && m.terminateErrors[i] != "" {
				b.WriteString("    " + failStyle.Render(m.terminateErrors[i]) + "\n")
			}
		}

		dialog := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(1, 2).
			Width(60)

		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(b.String()))

	case viewStateWorktreeExists:
		var b strings.Builder
		b.WriteString(failStyle.Render("Worktree Already Exists") + "\n\n")
		b.WriteString(fmt.Sprintf("A git worktree already exists for branch:\n%s\n\n", m.sessionCfg.Branch))
		b.WriteString("This usually means there's an existing session.\n\n")
		b.WriteString(activeStyle.Render("[b]") + " Change Branch Name\n")
		b.WriteString(activeStyle.Render("[c]") + " Cancel")

		dialog := lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(lipgloss.Color("196")).
			Padding(1, 2).
			Width(60)

		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(b.String()))

	case viewStateLoading:
		msg := m.loadingMsg
		if msg == "" {
			msg = "Loading..."
		}
		style := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(1, 2).
			Width(50)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, style.Render(msg))
	}
	return ""
}

func (m *Model) handleMainUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case attachMsg:
		return m, tea.ExecProcess(msg.cmd, func(err error) tea.Msg {
			return tmuxTickMsg(time.Now())
		})
	case tmuxTerminalMsg:
		if msg.err != nil {
			m.tmuxOutput = failStyle.Render("Failed to stream session: " + msg.err.Error())
			m.panelMode = panelModePreview
			m.state = viewStateMain
			return m, nil
		}
		m.termCloser = msg.stream
		term := NewTerminalModel(msg.stream, m.viewport.Width, m.viewport.Height)
		m.terminal = &term
		return m, nil
	case tmuxTickMsg:
		cmds = append(cmds, tickTmux())

		// On first tick, force-select the first session if nothing is active
		if m.initialLoad {
			m.initialLoad = false
			if len(m.list.Items()) > 0 {
				m.list.Select(0)
				if sel := m.list.SelectedItem(); sel != nil {
					s := sel.(item).session
					m.activeSession = s.TmuxSession
					m.tmuxOutput = "Loading..."
					m.viewport.SetContent(m.tmuxOutput)
					cmds = append(cmds,
						fetchTmuxPane(m.cfg, s),
						checkInputHint(m.cfg, s),
						fetchGitStatus(m.cfg, s),
					)
				}
			}
		} else if sel := m.list.SelectedItem(); sel != nil {
			s := sel.(item).session
			if m.activeSession != s.TmuxSession {
				m.activeSession = s.TmuxSession
			}
			cmds = append(cmds,
				fetchTmuxPane(m.cfg, s),
				checkInputHint(m.cfg, s),
				fetchGitStatus(m.cfg, s),
			)
		}
	case gitTickMsg:
		cmds = append(cmds, tickGit())
		if sel := m.list.SelectedItem(); sel != nil {
			s := sel.(item).session
			cmds = append(cmds, fetchGitStatus(m.cfg, s))
		}
	case gitStatusMsg:
		if msg.session == m.activeSession {
			if msg.err == nil {
				m.gitStatus = msg.status
				m.lastGitStatusUpdate = time.Now()
			} else {
				m.log("Git status error for %s: %v", msg.session, msg.err)
			}
		}
	case tmuxOutputMsg:
		if msg.session == m.activeSession {
			if msg.err != nil {
				m.tmuxOutput = failStyle.Render("Failed to capture pane: " + msg.err.Error())
			} else {
				m.tmuxOutput = msg.output
			}

			// Sticky scroll: only go to bottom if we were already at the bottom.
			// For first load, AtBottom() is true by default (YOffset 0).
			wasAtBottom := m.viewport.AtBottom()

			m.viewport.SetContent(m.tmuxOutput)

			if wasAtBottom {
				m.viewport.GotoBottom()
			}
		}
	case inputHintMsg:
		// Update list items with input hint when enabled
		if m.cfg.Features.InputPromptDetection {
			items := m.list.Items()
			for idx, it := range items {
				if sessItem, ok := it.(item); ok {
					if sessItem.session.TmuxSession == msg.session {
						sessItem.needsInput = msg.needsInput
						sessItem.activity = msg.activity
						items[idx] = sessItem
						break
					}
				}
			}
			m.list.SetItems(items)
		}
	case tea.KeyMsg:
		m, cmd, handled := m.handleMainKeyMsg(msg)
		if handled {
			return m, cmd
		}
	}

	// Capture list selection changes to trigger immediate fetch
	oldSel := m.list.SelectedItem()
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	cmds = append(cmds, cmd)
	newSel := m.list.SelectedItem()

	if oldSel != newSel && newSel != nil {
		s := newSel.(item).session
		m.activeSession = s.TmuxSession
		m.gitStatus = domain.GitStatus{} // Clear old status
		m.tmuxOutput = "Loading..."
		m.viewport.SetContent(m.tmuxOutput)
		if m.panelMode == panelModeTerminal {
			cmds = append(cmds, m.initTerminal(s), fetchGitStatus(m.cfg, s))
		} else {
			cmds = append(cmds, fetchTmuxPane(m.cfg, s), fetchGitStatus(m.cfg, s))
		}
	}

	if m.panelMode == panelModeTerminal && m.terminal != nil {
		var tModel tea.Model
		tModel, cmd = m.terminal.Update(msg)
		if tm, ok := tModel.(TerminalModel); ok {
			m.terminal = &tm
		}
		cmds = append(cmds, cmd)
	} else {
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) handleMainKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	var cmds []tea.Cmd

	if msg.String() == "`" {
		m.consoleOpen = !m.consoleOpen
		m.log("Console toggled: %v", m.consoleOpen)
		if m.consoleOpen {
			// Initialize console viewport
			consoleHeight := m.height / 3
			if consoleHeight < 5 {
				consoleHeight = 5
			}
			if consoleHeight > 20 {
				consoleHeight = 20
			}
			m.consoleViewport = viewport.New(m.width-6, consoleHeight-4)
			m.consoleViewport.SetContent(strings.Join(m.consoleLog, "\n"))
			m.consoleViewport.GotoBottom()
		}
		return m, nil, true
	}
	// Handle console scrolling when open
	if m.consoleOpen {
		switch msg.String() {
		case "up", "k":
			m.consoleViewport.ScrollUp(1)
			return m, nil, true
		case "down", "j":
			m.consoleViewport.ScrollDown(1)
			return m, nil, true
		case "pgup":
			m.consoleViewport.PageUp()
			return m, nil, true
		case "pgdown":
			m.consoleViewport.PageDown()
			return m, nil, true
		}
	}
	if msg.String() == "n" {
		m.state = viewStateIssuePicker
		m.issuePicker = NewIssuePickerModel(nil)
		m.issuePicker.loading = true
		m.issuePicker.SetSize(m.width, m.height)
		return m, m.searchJira(""), true
	}
	if msg.String() == "m" {
		m.state = viewStateMenu
		return m, nil, true
	}
	if msg.String() == "ctrl+c" {
		if m.termCloser != nil {
			m.termCloser.Close()
		}
		return m, tea.Quit, true
	}
	if msg.String() == "ctrl+t" {
		if m.panelMode == panelModePreview {
			if sel := m.list.SelectedItem(); sel != nil {
				m.loadingMsg = fmt.Sprintf("Connecting to session %s...", sel.(item).session.TmuxSession)
				m.loadingNext = viewStateMain
				m.state = viewStateLoading
				return m, tea.Sequence(
					tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg {
						return m.initTerminal(sel.(item).session)()
					}),
				), true
			}
		} else {
			m.panelMode = panelModePreview
			if m.termCloser != nil {
				m.termCloser.Close()
				m.termCloser = nil
			}
		}
		return m, tea.Batch(cmds...), true
	}
	if msg.String() == "ctrl+s" {
		if sel := m.list.SelectedItem(); sel != nil {
			s := sel.(item).session
			var remote config.Remote
			for _, r := range m.cfg.Remotes {
				if r.Host == m.cfg.ActiveRemote {
					remote = r
					break
				}
			}
			mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
			c := mgr.AttachTmuxSession(s.TmuxSession)
			m.loadingMsg = fmt.Sprintf("Connecting to session %s...", s.TmuxSession)
			m.loadingNext = viewStateMain
			m.state = viewStateLoading
			return m, tea.Sequence(
				tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg {
					return attachMsg{cmd: c}
				}),
			), true
		}
	}
	if msg.String() == "v" {
		if sel := m.list.SelectedItem(); sel != nil {
			s := sel.(item).session
			if s.LocalPath != "" {
				_, err := exec.LookPath("code")
				if err != nil {
					m.lastError = "The VS Code CLI 'code' was not found in your PATH."
					m.state = viewStateVSCodeError
					return m, nil, true
				}
				err = exec.Command("code", s.LocalPath).Start() // #nosec G204
				if err != nil {
					m.lastError = fmt.Sprintf("Failed to start VS Code: %v", err)
					m.state = viewStateVSCodeError
					return m, nil, true
				}
			}
		}
	}
	if msg.String() == "ctrl+l" {
		m.log("ctrl+l pressed")
		if sel := m.list.SelectedItem(); sel != nil {
			s := sel.(item).session
			m.log("Selected session: %s, LocalPath: %s", s.TmuxSession, s.LocalPath)
			if s.LocalPath == "" {
				m.log("ERROR: No local path")
				m.lastError = "No local sync path available for this session"
				m.state = viewStateVSCodeError
				return m, nil, true
			}

			// Detect current terminal app and open a new window there
			termProgram := os.Getenv("TERM_PROGRAM")
			m.log("Terminal program: %s", termProgram)
			var script string

			switch termProgram {
			case "iTerm.app":
				// iTerm2
				script = fmt.Sprintf(`tell application "iTerm"
	create window with default profile
	tell current session of current window
		write text "cd %q && clear"
	end tell
end tell`, s.LocalPath)
			case "WarpTerminal":
				// Warp - just copy the local path to clipboard
				script = fmt.Sprintf(`do shell script "printf '%s' | pbcopy"
display notification "Local path copied to clipboard" with title "Aiman"`, strings.ReplaceAll(s.LocalPath, "'", "'\\''"))
			case "Apple_Terminal":
				// Terminal.app
				script = fmt.Sprintf(`tell application "Terminal"
	do script "cd %q && clear"
	activate
end tell`, s.LocalPath)
			default:
				// Fallback: try Terminal.app
				script = fmt.Sprintf(`tell application "Terminal"
	do script "cd %q && clear"
	activate
end tell`, s.LocalPath)
			}

			m.log("Executing AppleScript for %s...", termProgram)
			m.log("Script: %s", script)
			cmd := exec.Command("osascript", "-e", script)
			output, err := cmd.CombinedOutput()
			if err != nil {
				m.log("ERROR: osascript failed: %v", err)
				m.log("Output: %s", string(output))
				m.lastError = fmt.Sprintf("Failed to open terminal: %v\nOutput: %s", err, string(output))
				m.state = viewStateVSCodeError
				return m, nil, true
			}
			if len(output) > 0 {
				m.log("osascript output: %s", string(output))
			}
			m.log("Terminal command completed successfully")
		} else {
			m.log("ERROR: No session selected")
		}
		return m, nil, true
	}
	if msg.String() == "ctrl+r" {
		if sel := m.list.SelectedItem(); sel != nil {
			s := sel.(item).session
			m.restartingSession = &s
			m.sessionCfg = SessionConfig{
				IssueKey:  s.IssueKey,
				Branch:    s.Branch,
				Repo:      domain.Repo{Name: s.RepoName, URL: ""},
				Directory: "",
			}

			// If session is active or syncing, ask for confirmation
			if s.Status == domain.SessionStatusActive || s.Status == domain.SessionStatusSyncing {
				m.state = viewStateRestartConfirm
				return m, nil, true
			}

			m.loadingMsg = "Scanning available agents..."
			m.loadingNext = viewStateRestartAgentPicker
			m.state = viewStateLoading
			return m, m.fetchAgents(), true
		}
	}
	if msg.String() == "r" {
		// Refresh sessions from remote
		m.log("Refreshing sessions...")
		if m.cfg.ActiveRemote != "" {
			var remote config.Remote
			for _, r := range m.cfg.Remotes {
				if r.Host == m.cfg.ActiveRemote {
					remote = r
					break
				}
			}
			// Trigger session discovery
			m.loadingMsg = "Refreshing sessions..."
			m.loadingNext = viewStateMain
			m.state = viewStateLoading
			return m, func() tea.Msg {
				ctx := context.Background()
				mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
				if err := mgr.Connect(ctx); err != nil {
					return discoveryResultMsg{}
				}
				discoverer := usecase.NewSessionDiscoverer(mgr, mutagen.NewEngine())
				sessions, _ := discoverer.Discover(ctx, remote.Host)
				return discoveryResultMsg(sessions)
			}, true
		}
		return m, nil, true
	}
	if msg.String() == "ctrl+y" {
		if sel := m.list.SelectedItem(); sel != nil {
			m.loadingMsg = "Recreating mutagen sync..."
			m.loadingNext = viewStateMain
			m.state = viewStateLoading
			return m, m.recreateMutagenSync(sel.(item).session), true
		}
	}
	if msg.String() == "ctrl+k" {
		if sel := m.list.SelectedItem(); sel != nil {
			m.terminatePrecheckError = ""
			m.state = viewStateTerminateConfirm
			return m, nil, true
		}
	}

	return m, nil, false
}

func (m *Model) handleMenuUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	if msg, ok := msg.(tea.KeyMsg); ok {
		if msg.String() == "enter" {
			if i, ok := m.menu.SelectedItem().(menuItem); ok {
				if i.action == viewStateRemotes {
					m.remotes = NewRemotesModel(m.cfg)
					m.state = i.action
					return m, nil
				}
				if i.action == viewStateGitSetup {
					m.gitSetup = NewGitSetupModel(m.cfg)
					m.state = i.action
					return m, m.gitSetup.Init()
				}
				if i.action == viewStateGeneralSettings {
					m.generalSetup = NewGeneralSetupModel(m.cfg)
					m.state = i.action
					return m, m.generalSetup.Init()
				}
				m.state = i.action
				return m, nil
			}
		}
		if msg.String() == "esc" {
			if m.menu.FilterState() != list.Filtering {
				m.state = viewStateMain
				return m, nil
			}
		}
	}
	var cmds []tea.Cmd
	m.menu, cmd = m.menu.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m *Model) handleRemotesUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
		if m.remotes.list.FilterState() != list.Filtering {
			m.state = viewStateMenu
			return m, nil
		}
	}

	var subModel tea.Model
	var cmd tea.Cmd
	subModel, cmd = m.remotes.Update(msg)
	m.remotes = subModel.(RemotesModel)

	if m.remotes.done {
		items := make([]list.Item, len(m.remotes.DiscoveredSessions))
		for i, s := range m.remotes.DiscoveredSessions {
			items[i] = item{session: s}
		}
		m.list.SetItems(items)
		m.remotes.done = false
		m.state = viewStateMain
		return m, nil
	}
	return m, cmd
}

func (m *Model) handleSetupUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
		m.state = viewStateMenu
		return m, nil
	}
	var subModel tea.Model
	var cmd tea.Cmd
	subModel, cmd = m.setup.Update(msg)
	m.setup = subModel.(SetupModel)
	if m.setup.saved {
		m.setup.saved = false
		m.state = viewStateMenu
	}
	return m, cmd
}

func (m *Model) handleGitSetupUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
		m.state = viewStateMenu
		return m, nil
	}
	var subModel tea.Model
	var cmd tea.Cmd
	subModel, cmd = m.gitSetup.Update(msg)
	m.gitSetup = subModel.(GitSetupModel)
	if m.gitSetup.saved {
		m.gitSetup.saved = false
		m.state = viewStateMenu
	}
	return m, cmd
}

func (m *Model) handleGeneralSetupUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
		m.state = viewStateMenu
		return m, nil
	}
	var subModel tea.Model
	var cmd tea.Cmd
	subModel, cmd = m.generalSetup.Update(msg)
	m.generalSetup = subModel.(GeneralSetupModel)
	if m.generalSetup.saved {
		m.generalSetup.saved = false
		m.state = viewStateMenu
	}
	return m, cmd
}

func (m *Model) handleIssuePickerUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
		if m.issuePicker.list.FilterState() != list.Filtering {
			m.state = viewStateMain
			return m, nil
		}
	}
	if msg, ok := msg.(jiraIssuesMsg); ok {
		if msg.err != nil {
			m.lastError = fmt.Sprintf("Failed to fetch JIRA issues: %v", msg.err)
			m.state = viewStateVSCodeError
			return m, nil
		}
		m.issuePicker = NewIssuePickerModel(msg.issues)
		m.issuePicker.SetSize(m.width, m.height)
		return m, nil
	}
	var subModel tea.Model
	var cmd tea.Cmd
	subModel, cmd = m.issuePicker.Update(msg)
	m.issuePicker = subModel.(IssuePickerModel)
	if m.issuePicker.AdHoc {
		m.sessionCfg.IssueKey = ""
		m.sessionCfg.Branch = ""
		m.state = viewStateBranchInput
		m.branchInput = NewBranchInputModel("adhoc-session")
		return m, nil
	}
	if m.issuePicker.selected != nil {
		slug := m.issuePicker.selected.Slug()
		m.sessionCfg.IssueKey = m.issuePicker.selected.Key
		m.state = viewStateBranchInput
		m.branchInput = NewBranchInputModel(slug)
		return m, nil
	}
	return m, cmd
}

func (m *Model) handleBranchInputUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
		m.state = viewStateIssuePicker
		return m, nil
	}
	var subModel tea.Model
	var cmd tea.Cmd
	subModel, cmd = m.branchInput.Update(msg)
	m.branchInput = subModel.(BranchInputModel)
	if m.branchInput.Confirmed {
		m.sessionCfg.Branch = m.branchInput.Value()
		m.loadingMsg = "Loading repositories..."
		m.loadingNext = viewStateRepoPicker
		m.state = viewStateLoading
		m.picker = NewRepoPickerModel(nil, &m.cfg.Git)
		return m, m.fetchRepos()
	}
	return m, cmd
}

func (m *Model) handleRepoPickerUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
		if m.picker.list.FilterState() != list.Filtering {
			m.state = viewStateBranchInput
			return m, nil
		}
	}
	if msg, ok := msg.(reposMsg); ok {
		if msg.err != nil {
			m.lastError = fmt.Sprintf("Failed to fetch repos: %v", msg.err)
			m.state = viewStateVSCodeError
			return m, nil
		}
		m.picker = NewRepoPickerModel(msg.repos, &m.cfg.Git)
		h, v := docStyle.GetFrameSize()
		m.picker.list.SetSize(m.width-h, m.height-v)
		return m, nil
	}
	var subModel tea.Model
	var cmd tea.Cmd
	subModel, cmd = m.picker.Update(msg)
	m.picker = subModel.(RepoPickerModel)
	if m.picker.Skip {
		m.sessionCfg.Repo = domain.Repo{Name: "No Repository", URL: ""}
		m.loadingMsg = "Scanning remote root..."
		m.loadingNext = viewStateDirPicker
		m.state = viewStateLoading
		return m, m.fetchRepoDirectories(nil)
	}
	if m.picker.selected != nil {
		// Repo selected, now fetch directories
		m.sessionCfg.Repo = *m.picker.selected

		if m.sessionCfg.Repo.IsNew {
			// It's a new repo, skip directory scan
			m.sessionCfg.Directory = "."
			m.loadingMsg = "Scanning available agents..."
			m.loadingNext = viewStateAgentPicker
			m.state = viewStateLoading
			return m, m.fetchAgents()
		}

		m.loadingMsg = "Scanning directories..."
		m.loadingNext = viewStateDirPicker
		m.state = viewStateLoading
		return m, m.fetchRepoDirectories(m.picker.selected)
	}
	return m, cmd
}

func (m *Model) handleDirPickerUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
		if m.dirPicker.list.FilterState() != list.Filtering {
			m.state = viewStateRepoPicker
			return m, nil
		}
	}
	if msg, ok := msg.(dirsMsg); ok {
		if msg.err != nil {
			m.lastError = fmt.Sprintf("Failed to fetch directories: %v", msg.err)
			m.state = viewStateVSCodeError
			return m, nil
		}
		m.dirPicker = NewDirPickerModel(msg.dirs, *m.picker.selected)
		h, v := docStyle.GetFrameSize()
		m.dirPicker.SetSize(m.width-h, m.height-v)
		return m, nil
	}
	var subModel tea.Model
	var cmd tea.Cmd
	subModel, cmd = m.dirPicker.Update(msg)
	m.dirPicker = subModel.(DirPickerModel)
	if m.dirPicker.selected != "" {
		m.sessionCfg.Directory = m.dirPicker.selected
		m.loadingMsg = "Scanning available agents..."
		m.loadingNext = viewStateAgentPicker
		m.state = viewStateLoading
		return m, m.fetchAgents()
	}
	return m, cmd
}

func (m *Model) handleAgentPickerUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
		if m.agentPicker.list.FilterState() != list.Filtering {
			m.state = viewStateDirPicker
			return m, nil
		}
	}
	var subModel tea.Model
	var cmd tea.Cmd
	subModel, cmd = m.agentPicker.Update(msg)
	m.agentPicker = subModel.(AgentPickerModel)
	if m.agentPicker.selected != nil {
		m.sessionCfg.Agent = m.agentPicker.selected
		m.summary = NewSummaryModel(m.sessionCfg.IssueKey, m.sessionCfg.Branch, m.sessionCfg.Repo, m.sessionCfg.Directory)
		m.summary.SetAgent(m.sessionCfg.Agent)
		m.summary.SetSize(m.width, m.height)
		m.state = viewStateSummary
		return m, nil
	}
	return m, cmd
}

func (m *Model) handleSummaryUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
		m.state = viewStateAgentPicker
		return m, nil
	}
	var subModel tea.Model
	var cmd tea.Cmd
	subModel, cmd = m.summary.Update(msg)
	m.summary = subModel.(SummaryModel)
	if m.summary.IsConfirmed() {
		m.loadingMsg = "Creating session..."
		m.loadingNext = viewStateMain
		m.state = viewStateLoading
		return m, m.createSession()
	}
	return m, cmd
}

func (m *Model) handleTerminateConfirmUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "esc", "n":
			m.terminatePrecheckError = ""
			m.state = viewStateMain
			return m, nil
		case "y":
			if sel := m.list.SelectedItem(); sel != nil {
				s := sel.(item).session
				if err := m.validateTerminationPreconditions(s); err != nil {
					m.terminatePrecheckError = err.Error()
					return m, nil
				}
				m.terminatePrecheckError = ""
				m.terminateSession = s
				m.terminateSteps = []string{
					"Stopping mutagen sync",
					"Killing tmux session",
					"Stopping agent process",
					"Removing git worktree",
					"Cleaning local files",
					"Updating session status",
				}
				m.terminateErrors = make([]string, len(m.terminateSteps))
				m.terminateIndex = 0
				m.state = viewStateTerminateProgress
				return m, m.runTerminateStepCmd(0)
			}
		}
	}
	return m, nil
}

func (m *Model) handleWorktreeExistsUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "c", "esc":
			m.state = viewStateMain
			return m, nil
		case "b":
			m.state = viewStateBranchInput
			slug := m.sessionCfg.IssueKey
			if m.sessionCfg.Branch != "" {
				slug = m.sessionCfg.Branch
			}
			m.branchInput = NewBranchInputModel(slug)
			return m, nil
		}
	}
	return m, nil
}

func (m *Model) handleTerminateProgressUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(terminateStepMsg); ok {
		if msg.err != nil {
			m.terminateErrors[msg.index] = msg.err.Error()
		}
		next := msg.index + 1
		if next < len(m.terminateSteps) {
			m.terminateIndex = next
			return m, m.runTerminateStepCmd(next)
		}
		items := []list.Item{}
		for _, it := range m.list.Items() {
			if sessItem, ok := it.(item); ok {
				if sessItem.session.TmuxSession == m.terminateSession.TmuxSession {
					continue
				}
			}
			items = append(items, it)
		}
		m.list.SetItems(items)
		m.state = viewStateMain
		return m, nil
	}
	return m, nil
}

func (m *Model) handleLoadingUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case discoveryResultMsg:
		m.log("Discovered %d sessions", len(msg))
		ctx := context.Background()
		for i := range msg {
			if m.db != nil {
				_ = m.db.Save(ctx, &msg[i])
			}
		}
		items := make([]list.Item, len(msg))
		for i, s := range msg {
			items[i] = item{session: s, needsInput: false, activity: ""}
		}
		m.list.SetItems(items)
		m.state = viewStateMain
		return m, nil
	case attachMsg:
		return m, tea.ExecProcess(msg.cmd, func(err error) tea.Msg {
			return attachDoneMsg{}
		})
	case attachDoneMsg:
		m.state = viewStateMain
		m.panelMode = panelModePreview
		if sel := m.list.SelectedItem(); sel != nil {
			s := sel.(item).session
			m.activeSession = s.TmuxSession
			m.tmuxOutput = "Loading..."
			m.viewport.SetContent(m.tmuxOutput)
			return m, tea.Batch(tickTmux(), fetchTmuxPane(m.cfg, s))
		}
		return m, tickTmux()
	case tmuxTerminalMsg:
		if msg.err != nil {
			m.tmuxOutput = failStyle.Render("Failed to stream session: " + msg.err.Error())
			m.panelMode = panelModePreview
			m.state = viewStateMain
			return m, nil
		}
		m.termCloser = msg.stream
		term := NewTerminalModel(msg.stream, m.viewport.Width, m.viewport.Height)
		m.terminal = &term
		m.panelMode = panelModeTerminal
		m.state = viewStateMain
		return m, nil
	case reposMsg:
		if msg.err != nil {
			m.lastError = fmt.Sprintf("Failed to fetch repos: %v", msg.err)
			m.state = viewStateVSCodeError
			return m, nil
		}
		m.picker = NewRepoPickerModel(msg.repos, &m.cfg.Git)
		h, v := docStyle.GetFrameSize()
		m.picker.list.SetSize(m.width-h, m.height-v)
		m.state = m.loadingNext
		return m, nil
	case dirsMsg:
		if msg.err != nil {
			m.lastError = fmt.Sprintf("Failed to fetch directories: %v", msg.err)
			m.state = viewStateVSCodeError
			return m, nil
		}
		m.dirPicker = NewDirPickerModel(msg.dirs, m.sessionCfg.Repo)
		h, v := docStyle.GetFrameSize()
		m.dirPicker.SetSize(m.width-h, m.height-v)
		m.state = m.loadingNext
		return m, nil
	case recreateMutagenMsg:
		if msg.err != nil {
			m.lastError = fmt.Sprintf("Failed to recreate mutagen sync: %v", msg.err)
			m.state = viewStateVSCodeError
			return m, nil
		}
		m.state = viewStateMain
		return m, nil
	case agent.ScanAgentsMsg:
		if msg.Err != nil {
			m.lastError = fmt.Sprintf("Failed to scan agents: %v", msg.Err)
			m.state = viewStateVSCodeError
			return m, nil
		}
		m.agentPicker = NewAgentPickerModel(msg.Agents)
		h, v := docStyle.GetFrameSize()
		m.agentPicker.SetSize(m.width-h, m.height-v)
		m.state = m.loadingNext
		return m, nil
	case sessionCreateMsg:
		if msg.err != nil {
			// Check if it's a worktree exists error
			if msg.err.Error() == "WORKTREE_EXISTS" {
				m.state = viewStateWorktreeExists
				return m, nil
			}
			m.lastError = fmt.Sprintf("Failed to create/restart session: %v", msg.err)
			m.state = viewStateVSCodeError
			return m, nil
		}

		items := m.list.Items()
		if m.restartingSession != nil {
			// Update existing session in the list
			for i, it := range items {
				if sessItem, ok := it.(item); ok && sessItem.session.TmuxSession == msg.session.TmuxSession {
					sessItem.session = msg.session
					items[i] = sessItem
					m.list.Select(i)
					break
				}
			}
			m.restartingSession = nil
		} else {
			// Add new session to list
			items = append(items, item{session: msg.session, needsInput: false, activity: ""})
			m.list.Select(len(items) - 1)
		}
		m.list.SetItems(items)

		// Fetch preview for the session
		m.activeSession = msg.session.TmuxSession
		m.tmuxOutput = "Loading..."
		m.viewport.SetContent(m.tmuxOutput)
		m.state = m.loadingNext

		return m, tea.Batch(tickTmux(), fetchTmuxPane(m.cfg, msg.session))
	}
	return m, nil
}

func (m *Model) renderMainView() string {
	// Split View
	h, _ := docStyle.GetFrameSize()
	sidebarWidth := m.width / 3
	mainWidth := m.width - sidebarWidth - h - 2

	// Sidebar
	sidebar := m.list.View()

	// Main Panel
	var mainContent string
	if sel := m.list.SelectedItem(); sel != nil {
		s := sel.(item).session

		// 1. Session Details (Left Column)
		var sessionDetails strings.Builder
		sessionDetails.WriteString(activeStyle.Render("SESSION DETAILS") + "\n\n")
		sessionDetails.WriteString(fmt.Sprintf("Tmux: %s\n", s.TmuxSession))
		sessionDetails.WriteString(fmt.Sprintf("Host: %s\n", s.RemoteHost))
		sessionDetails.WriteString(fmt.Sprintf("Repo: %s\n", s.RepoName))
		sessionDetails.WriteString(fmt.Sprintf("Path: %s\n", s.WorktreePath))
		if s.MutagenSyncID != "" {
			sessionDetails.WriteString(fmt.Sprintf("Local Sync: %s\n", successStyle.Render(s.LocalPath)))
		} else {
			sessionDetails.WriteString("Local Sync: " + failStyle.Render("None") + "\n")
		}
		if s.IssueKey != "" {
			sessionDetails.WriteString(fmt.Sprintf("JIRA: %s\n", s.IssueKey))
		}
		sessionDetails.WriteString(fmt.Sprintf("\nStatus: %s\n", s.Status))
		sessionDetails.WriteString(fmt.Sprintf("Created: %s\n", s.CreatedAt.Format("2006-01-02 15:04:05")))

		// 2. Git Intelligence (Right Column)
		var gitDetails strings.Builder
		gitDetails.WriteString(activeStyle.Render("GIT INTELLIGENCE") + "\n\n")
		if m.gitStatus.Branch != "" {
			gitDetails.WriteString(fmt.Sprintf("Branch: %s", m.gitStatus.Branch))
			if m.gitStatus.Ahead > 0 || m.gitStatus.Behind > 0 {
				gitDetails.WriteString(fmt.Sprintf(" (↑%d ↓%d)", m.gitStatus.Ahead, m.gitStatus.Behind))
			}
			gitDetails.WriteString("\n")

			if m.gitStatus.StagedCount > 0 || m.gitStatus.UnstagedCount > 0 || m.gitStatus.UntrackedCount > 0 {
				gitDetails.WriteString(fmt.Sprintf("Changes: %d staged, %d unstaged, %d untracked\n",
					m.gitStatus.StagedCount, m.gitStatus.UnstagedCount, m.gitStatus.UntrackedCount))
			} else {
				gitDetails.WriteString("Changes: clean\n")
			}

			if m.gitStatus.PullRequest != nil {
				pr := m.gitStatus.PullRequest
				gitDetails.WriteString(fmt.Sprintf("PR: #%d %s (%s)\n", pr.Number, pr.Title, pr.State))
				reviewColor := "#7D7D7D" // grey
				switch pr.ReviewStatus {
				case "approved":
					reviewColor = "#00FF00" // green
				case "changes_requested":
					reviewColor = "#FF0000" // red
				case "pending":
					reviewColor = "#FFA500" // orange
				}
				gitDetails.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(reviewColor)).Render(fmt.Sprintf("Review: %s", pr.ReviewStatus)))
				if pr.CommentCount > 0 {
					gitDetails.WriteString(fmt.Sprintf(" (%d comments)", pr.CommentCount))
				}
				gitDetails.WriteString("\n")

				// Checks status
				if pr.ChecksStatus != "none" {
					checkColor := "#7D7D7D" // grey
					switch pr.ChecksStatus {
					case "success":
						checkColor = "#00FF00" // green
					case "failure":
						checkColor = "#FF0000" // red
					case "pending":
						checkColor = "#FFA500" // orange
					}
					gitDetails.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(checkColor)).Render(fmt.Sprintf("Checks: %s", pr.ChecksSummary)))
					gitDetails.WriteString("\n")
				}
			} else {
				gitDetails.WriteString("PR: none\n")
			}
		} else {
			gitDetails.WriteString("Loading git status...\n")
		}

		// Combine columns
		colWidth := (mainWidth - 4) / 2
		leftCol := lipgloss.NewStyle().Width(colWidth).Render(sessionDetails.String())
		rightCol := lipgloss.NewStyle().Width(colWidth).PaddingLeft(2).
			Border(lipgloss.NormalBorder(), false, false, false, true). // Left border for separator
			Render(gitDetails.String())

		infoPanel := lipgloss.JoinHorizontal(lipgloss.Top, leftCol, rightCol)

		// 3. Tmux Output (Bottom)
		var outputPanel strings.Builder
		outputPanel.WriteString("\n" + strings.Repeat("─", mainWidth-4) + "\n")
		modeName := "PREVIEW"
		if m.panelMode == panelModeTerminal {
			modeName = "TERMINAL"
		}
		outputPanel.WriteString(activeStyle.Render(modeName) + " (ctrl+t toggle, ctrl+s full screen)\n\n")

		if m.panelMode == panelModeTerminal && m.terminal != nil {
			outputPanel.WriteString(m.terminal.View())
		} else {
			outputPanel.WriteString(m.viewport.View())
		}

		mainContent = infoPanel + outputPanel.String()
	} else {
		mainContent = "\n\n  No session selected.\n  Press 'm' for Admin Menu."
	}

	mainStyle := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, false, true). // Left border only
		PaddingLeft(2).
		Width(mainWidth)

	content := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, mainStyle.Render(mainContent))

	// Footer (Checks & Active Remote)
	var doctorOutput strings.Builder
	doctorOutput.WriteString("Startup Checks:\n")
	for _, res := range m.doctorResults {
		status := successStyle.Render("✓")
		if !res.Passed {
			status = failStyle.Render("✗")
		}
		doctorOutput.WriteString(fmt.Sprintf("%s %-10s: %s\n", status, res.Name, res.Message))
	}

	var activeHost string
	if m.cfg.ActiveRemote != "" {
		activeHost = successStyle.Render(m.cfg.ActiveRemote)
	} else {
		activeHost = failStyle.Render("None")
	}

	footer := "\nActive Remote: " + activeHost + "\n\n" + doctorOutput.String()

	return docStyle.Render(content + "\n" + footer)
}

func (m *Model) handleRestartAgentPickerUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
		if m.agentPicker.list.FilterState() != list.Filtering {
			m.state = viewStateMain
			m.restartingSession = nil
			return m, nil
		}
	}

	var subModel tea.Model
	var cmd tea.Cmd
	subModel, cmd = m.agentPicker.Update(msg)
	m.agentPicker = subModel.(AgentPickerModel)

	if m.agentPicker.selected != nil {
		m.sessionCfg.Agent = m.agentPicker.selected
		m.loadingMsg = fmt.Sprintf("Restarting session %s...", m.restartingSession.TmuxSession)
		m.loadingNext = viewStateMain
		m.state = viewStateLoading
		return m, m.restartSession()
	}

	return m, cmd
}

func (m *Model) restartSession() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		s := m.restartingSession
		if s == nil {
			return sessionCreateMsg{err: fmt.Errorf("no session to restart")}
		}

		remote, ok := resolveRemote(m.cfg, *s)
		if !ok {
			return sessionCreateMsg{err: fmt.Errorf("no active remote configured")}
		}

		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})

		// Ensure working directory exists
		workingDir := s.WorktreePath
		if err := mgr.ValidateDir(ctx, workingDir); err != nil {
			return sessionCreateMsg{err: fmt.Errorf("working directory not found: %w", err)}
		}

		// 1. Kill existing tmux session if it exists
		mgr.Execute(ctx, fmt.Sprintf("tmux kill-session -t %s", s.TmuxSession))

		// 2. Terminate existing mutagen sync if it exists
		// We ignore errors here because it might not exist
		mutagenCmd := exec.CommandContext(ctx, "mutagen", "sync", "terminate", s.TmuxSession)
		_ = mutagenCmd.Run()

		// 3. Start tmux session and agent
		agentCmd := m.sessionCfg.Agent.Command
		startCmd := fmt.Sprintf("tmux new-session -d -s %q -c %q %q", s.TmuxSession, workingDir, agentCmd)
		_, tmuxErr := mgr.Execute(ctx, startCmd)
		if tmuxErr != nil {
			return sessionCreateMsg{err: fmt.Errorf("failed to start tmux session: %w", tmuxErr)}
		}

		// 4. Start mutagen sync
		mutagenEngine := mutagen.NewEngine()
		home, _ := os.UserHomeDir()
		localSyncPath := fmt.Sprintf("%s/%s/work/%s", home, config.DirName, s.TmuxSession)
		target := remote.Host
		if remote.User != "" {
			target = fmt.Sprintf("%s@%s", remote.User, remote.Host)
		}
		remoteSyncPath := fmt.Sprintf("%s:%s", target, workingDir)
		if err := mutagenEngine.StartSync(ctx, localSyncPath, remoteSyncPath); err != nil {
			// We continue even if sync fails, but log it
			m.log("Warning: failed to restart mutagen sync: %v", err)
		}

		// Update session status
		s.Status = domain.SessionStatusSyncing
		s.AgentName = m.sessionCfg.Agent.Name
		s.UpdatedAt = time.Now()

		if m.db != nil {
			_ = m.db.Save(ctx, s)
		}

		return sessionCreateMsg{session: *s, err: nil}
	}
}

func (m *Model) handleRestartConfirmUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "y":
			m.loadingMsg = "Scanning available agents..."
			m.loadingNext = viewStateRestartAgentPicker
			m.state = viewStateLoading
			return m, m.fetchAgents()
		case "n", "esc":
			m.state = viewStateMain
			m.restartingSession = nil
			return m, nil
		}
	}
	return m, nil
}
