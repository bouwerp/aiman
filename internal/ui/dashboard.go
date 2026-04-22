package ui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/ai"
	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/git"
	"github.com/bouwerp/aiman/internal/infra/jira"
	"github.com/bouwerp/aiman/internal/infra/mutagen"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/pane"
	"github.com/bouwerp/aiman/internal/usecase"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

var (
	docStyle     = lipgloss.NewStyle().Margin(1, 2)
	statusStyle  = lipgloss.NewStyle().PaddingLeft(2).Italic(true).Foreground(lipgloss.Color("#7D7D7D"))
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#00FF00"))
	failStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF0000"))
	titleStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("62")).Bold(true).Underline(true)
	activeStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Bold(true)
	promptRegex  = regexp.MustCompile(`^[\w\-@~/:\.]+\s*(\$|#|>)\s*$`)
)

// copyStringToSystemClipboard writes text to the OS clipboard when a native helper exists.
func copyStringToSystemClipboard(s string) error {
	switch runtime.GOOS {
	case "darwin":
		cmd := exec.Command("pbcopy")
		cmd.Stdin = strings.NewReader(s)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("pbcopy: %w", err)
		}
		return nil
	case "linux":
		if path, err := exec.LookPath("wl-copy"); err == nil {
			cmd := exec.Command(path)
			cmd.Stdin = strings.NewReader(s)
			return cmd.Run()
		}
		if path, err := exec.LookPath("xclip"); err == nil {
			cmd := exec.Command(path, "-selection", "clipboard")
			cmd.Stdin = strings.NewReader(s)
			return cmd.Run()
		}
		return fmt.Errorf("install wl-copy or xclip for clipboard support on Linux")
	default:
		return fmt.Errorf("clipboard copy not implemented on %s", runtime.GOOS)
	}
}

// normalizeMultiline trims trailing spaces per line and leading/trailing blank lines.
func normalizeMultiline(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// yankSessionOutputToClipboard copies visible or full preview buffer to the OS clipboard.
// Mouse selection in alternate-screen TUIs often does not reach the system clipboard; use y / Y instead.
func (m *Model) yankSessionOutputToClipboard(fullBuffer bool) bool {
	if m.list.SelectedItem() == nil {
		return false
	}
	var raw string
	if m.panelMode == panelModeTerminal && m.terminal != nil {
		raw = m.terminal.View()
	} else if fullBuffer {
		raw = m.tmuxOutput
	} else {
		raw = m.viewport.View()
	}
	text := normalizeMultiline(ansi.Strip(raw))
	if text == "" {
		m.lastError = "Nothing to copy from this session view yet."
		m.state = viewStateVSCodeError
		return true
	}
	if err := copyStringToSystemClipboard(text); err != nil {
		m.lastError = fmt.Sprintf("Could not copy to clipboard: %v", err)
		m.state = viewStateVSCodeError
		return true
	}
	m.log("Copied session output to clipboard (%d chars)", len(text))
	return true
}

type viewState int

const (
	viewStateMain viewState = iota
	viewStateMenu
	viewStateRemotes
	viewStateSetup
	viewStateGitSetup
	viewStateGeneralSettings
	viewStateAISettings
	viewStateSnapshotBrowser
	viewStateArchiveSession // sentinel: archive selected session from menu
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
	viewStateBranchPicker
	viewStateModePicker
	viewStateRestartAgentPicker
	viewStateRestartConfirm
	viewStateSnapshotPreview
	viewStateArchivePreview
	viewStateArchiveProgress
	viewStateChangeDirPicker
	viewStateChangeDirConfirm
	viewStateProvisioningRemotePicker
	viewStateProvisioningProgress
	viewStateAuthRemotePicker
	viewStateAuthWizard
	viewStateTunnelManager
	viewStateTunnelAdd
	viewStateError        // generic error dialog (press any key to dismiss)
	viewStateRemotePicker // select remote for new session
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
	remoteName string // short name of the remote for display
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
	remoteTag := ""
	if i.remoteName != "" {
		remoteTag = " [" + i.remoteName + "]"
	}
	if i.session.IssueKey != "" {
		return fmt.Sprintf("%s%s (%s)%s%s", prefix, i.session.IssueKey, i.session.TmuxSession, activity, remoteTag)
	}
	return fmt.Sprintf("%s%s%s%s", prefix, i.session.TmuxSession, activity, remoteTag)
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
	return i.session.IssueKey + " " + i.session.TmuxSession + " " + i.session.RepoName + " " + i.remoteName
}

type tunnelItem struct {
	tunnel  domain.Tunnel
	running bool
}

func (i tunnelItem) Title() string {
	state := failStyle.Render("stopped")
	if i.running {
		state = successStyle.Render("running")
	}
	return fmt.Sprintf("localhost:%d -> remote:%d  (%s)", i.tunnel.LocalPort, i.tunnel.RemotePort, state)
}

func (i tunnelItem) Description() string {
	return "SSH local forward"
}

func (i tunnelItem) FilterValue() string {
	return fmt.Sprintf("%d %d", i.tunnel.LocalPort, i.tunnel.RemotePort)
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
	aiSetup                AISetupModel
	snapshotBrowser        SnapshotBrowserModel
	picker                 RepoPickerModel
	issuePicker            IssuePickerModel
	branchInput            BranchInputModel
	branchPicker           BranchPickerModel
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
	sessionCfg             domain.SessionConfig
	loadingNext            viewState
	initialLoad            bool
	terminateSession       domain.Session
	terminateSteps         []string
	terminateErrors        []string
	terminateIndex         int
	terminatePrecheckError string
	terminateForced        bool
	consoleOpen            bool
	consoleLog             []string
	consoleViewport        viewport.Model
	gitStatus              domain.GitStatus
	lastGitStatusUpdate    time.Time
	restartingSession      *domain.Session
	changingDirSession     *domain.Session
	flowManager            *usecase.FlowManager
	firstLoad              map[string]bool
	selectedRemote         config.Remote    // remote chosen for the current new-session wizard
	remoteFilter           string           // "" = all remotes, otherwise a Remote.Host to filter by
	allSessions            []domain.Session // unfiltered master session list
	mouseEnabled           bool
	provisionSteps         []domain.ProvisionStep
	provisioningIdx        int
	provisioningError      string
	provisioningStatus     string
	provisionSpinner       spinner.Model
	authSteps              []authWizardStep
	authStepIdx            int
	authStepStatus         map[int]string
	authStepDetails        map[int]string
	authStatusMsg          string
	authChecking           bool
	tunnelList             list.Model
	tunnelSession          *domain.Session
	tunnelInput            textinput.Model
	tunnelError            string
	intelligence           domain.IntelligenceProvider
	snapshotManager        *usecase.SnapshotManager
	aiSummary              map[string]*domain.SessionSummary // keyed by TmuxSession
	aiLoading              bool
	aiError                string
	snapshotToast          string
	snapshotToastError     bool
	priorSnapshotCandidate *domain.SessionSnapshot
	archivePreview         *archivePreviewData
	archivePreviewVP       viewport.Model
	archiveSteps           []archiveStep
	archiveStepIdx         int
}

// archivePreviewData holds the pre-computed data shown in the archive confirmation popup.
type archivePreviewData struct {
	session        domain.Session
	summary        *domain.SessionSummary
	rawPane        string // full raw pane capture (for inspection/dump)
	rawPaneLen     int    // chars captured from tmux (before cleaning)
	cleanedPaneLen int    // chars after pane.Clean() (before truncation)
	compressedSize int    // bytes of gzip-compressed cleaned pane
	cleanedPane    string // full cleaned pane text (for head/tail preview)
	promptHead     string // first N chars sent to the model (SESSION START)
	promptTail     string // last M chars sent to the model (SESSION RECENT ACTIVITY)
	snapshot       *domain.SessionSnapshot
}

// archiveStep represents one step in the archive progress view.
type archiveStep struct {
	label string
	done  bool
	err   bool
}

// archiveStepMsg is sent from loadArchivePreviewCmd to advance the step indicator.
type archiveStepMsg struct{ idx int }

// archiveStepErrMsg signals that a step failed.
type archiveStepErrMsg struct {
	idx int
	err error
}

func remoteNameForHost(cfg *config.Config, host string) string {
	for _, r := range cfg.Remotes {
		if r.Host == host {
			if r.Name != "" {
				return r.Name
			}
			return r.Host
		}
	}
	return host
}

func (m *Model) makeItem(s domain.Session) item {
	return item{
		session:    s,
		remoteName: remoteNameForHost(m.cfg, s.RemoteHost),
	}
}

func (m *Model) applyRemoteFilter() {
	var filtered []list.Item
	for _, s := range m.allSessions {
		if m.remoteFilter == "" || s.RemoteHost == m.remoteFilter {
			filtered = append(filtered, m.makeItem(s))
		}
	}
	m.list.SetItems(filtered)
	if m.remoteFilter == "" {
		m.list.Title = "Aiman Dashboard - Active Sessions"
	} else {
		m.list.Title = "Sessions [" + remoteNameForHost(m.cfg, m.remoteFilter) + "]"
	}
}

func NewModel(cfg *config.Config, doctorResults []usecase.CheckResult, initialSessions []domain.Session, db domain.SessionRepository, flowManager *usecase.FlowManager, intelligence domain.IntelligenceProvider, snapshotManager *usecase.SnapshotManager, initialLogs ...string) *Model {
	items := make([]list.Item, len(initialSessions))
	for i, s := range initialSessions {
		items[i] = item{session: s, remoteName: remoteNameForHost(cfg, s.RemoteHost)}
	}

	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Aiman Dashboard - Active Sessions"
	l.AdditionalFullHelpKeys = func() []key.Binding {
		return []key.Binding{
			key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new session")),
			key.NewBinding(key.WithKeys("ctrl+r", "s"), key.WithHelp("s", "restart session")),
			key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "change directory scope")),
			key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "manage tunnels")),
			key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "admin menu")),
			key.NewBinding(key.WithKeys("ctrl+s", "a"), key.WithHelp("a", "attach full terminal")),
			key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "open vscode")),
			key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "copy local path")),
			key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "copy session output (visible)")),
			key.NewBinding(key.WithKeys("Y"), key.WithHelp("Y", "copy session output (full preview)")),
			key.NewBinding(key.WithKeys("G", "end"), key.WithHelp("G/end", "jump preview to latest")),
			key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh status")),
			key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "AI insight")),
			key.NewBinding(key.WithKeys("ctrl+a"), key.WithHelp("ctrl+a", "archive session")),
			key.NewBinding(key.WithKeys("ctrl+y"), key.WithHelp("ctrl+y", "recreate mutagen sync")),
			key.NewBinding(key.WithKeys("ctrl+k"), key.WithHelp("ctrl+k", "terminate session")),
			key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "filter by remote")),
			key.NewBinding(key.WithKeys("`"), key.WithHelp("`", "toggle debug console")),
		}
	}

	menuItems := []list.Item{
		menuItem{title: "Manage Remote Servers", desc: "Add, edit, or remove remote dev servers", action: viewStateRemotes},
		menuItem{title: "Provision Remote Server", desc: "100% setup: install gh, agents, node, and skills", action: viewStateProvisioningRemotePicker},
		menuItem{title: "Auth Setup Wizard", desc: "Guided auth checks and instructions per remote tool", action: viewStateAuthRemotePicker},
		menuItem{title: "JIRA Configuration", desc: "Update URL, Email, and Token", action: viewStateSetup},
		menuItem{title: "Git Configuration", desc: "Configure repositories and organizations", action: viewStateGitSetup},
		menuItem{title: "General Settings", desc: "Experimental and general features", action: viewStateGeneralSettings},
		menuItem{title: "AI Settings", desc: "Enable local AI and configure Ollama model/host", action: viewStateAISettings},
		menuItem{title: "Session Snapshots", desc: "Browse archived session snapshots", action: viewStateSnapshotBrowser},
	}
	m := list.New(menuItems, list.NewDefaultDelegate(), 0, 0)
	m.Title = "Administrative Menu"

	vp := viewport.New(0, 0)
	vp.MouseWheelEnabled = true
	vp.MouseWheelDelta = 10
	vp.Style = lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false). // Top border
		PaddingTop(1)

	model := &Model{
		cfg:             cfg,
		db:              db,
		flowManager:     flowManager,
		state:           viewStateMain,
		panelMode:       panelModePreview,
		list:            l,
		menu:            m,
		remotes:         NewRemotesModel(cfg),
		setup:           NewSetupModel(cfg),
		gitSetup:        NewGitSetupModel(cfg),
		generalSetup:    NewGeneralSetupModel(cfg),
		aiSetup:         NewAISetupModel(cfg),
		doctorResults:   doctorResults,
		viewport:        vp,
		firstLoad:       make(map[string]bool),
		allSessions:     initialSessions,
		mouseEnabled:    true,
		tunnelList:      list.New(nil, list.NewDefaultDelegate(), 0, 0),
		intelligence:    intelligence,
		snapshotManager: snapshotManager,
		aiSummary:       make(map[string]*domain.SessionSummary),
	}
	model.tunnelList.Title = "Session Tunnels"
	model.provisionSpinner = spinner.New()
	model.provisionSpinner.Spinner = spinner.Dot
	model.provisionSpinner.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
	for _, log := range initialLogs {
		model.consoleLog = append(model.consoleLog, log)
	}
	return model
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

type aiSummaryMsg struct {
	session string
	summary *domain.SessionSummary
	err     error
}

type snapshotSavedMsg struct {
	snapshot *domain.SessionSnapshot
	err      error
}

type snapshotToastMsg struct {
	text    string
	isError bool
}

type snapshotPreviewMsg struct {
	snapshot *domain.SessionSnapshot // nil = no snapshot found
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

type tunnelStatesMsg struct {
	sessionID string
	items     []list.Item
	err       error
}

type tunnelToggleMsg struct {
	sessionID  string
	localPort  int
	remotePort int
	running    bool
	err        error
}

func parseTunnelSpec(spec string) (domain.Tunnel, error) {
	raw := strings.TrimSpace(spec)
	if raw == "" {
		return domain.Tunnel{}, fmt.Errorf("enter tunnel as local:remote (e.g. 5173:5173)")
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 2 {
		return domain.Tunnel{}, fmt.Errorf("invalid tunnel format %q (expected local:remote)", raw)
	}
	localPort, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || localPort <= 0 || localPort > 65535 {
		return domain.Tunnel{}, fmt.Errorf("invalid local port: %q", parts[0])
	}
	remotePort, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || remotePort <= 0 || remotePort > 65535 {
		return domain.Tunnel{}, fmt.Errorf("invalid remote port: %q", parts[1])
	}
	return domain.Tunnel{LocalPort: localPort, RemotePort: remotePort}, nil
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

func (m *Model) updateSessionInMemory(updated domain.Session) {
	for i, s := range m.allSessions {
		if s.ID == updated.ID {
			m.allSessions[i] = updated
			break
		}
	}
	items := m.list.Items()
	for i, it := range items {
		sessItem, ok := it.(item)
		if !ok {
			continue
		}
		if sessItem.session.ID == updated.ID {
			sessItem.session = updated
			items[i] = sessItem
			break
		}
	}
	m.list.SetItems(items)
}

func (m *Model) refreshTunnelStatesCmd(s domain.Session) tea.Cmd {
	return func() tea.Msg {
		remote, ok := resolveRemote(m.cfg, s)
		if !ok {
			return tunnelStatesMsg{sessionID: s.ID, err: fmt.Errorf("no remote configured for session")}
		}
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		items := make([]list.Item, 0, len(s.Tunnels))
		for _, t := range s.Tunnels {
			items = append(items, tunnelItem{
				tunnel:  t,
				running: mgr.IsTunnelRunning(ctx, t.LocalPort, t.RemotePort),
			})
		}
		return tunnelStatesMsg{sessionID: s.ID, items: items}
	}
}

func (m *Model) toggleTunnelCmd(s domain.Session, t domain.Tunnel, start bool) tea.Cmd {
	return func() tea.Msg {
		remote, ok := resolveRemote(m.cfg, s)
		if !ok {
			return tunnelToggleMsg{sessionID: s.ID, localPort: t.LocalPort, remotePort: t.RemotePort, err: fmt.Errorf("no remote configured for session")}
		}
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		var err error
		if start {
			err = mgr.StartTunnel(ctx, t.LocalPort, t.RemotePort)
		} else {
			err = mgr.StopTunnel(ctx, t.LocalPort, t.RemotePort)
		}
		return tunnelToggleMsg{
			sessionID:  s.ID,
			localPort:  t.LocalPort,
			remotePort: t.RemotePort,
			running:    start && err == nil,
			err:        err,
		}
	}
}

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
	return tea.Tick(500*time.Millisecond, func(t time.Time) tea.Msg {
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

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

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

// summariseSessionCmd captures the current tmux pane and sends it to the local SLM
// for analysis. Runs in a bubbletea goroutine — never blocks the TUI loop.
func summariseSessionCmd(cfg *config.Config, intel domain.IntelligenceProvider, session domain.Session) tea.Cmd {
	return func() tea.Msg {
		remote, ok := resolveRemote(cfg, session)
		if !ok {
			return aiSummaryMsg{session: session.TmuxSession, err: fmt.Errorf("no remote configured")}
		}
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		pane, err := mgr.CaptureTmuxPane(context.Background(), session.TmuxSession)
		if err != nil {
			return aiSummaryMsg{session: session.TmuxSession, err: fmt.Errorf("capture pane: %w", err)}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		summary, err := intel.SummariseBriefly(ctx, pane)
		return aiSummaryMsg{session: session.TmuxSession, summary: summary, err: err}
	}
}

// loadPriorSnapshotCmd looks up the latest snapshot for a session.
// The result is delivered as snapshotPreviewMsg so the UI can show a preview.
func loadPriorSnapshotCmd(snapMgr *usecase.SnapshotManager, sessionID string) tea.Cmd {
	return func() tea.Msg {
		if snapMgr == nil {
			return snapshotPreviewMsg{snapshot: nil}
		}
		snap, err := snapMgr.GetLatestSnapshot(context.Background(), sessionID)
		if err != nil || snap == nil {
			return snapshotPreviewMsg{snapshot: nil}
		}
		return snapshotPreviewMsg{snapshot: snap}
	}
}

// archivePreviewReadyMsg carries pre-computed archive preview data to the UI.
type archivePreviewReadyMsg struct {
	data *archivePreviewData
	err  error
}

// archiveStepLabels are the human-readable labels for each archive step shown in the progress view.
var archiveStepLabels = []string{
	"Connecting to remote host",
	"Capturing terminal session",
	"Cleaning & compressing output",
	"Generating AI summary",
	"Ready for review",
}

// loadArchivePreviewCmd captures the pane, cleans it, measures the compressed
// size, and asks the AI for a summary — but does NOT persist anything.
// It sends archiveStepMsg messages as each step completes so the progress
// view can update in real time.
func loadArchivePreviewCmd(cfg *config.Config, snapMgr *usecase.SnapshotManager, session domain.Session) tea.Cmd {
	return tea.Sequence(
		// Step 0 — resolve remote (instant, but we still tick it so the UI starts in the right state)
		func() tea.Msg { return archiveStepMsg{idx: 0} },
		func() tea.Msg {
			if snapMgr == nil {
				return archiveStepErrMsg{idx: 0, err: fmt.Errorf("snapshot manager unavailable")}
			}
			if _, ok := resolveRemote(cfg, session); !ok {
				return archiveStepErrMsg{idx: 0, err: fmt.Errorf("no remote configured for session")}
			}
			return archiveStepMsg{idx: 1} // step 0 done, start step 1
		},
		// Step 1 — capture pane
		func() tea.Msg {
			remote, _ := resolveRemote(cfg, session)
			sshMgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
			rawPane, err := sshMgr.CaptureTmuxPane(context.Background(), session.TmuxSession)
			if err != nil {
				return archiveStepErrMsg{idx: 1, err: fmt.Errorf("capture pane: %w", err)}
			}
			return archivePaneCapturedMsg{rawPane: rawPane, rawPaneLen: len(rawPane)}
		},
	)
}

// archivePaneCapturedMsg carries the raw pane forward in the sequence.
// The main Update uses this to fire the next steps.
type archivePaneCapturedMsg struct {
	rawPane    string
	rawPaneLen int // len(rawPane) captured before passing along
}

// loadArchivePreviewContinueCmd runs steps 2–4 (clean/compress + AI summary)
// after the pane has been captured. Called from the Update handler.
func loadArchivePreviewContinueCmd(cfg *config.Config, snapMgr *usecase.SnapshotManager, session domain.Session, rawPane string, rawPaneLen int) tea.Cmd {
	return tea.Sequence(
		func() tea.Msg { return archiveStepMsg{idx: 2} }, // cleaning starts
		func() tea.Msg {
			// Step 2 is fast (CPU-only), so advance to step 3 when done
			return archiveCleanedMsg{rawPane: rawPane, rawPaneLen: rawPaneLen}
		},
		func() tea.Msg { return archiveStepMsg{idx: 3} }, // AI summarise starts
		func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			snap, summary, compressedSize, err := snapMgr.PreviewSnapshot(ctx, &session, rawPane)
			if err != nil {
				return archiveStepErrMsg{idx: 3, err: err}
			}
			cleaned := pane.Clean(rawPane)
			head, tail := promptHeadTail(cleaned)
			return archivePreviewReadyMsg{
				data: &archivePreviewData{
					session:        session,
					summary:        summary,
					rawPane:        rawPane,
					rawPaneLen:     rawPaneLen,
					cleanedPaneLen: len(cleaned),
					compressedSize: compressedSize,
					cleanedPane:    cleaned,
					promptHead:     head,
					promptTail:     tail,
					snapshot:       snap,
				},
			}
		},
	)
}

// archiveCleanedMsg is an internal progress step — not displayed but used to sequence commands.
type archiveCleanedMsg struct {
	rawPane    string
	rawPaneLen int
}

// persistSnapshotCmd saves a pre-built snapshot to the database.
func persistSnapshotCmd(snapMgr *usecase.SnapshotManager, snap *domain.SessionSnapshot) tea.Cmd {
	return func() tea.Msg {
		if snapMgr == nil {
			return snapshotSavedMsg{err: fmt.Errorf("snapshot manager unavailable")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := snapMgr.PersistSnapshot(ctx, snap)
		if err != nil {
			return snapshotSavedMsg{err: err}
		}
		return snapshotSavedMsg{snapshot: snap}
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

	// Prefer the session's own remote host — this is the authoritative source for
	// existing sessions and enables multi-remote support.
	if session.RemoteHost != "" {
		for _, r := range cfg.Remotes {
			if r.Host == session.RemoteHost {
				return r, true
			}
		}
	}

	// Fallback for legacy sessions with empty RemoteHost: try ActiveRemote
	// (for backward compat with existing configs) then first configured remote.
	if cfg.ActiveRemote != "" {
		for _, r := range cfg.Remotes {
			if r.Host == cfg.ActiveRemote {
				return r, true
			}
		}
	}
	if len(cfg.Remotes) > 0 {
		return cfg.Remotes[0], true
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

type provisionProgressMsg struct {
	progress domain.ProvisionProgress
}

type provisionDoneMsg struct {
	err error
}

type authWizardStep struct {
	Name        string
	Scope       string // "local" or "remote"
	Instruction string
	CheckCmd    string
}

type authCheckDoneMsg struct {
	idx    int
	ok     bool
	output string
	err    error
}

type provisionConnectMsg struct {
	err error
}

type provisionStepDoneMsg struct {
	idx int
	err error
}

func (m *Model) provisionConnectCmd(remote config.Remote) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		return provisionConnectMsg{err: mgr.Connect(ctx)}
	}
}

func (m *Model) provisionStepCmd(remote config.Remote, idx int, step domain.ProvisionStep) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		_, err := mgr.Execute(ctx, step.Command)
		return provisionStepDoneMsg{idx: idx, err: err}
	}
}

func defaultAuthWizardSteps() []authWizardStep {
	return []authWizardStep{
		{
			Name:        "GitHub CLI (remote)",
			Scope:       "remote",
			Instruction: "Run `gh auth login` on the remote if this check fails.",
			CheckCmd:    "gh auth status >/dev/null 2>&1",
		},
		{
			Name:        "GitHub Copilot (remote)",
			Scope:       "remote",
			Instruction: "Run `gh copilot login` or `copilot login` on the remote if this check fails.",
			CheckCmd:    "if command -v copilot >/dev/null 2>&1; then copilot --help >/dev/null 2>&1; elif command -v gh >/dev/null 2>&1; then gh copilot -h >/dev/null 2>&1; else false; fi",
		},
		{
			Name:        "Claude Code (remote)",
			Scope:       "remote",
			Instruction: "Run `claude login` on the remote if this check fails.",
			CheckCmd:    "if command -v claude >/dev/null 2>&1; then claude --version >/dev/null 2>&1; else false; fi",
		},
		{
			Name:        "Gemini CLI (remote)",
			Scope:       "remote",
			Instruction: "Run `gemini auth login` on the remote if this check fails.",
			CheckCmd:    "if command -v gemini >/dev/null 2>&1; then gemini --help >/dev/null 2>&1; else false; fi",
		},
		{
			Name:        "JIRA token (local config)",
			Scope:       "local",
			Instruction: "Set JIRA URL/email/token in Aiman config if this check fails.",
			CheckCmd:    "test -n \"$HOME\"",
		},
	}
}

func (m *Model) authCheckCmd(remote config.Remote, idx int, step authWizardStep) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		if step.CheckCmd == "" {
			return authCheckDoneMsg{idx: idx, ok: false, err: fmt.Errorf("no automated check command available")}
		}

		if step.Name == "JIRA token (local config)" {
			ok := m.cfg.Integrations.Jira.URL != "" && m.cfg.Integrations.Jira.Email != "" && m.cfg.Integrations.Jira.APIToken != ""
			if ok {
				return authCheckDoneMsg{idx: idx, ok: true, output: "JIRA credentials are present in config."}
			}
			return authCheckDoneMsg{idx: idx, ok: false, err: fmt.Errorf("missing JIRA URL/email/token in config")}
		}

		if step.Scope == "local" {
			cmd := exec.CommandContext(ctx, "bash", "-lc", step.CheckCmd)
			out, err := cmd.CombinedOutput()
			if err != nil {
				return authCheckDoneMsg{idx: idx, ok: false, output: strings.TrimSpace(string(out)), err: err}
			}
			return authCheckDoneMsg{idx: idx, ok: true, output: strings.TrimSpace(string(out))}
		}

		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		out, err := mgr.Execute(ctx, step.CheckCmd)
		if err != nil {
			return authCheckDoneMsg{idx: idx, ok: false, output: out, err: err}
		}
		return authCheckDoneMsg{idx: idx, ok: true, output: strings.TrimSpace(out)}
	}
}

func (m *Model) provisionRemoteCmd(remote config.Remote) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()

		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		if err := mgr.Connect(ctx); err != nil {
			return provisionDoneMsg{err: err}
		}

		provisioner := usecase.NewProvisioner(mgr)
		progressChan := make(chan domain.ProvisionProgress)

		// Start provisioning in a goroutine
		go func() {
			_ = provisioner.Provision(ctx, progressChan)
			close(progressChan)
			// The final done message is handled separately or we could send it here
			// but we need to ensure we don't send to a closed program.
			// Bubble Tea handles this better via returning Cmds.
		}()

		// This approach is tricky with Bubble Tea's functional style.
		// We'll simplify: just run the steps and return results.
		// For a truly "streaming" UI, we'd need a more complex setup.
		// Let's do a sequence of commands for each step instead.
		return provisionDoneMsg{err: provisioner.Provision(ctx, nil)} // Simplified for now
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

type branchesMsg struct {
	branches []string
	err      error
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

type attachDoneMsg struct {
	err error
}

type terminateStepMsg struct {
	index int
	err   error
}

type recreateMutagenMsg struct {
	session domain.Session
	err     error
}

type refreshAWSMsg struct {
	err error
}

func (m *Model) refreshAWSCredentialsCmd(s domain.Session) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		remote, ok := resolveRemote(m.cfg, s)
		if !ok || remote.AWSDelegation == nil || !remote.AWSDelegation.SyncCredentials {
			return refreshAWSMsg{err: fmt.Errorf("no AWS delegation configured for remote %q", s.RemoteHost)}
		}
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		if err := mgr.Connect(ctx); err != nil {
			return refreshAWSMsg{err: fmt.Errorf("SSH connect: %w", err)}
		}
		d := remote.AWSDelegation
		cfg := &domain.AWSConfig{
			SourceProfile:   d.SourceProfile,
			RoleName:        d.RoleName,
			AccountID:       d.AccountID,
			Region:          d.Region,
			Regions:         d.Regions,
			SessionPolicy:   d.SessionPolicy,
			DurationSeconds: d.DurationSeconds,
		}
		_, err := usecase.PushSessionAWSCredentials(ctx, mgr, s.ID, cfg)
		return refreshAWSMsg{err: err}
	}
}

func (m *Model) recreateMutagenSync(s domain.Session) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()

		remote, ok := resolveRemote(m.cfg, s)
		if !ok {
			return recreateMutagenMsg{err: fmt.Errorf("no remote configured")}
		}
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})

		// Use persisted WorkingDirectory if available, otherwise try to fetch from tmux or fallback to worktree
		remoteSyncDir := s.WorkingDirectory
		if remoteSyncDir == "" && s.TmuxSession != "" {
			fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			if cwd, err := mgr.GetTmuxSessionCWD(fetchCtx, s.TmuxSession); err == nil && strings.TrimSpace(cwd) != "" {
				remoteSyncDir = strings.TrimSpace(cwd)
			}
			cancel()
		}
		if remoteSyncDir == "" {
			remoteSyncDir = s.WorktreePath
		}
		if remoteSyncDir == "" {
			return recreateMutagenMsg{err: fmt.Errorf("session has no remote working directory (WorkingDirectory, WorktreePath, and tmux CWD are all empty)")}
		}

		target := remote.Host
		if remote.User != "" {
			target = fmt.Sprintf("%s@%s", remote.User, remote.Host)
		}

		tmuxName := s.TmuxSession
		if tmuxName == "" {
			tmuxName = filepath.Base(s.WorktreePath)
		}

		s.ID = strings.TrimSpace(s.ID)
		if s.ID == "" {
			return recreateMutagenMsg{err: fmt.Errorf("session ID is empty (%q), cannot safely create sync path", s.ID)}
		}

		syncName := "aiman-sync-" + s.ID
		m.log("Recreating sync %q", syncName)
		home, _ := os.UserHomeDir()
		localPath := filepath.Join(home, config.DirName, "work", s.ID)

		m.log("Cleaning up local sync path: %s", localPath)
		_ = os.RemoveAll(localPath)
		if err := os.MkdirAll(localPath, 0755); err != nil {
			m.log("Warning: failed to create local sync path: %v", err)
		}

		m.log("Terminating existing sync: %s", syncName)
		_ = exec.CommandContext(ctx, "mutagen", "sync", "terminate", syncName).Run()

		terminateCandidates := []string{
			s.MutagenSyncID,
			s.TmuxSession,
			filepath.Base(s.LocalPath),
			tmuxName,
		}
		terminated := map[string]bool{syncName: true}
		for _, candidate := range terminateCandidates {
			if candidate == "" || terminated[candidate] {
				continue
			}
			terminated[candidate] = true
			_, _ = exec.CommandContext(ctx, "mutagen", "sync", "terminate", candidate).CombinedOutput()
		}

		mutagenEngine := mutagen.NewEngine()
		remoteSyncPath := fmt.Sprintf("%s:%s", target, remoteSyncDir)
		labels := map[string]string{"aiman-id": s.ID}
		if err := mutagenEngine.StartSync(ctx, syncName, localPath, remoteSyncPath, labels); err != nil {
			return recreateMutagenMsg{err: fmt.Errorf("failed to recreate mutagen sync: %w", err)}
		}

		s.LocalPath = localPath
		s.WorkingDirectory = remoteSyncDir
		s.MutagenSyncID = syncName
		return recreateMutagenMsg{session: s}
	}
}

func (m *Model) fetchBranches(repo domain.Repo) tea.Cmd {
	remote := m.selectedRemote
	return func() tea.Msg {
		ctx := context.Background()

		if remote.Host == "" {
			return branchesMsg{err: fmt.Errorf("no remote selected")}
		}

		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		if err := mgr.Connect(ctx); err != nil {
			return branchesMsg{err: fmt.Errorf("failed to connect to remote: %w", err)}
		}
		defer mgr.Close()

		gitMgr := git.NewManager(&m.cfg.Git)
		branches, err := gitMgr.ListRemoteBranches(ctx, mgr, repo)
		return branchesMsg{branches: branches, err: err}
	}
}

func (m *Model) fetchDirectories(basePath string) tea.Cmd {
	remote := m.selectedRemote
	return func() tea.Msg {
		ctx := context.Background()

		if remote.Host == "" {
			return dirsMsg{err: fmt.Errorf("no remote selected")}
		}

		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})

		// Scan directories up to depth 3
		dirs, err := mgr.ScanDirectories(ctx, basePath, 3)
		if err != nil {
			return dirsMsg{err: err}
		}

		// Always add root as an option
		dirs = append([]string{"."}, dirs...)

		return dirsMsg{dirs: dirs, err: nil}
	}
}

func (m *Model) fetchRepoDirectories(repo *domain.Repo) tea.Cmd {
	remote := m.selectedRemote
	return func() tea.Msg {
		ctx := context.Background()

		if remote.Host == "" {
			return dirsMsg{err: fmt.Errorf("no remote selected")}
		}

		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		if err := mgr.Connect(ctx); err != nil {
			return dirsMsg{err: fmt.Errorf("failed to connect to remote: %w", err)}
		}

		var repoPath string
		if repo != nil && repo.URL != "" {
			// Extract just the repo name (not org/repo)
			repoName := extractRepoName(repo.Name)

			// Handle case where remote.Root might already end with the repo name
			cleanRoot := strings.TrimRight(remote.Root, "/")
			if strings.HasSuffix(cleanRoot, "/"+repoName) || cleanRoot == repoName {
				repoPath = cleanRoot
			} else {
				repoPath = fmt.Sprintf("%s/%s", cleanRoot, repoName)
			}

			// Distinguish "missing repo" from connectivity/auth failures.
			existsOut, existsErr := mgr.Execute(ctx, fmt.Sprintf("if test -d %q; then echo EXISTS; else echo MISSING; fi", repoPath))
			if existsErr != nil {
				return dirsMsg{err: fmt.Errorf("failed to check remote repository path: %w", existsErr)}
			}
			if strings.TrimSpace(existsOut) == "MISSING" {
				_, cloneErr := mgr.Execute(ctx, fmt.Sprintf("cd %q && git clone %q %q", remote.Root, repo.URL, repoName))
				if cloneErr != nil {
					return dirsMsg{err: fmt.Errorf("failed to clone repository: %w", cloneErr)}
				}
			}
		} else {
			// No repository, scan from remote root
			repoPath = remote.Root
		}

		return m.fetchDirectories(repoPath)()
	}
}

func (m *Model) fetchAgents() tea.Cmd {
	remote := m.selectedRemote
	return func() tea.Msg {
		if remote.Host == "" {
			return agent.ScanAgentsMsg{Err: fmt.Errorf("no remote selected")}
		}

		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		scanner := agent.NewScanner(mgr)
		return agent.ScanCmd(scanner)()
	}
}

func (m *Model) createSession() tea.Cmd {
	// Resolve the active remote at dispatch time (before goroutine runs)
	sessionCfg := m.sessionCfg
	remote := m.selectedRemote
	if remote.Host != "" {
		sessionCfg.SSHManager = ssh.NewManager(ssh.Config{
			Host: remote.Host,
			User: remote.User,
			Root: remote.Root,
		})
		sessionCfg.RemoteHost = remote.Host
	}

	return func() tea.Msg {
		ctx := context.Background()

		// Use FlowManager to create the session
		session, err := m.flowManager.CreateSession(ctx, sessionCfg)
		if err != nil {
			return sessionCreateMsg{err: err}
		}

		session.ID = strings.TrimSpace(session.ID)
		if session.ID == "" {
			return sessionCreateMsg{err: fmt.Errorf("session ID is empty (%q), cannot safely create sync path", session.ID)}
		}

		// Tag the session with the remote it was created on
		session.RemoteHost = sessionCfg.RemoteHost

		// Start mutagen sync
		mutagenEngine := mutagen.NewEngine()
		home, _ := os.UserHomeDir()

		syncName := "aiman-sync-" + session.ID
		m.log("Creating sync %q", syncName)
		localSyncPath := filepath.Join(home, config.DirName, "work", session.ID)

		m.log("Cleaning up local sync path: %s", localSyncPath)
		_ = os.RemoveAll(localSyncPath)
		if err := os.MkdirAll(localSyncPath, 0755); err != nil {
			m.log("Warning: failed to create local sync path: %v", err)
		}

		target := remote.Host
		if remote.User != "" {
			target = fmt.Sprintf("%s@%s", remote.User, remote.Host)
		}

		m.log("Terminating existing sync: %s", syncName)
		_ = exec.CommandContext(ctx, "mutagen", "sync", "terminate", syncName).Run()

		m.log("Starting mutagen sync: %s -> %s:%s", localSyncPath, target, session.WorkingDirectory)
		labels := map[string]string{"aiman-id": session.ID}
		syncErr := mutagenEngine.StartSync(ctx, syncName, localSyncPath, fmt.Sprintf("%s:%s", target, session.WorkingDirectory), labels)
		if syncErr == nil {
			session.MutagenSyncID = syncName
			session.LocalPath = localSyncPath
			_ = session.Transition(domain.SessionStatusSyncing)
		} else {
			m.log("Warning: failed to start mutagen sync: %v", syncErr)
		}

		// Save to DB
		if m.db != nil {
			_ = m.db.Save(ctx, session)
		}

		return sessionCreateMsg{session: *session}
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
		// Build a list of candidate names to try. The canonical name is
		// "aiman-sync-{session-id}" but older sessions or DB rows may store
		// the mutagen UUID, tmux session name, or nothing at all.
		var candidates []string
		if s.ID != "" {
			candidates = append(candidates, "aiman-sync-"+s.ID)
		}
		if s.MutagenSyncID != "" {
			candidates = append(candidates, s.MutagenSyncID)
		}
		if s.TmuxSession != "" {
			candidates = append(candidates, s.TmuxSession)
		}
		if s.LocalPath != "" {
			candidates = append(candidates, filepath.Base(s.LocalPath))
		}

		tried := map[string]bool{}
		for _, name := range candidates {
			if name == "" || tried[name] {
				continue
			}
			tried[name] = true
			cmd := exec.CommandContext(ctx, "mutagen", "sync", "terminate", name) // #nosec G204
			if _, err := cmd.CombinedOutput(); err == nil {
				return nil // successfully terminated
			}
		}
		// Also try terminating by label if we have a session ID.
		if s.ID != "" {
			cmd := exec.CommandContext(ctx, "mutagen", "sync", "terminate", "--label-selector", "aiman-id="+s.ID) // #nosec G204
			if _, err := cmd.CombinedOutput(); err == nil {
				return nil
			}
		}
		// Not finding a sync is fine — it may have been cleaned up already.
		return nil
	}

	effectiveIndex := index
	if m.terminateForced {
		if index == 1 {
			// Forced discard
			if s.WorktreePath == "" {
				return nil
			}
			remote, ok := resolveRemote(m.cfg, s)
			if !ok {
				return fmt.Errorf("no remote configured")
			}
			mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
			_, err := mgr.Execute(ctx, fmt.Sprintf("bash -c 'git -C %q reset --hard HEAD && git -C %q clean -fd'", s.WorktreePath, s.WorktreePath))
			return err
		}
		effectiveIndex--
	}

	switch effectiveIndex {
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
		if s.WorktreePath == "" || s.RepoName == "" {
			// Ad-hoc sessions have no worktree; skip removal.
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
	case 5: // Clean up session-scoped AWS credentials from the remote
		if s.AWSProfileName == "" || s.RemoteHost == "" {
			return nil
		}
		remote, ok := resolveRemote(m.cfg, s)
		if !ok {
			return nil
		}
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		return awsdelegation.RemoveSessionProfile(ctx, mgr, s.AWSProfileName)
	case 6: // Delete session from database
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
		tea.EnableMouseCellMotion,
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

// wrapLines wraps each log line to width and joins them with newlines.
func wrapLines(lines []string, width int) string {
	if width <= 0 {
		return strings.Join(lines, "\n")
	}
	var sb strings.Builder
	for i, line := range lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		for len(line) > width {
			// Try to break at the last space before width
			cut := width
			if idx := strings.LastIndex(line[:width], " "); idx > 0 {
				cut = idx + 1
			}
			sb.WriteString(line[:cut])
			sb.WriteByte('\n')
			line = line[cut:]
		}
		sb.WriteString(line)
	}
	return sb.String()
}

func (m *Model) renderWithConsole(baseView string) string {
	consoleHeight := m.height / 3
	if consoleHeight < 5 {
		consoleHeight = 5
	}
	if consoleHeight > 20 {
		consoleHeight = 20
	}

	// Update viewport content without losing scroll position.
	// If already at the bottom, follow new content; otherwise stay put.
	atBottom := m.consoleViewport.AtBottom()
	yOffset := m.consoleViewport.YOffset
	m.consoleViewport.SetContent(wrapLines(m.consoleLog, m.consoleViewport.Width))
	if atBottom {
		m.consoleViewport.GotoBottom()
	} else {
		m.consoleViewport.SetYOffset(yOffset)
	}

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
	m.remotes.list.SetSize(width-h-4, height-v-6)
	m.remotes.width = width
	m.remotes.height = height

	// Viewport takes up the bottom part of the main panel
	m.viewport.Width = width - (width / 3) - h - 4
	// Compact stacked session/git strip (~6–9 lines) + thin preview chrome; rest for tmux/terminal.
	const compactMainUpperBudget = 12
	m.viewport.Height = max(6, mainHeight-compactMainUpperBudget)

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

	// Global viewState handling (e.g. from tea.Tick)
	if s, ok := msg.(viewState); ok {
		m.state = s
		return m, nil
	}

	// Archive progress messages are handled globally so they reach the handler
	// regardless of which sub-state is active during the pipeline.
	switch msg := msg.(type) {
	case archiveStepMsg:
		if msg.idx < len(m.archiveSteps) {
			if msg.idx > 0 {
				m.archiveSteps[msg.idx-1].done = true
			}
			m.archiveStepIdx = msg.idx
		}
		return m, nil
	case archiveStepErrMsg:
		if msg.idx < len(m.archiveSteps) {
			m.archiveSteps[msg.idx].err = true
		}
		m.snapshotToast = "❌ Archive failed: " + msg.err.Error()
		m.snapshotToastError = true
		m.state = viewStateMain
		return m, nil
	case archivePaneCapturedMsg:
		if 1 < len(m.archiveSteps) {
			m.archiveSteps[0].done = true
			m.archiveStepIdx = 2
		}
		if m.archivePreview != nil {
			sess := m.archivePreview.session
			return m, loadArchivePreviewContinueCmd(m.cfg, m.snapshotManager, sess, msg.rawPane, msg.rawPaneLen)
		}
		return m, nil
	case archiveCleanedMsg:
		if 2 < len(m.archiveSteps) {
			m.archiveSteps[1].done = true
			m.archiveStepIdx = 3
		}
		return m, nil
	case archivePreviewReadyMsg:
		if msg.err != nil {
			m.snapshotToast = "❌ Archive failed: " + msg.err.Error()
			m.snapshotToastError = true
			m.state = viewStateMain
			return m, nil
		}
		for i := range m.archiveSteps {
			m.archiveSteps[i].done = true
		}
		m.archivePreview = msg.data
		dialogW := 76
		inner := dialogW - 6
		vpH := max(5, m.height-16) // 16 = border+padding+fixed header+footer lines
		m.archivePreviewVP = viewport.New(inner, vpH)
		m.archivePreviewVP.SetContent(buildArchivePreviewBody(msg.data, inner))
		m.state = viewStateArchivePreview
		return m, nil
	case snapshotPreviewMsg:
		// Handled globally so it works regardless of which state dispatched loadPriorSnapshotCmd.
		if m.restartingSession == nil {
			return m, nil
		}
		if msg.snapshot != nil {
			m.priorSnapshotCandidate = msg.snapshot
			m.state = viewStateSnapshotPreview
		} else {
			m.loadingMsg = fmt.Sprintf("Restarting session %s...", m.restartingSession.TmuxSession)
			m.loadingNext = viewStateMain
			m.state = viewStateLoading
			return m, m.restartSession()
		}
		return m, nil
	case refreshAWSMsg:
		if msg.err != nil {
			m.lastError = fmt.Sprintf("AWS credentials refresh failed: %v", msg.err)
			m.state = viewStateError
		} else {
			m.state = viewStateMain
		}
		return m, nil
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

	case viewStateAISettings:
		return m.handleAISetupUpdate(msg)

	case viewStateSnapshotBrowser:
		return m.handleSnapshotBrowserUpdate(msg)

	case viewStateVSCodeError, viewStateError:
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

	case viewStateSnapshotPreview:
		return m.handleSnapshotPreviewUpdate(msg)

	case viewStateArchivePreview:
		return m.handleArchivePreviewUpdate(msg)

	case viewStateArchiveProgress:
		return m.handleArchiveProgressUpdate(msg)

	case viewStateChangeDirPicker:
		return m.handleChangeDirPickerUpdate(msg)

	case viewStateChangeDirConfirm:
		return m.handleChangeDirConfirmUpdate(msg)

	case viewStateSummary:
		return m.handleSummaryUpdate(msg)

	case viewStateTerminateConfirm:
		return m.handleTerminateConfirmUpdate(msg)

	case viewStateWorktreeExists:
		return m.handleWorktreeExistsUpdate(msg)

	case viewStateBranchPicker:
		return m.handleBranchPickerUpdate(msg)

	case viewStateRemotePicker:
		return m.handleRemotePickerUpdate(msg)

	case viewStateModePicker:
		return m.handleModePickerUpdate(msg)

	case viewStateTerminateProgress:
		return m.handleTerminateProgressUpdate(msg)

	case viewStateProvisioningRemotePicker:
		return m.handleProvisioningRemotePickerUpdate(msg)

	case viewStateProvisioningProgress:
		return m.handleProvisioningProgressUpdate(msg)

	case viewStateAuthRemotePicker:
		return m.handleAuthRemotePickerUpdate(msg)

	case viewStateAuthWizard:
		return m.handleAuthWizardUpdate(msg)

	case viewStateTunnelManager:
		return m.handleTunnelManagerUpdate(msg)

	case viewStateTunnelAdd:
		return m.handleTunnelAddUpdate(msg)

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

	case viewStateAISettings:
		return m.aiSetup.View()

	case viewStateSnapshotBrowser:
		return docStyle.Render(m.snapshotBrowser.View())

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

	case viewStateProvisioningRemotePicker:
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
			m.remotes.list.View())

	case viewStateAuthRemotePicker:
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
			m.remotes.list.View())

	case viewStateProvisioningProgress:
		var b strings.Builder
		b.WriteString(titleStyle.Render("Provisioning Remote Server: "+m.selectedRemote.Host) + "\n\n")

		if m.provisioningStatus != "" {
			statusLine := m.provisioningStatus
			if m.provisioningError == "" && m.provisioningIdx < len(m.provisionSteps) {
				statusLine = fmt.Sprintf("%s %s", m.provisionSpinner.View(), statusLine)
			}
			b.WriteString(statusStyle.Render(statusLine) + "\n\n")
		}

		for i, step := range m.provisionSteps {
			status := "○" // Pending
			style := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

			if m.provisioningIdx >= 0 && i < m.provisioningIdx {
				status = "✔" // Success
				style = successStyle
			} else if m.provisioningIdx >= 0 && i == m.provisioningIdx {
				if m.provisioningError != "" {
					status = "✘" // Error
					style = failStyle
				} else {
					status = "●" // Running
					style = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
				}
			}

			b.WriteString(style.Render(fmt.Sprintf("%s %s", status, step.Name)) + "\n")
		}

		if m.provisioningError != "" {
			b.WriteString("\n" + failStyle.Render("Error: "+m.provisioningError) + "\n")
			b.WriteString("\nPress esc to return.")
		} else if m.provisioningIdx >= len(m.provisionSteps) {
			b.WriteString("\n" + successStyle.Render("Provisioning Complete!") + "\n")
			b.WriteString("\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("Note: Run 'gh auth login' manually in your first session."))
		}

		dialog := lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).
			Padding(1, 4).
			Width(60)

		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(b.String()))

	case viewStateAuthWizard:
		var b strings.Builder
		title := "Auth Setup Wizard"
		if m.selectedRemote.Host != "" {
			title = fmt.Sprintf("Auth Setup Wizard: %s@%s", m.selectedRemote.User, m.selectedRemote.Host)
		}
		b.WriteString(titleStyle.Render(title) + "\n\n")
		if m.authStatusMsg != "" {
			line := m.authStatusMsg
			if m.authChecking {
				line = fmt.Sprintf("%s %s", m.provisionSpinner.View(), line)
			}
			b.WriteString(statusStyle.Render(line) + "\n\n")
		}

		for i, step := range m.authSteps {
			marker := "○"
			style := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
			switch m.authStepStatus[i] {
			case "ok":
				marker = "✔"
				style = successStyle
			case "manual":
				marker = "◐"
				style = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
			case "fail":
				marker = "✘"
				style = failStyle
			}
			prefix := "  "
			if i == m.authStepIdx {
				prefix = "> "
				style = style.Bold(true)
			}
			scope := strings.ToUpper(step.Scope)
			b.WriteString(style.Render(fmt.Sprintf("%s%s %s (%s)", prefix, marker, step.Name, scope)) + "\n")
		}

		if len(m.authSteps) > 0 && m.authStepIdx >= 0 && m.authStepIdx < len(m.authSteps) {
			step := m.authSteps[m.authStepIdx]
			b.WriteString("\n" + activeStyle.Render("Instruction:") + " " + step.Instruction + "\n")
			if d := strings.TrimSpace(m.authStepDetails[m.authStepIdx]); d != "" {
				b.WriteString(statusStyle.Render("Detail: "+d) + "\n")
			}
		}
		b.WriteString("\n" + statusStyle.Render("Keys: c=check  m=mark done  ↑/↓=select  esc=back") + "\n")

		dialog := lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).
			Padding(1, 3).
			Width(90)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(b.String()))

	case viewStateTunnelManager:
		var b strings.Builder
		title := "Session Tunnels"
		if m.tunnelSession != nil {
			title = fmt.Sprintf("Session Tunnels: %s", m.tunnelSession.TmuxSession)
		}
		b.WriteString(titleStyle.Render(title) + "\n\n")
		if m.tunnelError != "" {
			b.WriteString(failStyle.Render(m.tunnelError) + "\n\n")
		}
		if len(m.tunnelList.Items()) == 0 {
			b.WriteString(statusStyle.Render("No tunnels configured for this session yet.") + "\n\n")
		} else {
			b.WriteString(m.tunnelList.View() + "\n")
		}
		b.WriteString(statusStyle.Render("Keys: a=add  enter=toggle start/stop  d=delete  r=refresh  esc=back"))
		dialog := lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).
			Padding(1, 2).
			Width(90)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(b.String()))

	case viewStateTunnelAdd:
		var b strings.Builder
		b.WriteString(activeStyle.Render("Add Session Tunnel") + "\n\n")
		b.WriteString("Enter local:remote ports (example: 5173:5173)\n\n")
		b.WriteString(m.tunnelInput.View() + "\n\n")
		if m.tunnelError != "" {
			b.WriteString(failStyle.Render(m.tunnelError) + "\n\n")
		}
		b.WriteString(statusStyle.Render("enter=save  esc=cancel"))
		dialog := lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("62")).
			Padding(1, 2).
			Width(72)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(b.String()))

	case viewStateError:
		var b strings.Builder
		b.WriteString(failStyle.Render("Error") + "\n\n")
		b.WriteString(m.lastError + "\n\n")
		b.WriteString("Press any key to return.")

		dialog := lipgloss.NewStyle().
			Border(lipgloss.DoubleBorder()).
			BorderForeground(lipgloss.Color("196")).
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

	case viewStateBranchPicker:
		return docStyle.Render(m.branchPicker.View())

	case viewStateRemotePicker:
		var b strings.Builder
		b.WriteString(activeStyle.Render("Select Remote Server") + "\n\n")
		for i, r := range m.cfg.Remotes {
			label := r.Name
			if label == "" {
				label = r.Host
			}
			b.WriteString(fmt.Sprintf("  %s  %s (%s@%s)\n", activeStyle.Render(fmt.Sprintf("[%d]", i+1)), label, r.User, r.Host))
		}
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("  esc: cancel"))

		dialog := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("205")).
			Padding(1, 2).
			Width(60)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(b.String()))

	case viewStateModePicker:
		var b strings.Builder
		b.WriteString(activeStyle.Render("New Session") + "\n\n")
		if m.selectedRemote.Host != "" {
			remoteLabel := m.selectedRemote.Name
			if remoteLabel == "" {
				remoteLabel = m.selectedRemote.Host
			}
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("Remote: ") +
				lipgloss.NewStyle().Foreground(lipgloss.Color("33")).Render(remoteLabel) + "\n\n")
		}
		b.WriteString("How would you like to start?\n\n")
		b.WriteString(activeStyle.Render("[1]") + "  From JIRA Issue      — link session to a JIRA ticket\n")
		b.WriteString(activeStyle.Render("[2]") + "  New Branch           — start with a custom branch name\n")
		b.WriteString(activeStyle.Render("[3]") + "  Existing Branch      — check out an existing remote branch\n")
		b.WriteString(activeStyle.Render("[4]") + "  Ad-hoc               — no git repo, no JIRA ticket\n")
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("esc: cancel"))
		style := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(2, 4).
			Width(62)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, style.Render(b.String()))

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

	case viewStateSnapshotPreview:
		snap := m.priorSnapshotCandidate
		dialogW := 72
		var b strings.Builder
		b.WriteString(activeStyle.Render("📸 Prior Session Snapshot") + "\n\n")
		if snap != nil {
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(
				fmt.Sprintf("Captured %s · %s", snap.CreatedAt.Format("2006-01-02 15:04"), snap.AgentName),
			) + "\n\n")
			if snap.Summary != "" {
				b.WriteString(activeStyle.Render("What was done") + "\n")
				wrapped := lipgloss.NewStyle().Width(dialogW - 6).Render(snap.Summary)
				for _, l := range strings.Split(wrapped, "\n") {
					b.WriteString("  " + l + "\n")
				}
				b.WriteString("\n")
			}
			if len(snap.NextSteps) > 0 {
				b.WriteString(activeStyle.Render("Next steps") + "\n")
				for _, s := range snap.NextSteps {
					wrapped := lipgloss.NewStyle().Width(dialogW - 8).Render(s)
					lines := strings.Split(wrapped, "\n")
					b.WriteString("  • " + lines[0] + "\n")
					for _, l := range lines[1:] {
						b.WriteString("    " + l + "\n")
					}
				}
				b.WriteString("\n")
			}
			if snap.InjectedAt != nil {
				b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(
					fmt.Sprintf("Previously injected %s", snap.InjectedAt.Format("2006-01-02 15:04")),
				) + "\n\n")
			}
		}
		b.WriteString("Inject this context into the restarted session?\n\n")
		b.WriteString(activeStyle.Render("[y]") + " Yes, inject context  " +
			activeStyle.Render("[n]") + " Restart fresh  " +
			activeStyle.Render("[esc]") + " Cancel")

		dialog2 := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(1, 3).
			Width(dialogW)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog2.Render(b.String()))

	case viewStateChangeDirPicker:
		return docStyle.Render(m.dirPicker.View())

	case viewStateArchivePreview:
		p := m.archivePreview
		dialogW := 76
		inner := dialogW - 6
		muted := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

		var header strings.Builder
		header.WriteString(activeStyle.Render("📦 Archive Session") + "\n\n")
		if p != nil {
			meta := fmt.Sprintf("%s  ·  %s  ·  %s", p.session.TmuxSession, p.session.RepoName, p.session.AgentName)
			if p.session.Branch != "" {
				meta += "  ·  " + p.session.Branch
			}
			if p.session.WorktreePath != "" {
				meta += "  ·  " + p.session.WorktreePath
			}
			header.WriteString(muted.Render(meta) + "\n")
		}
		header.WriteString(muted.Render(strings.Repeat("─", inner)) + "\n")

		scrollPct := ""
		if m.archivePreviewVP.TotalLineCount() > m.archivePreviewVP.Height {
			pct := int(m.archivePreviewVP.ScrollPercent() * 100)
			scrollPct = muted.Render(fmt.Sprintf(" (%d%%)", pct))
		}

		var footer strings.Builder
		footer.WriteString(muted.Render(strings.Repeat("─", inner)) + "\n")
		if p != nil {
			var sizeStr string
			switch {
			case p.compressedSize >= 1024*1024:
				sizeStr = fmt.Sprintf("%.1f MB", float64(p.compressedSize)/1024/1024)
			case p.compressedSize >= 1024:
				sizeStr = fmt.Sprintf("%.1f KB", float64(p.compressedSize)/1024)
			default:
				sizeStr = fmt.Sprintf("%d B", p.compressedSize)
			}
			rawKB := float64(p.rawPaneLen) / 1024
			cleanedKB := float64(p.cleanedPaneLen) / 1024
			footer.WriteString(muted.Render(fmt.Sprintf(
				"%.1f KB raw  →  %.1f KB cleaned  →  %s compressed",
				rawKB, cleanedKB, sizeStr,
			)) + "\n\n")
		}
		footer.WriteString("Save this snapshot to the archive?\n\n")
		footer.WriteString(activeStyle.Render("[enter]") + " Save  " +
			activeStyle.Render("[esc]") + " Cancel" +
			"  " + muted.Render("↑↓/pgup/pgdn: scroll") + scrollPct)

		body := header.String() + m.archivePreviewVP.View() + "\n" + footer.String()
		dialog := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(1, 3).
			Width(dialogW)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(body))

	case viewStateArchiveProgress:
		dialogW := 60
		var b strings.Builder
		b.WriteString(activeStyle.Render("📦 Archiving Session") + "\n\n")
		if m.archivePreview != nil {
			muted := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
			b.WriteString(muted.Render(fmt.Sprintf(
				"%s  ·  %s", m.archivePreview.session.TmuxSession, m.archivePreview.session.RepoName,
			)) + "\n\n")
		}
		checkStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
		errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
		for i, step := range m.archiveSteps {
			var prefix string
			switch {
			case step.err:
				prefix = errStyle.Render("✗")
			case step.done:
				prefix = checkStyle.Render("✓")
			case i == m.archiveStepIdx:
				prefix = m.provisionSpinner.View()
			default:
				prefix = "○"
			}
			label := step.label
			if i == m.archiveStepIdx && !step.done && !step.err {
				label = lipgloss.NewStyle().Bold(true).Render(label)
			} else if step.done {
				label = lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(label)
			}
			b.WriteString(fmt.Sprintf("  %s  %s\n", prefix, label))
		}
		b.WriteString("\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("esc to cancel"))

		dialog2 := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(1, 3).
			Width(dialogW)
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog2.Render(b.String()))

	case viewStateChangeDirConfirm:
		var b strings.Builder
		b.WriteString(activeStyle.Render("Confirm Scope Change") + "\n\n")
		b.WriteString(fmt.Sprintf("Session: %s\n", m.changingDirSession.TmuxSession))
		b.WriteString(fmt.Sprintf("New Scope: %s\n\n", m.sessionCfg.Directory))
		b.WriteString("Changing the scope requires restarting the Mutagen sync and the agent.\n")
		b.WriteString("The agent will be restarted in the new subdirectory.\n\n")
		b.WriteString("Do you want to proceed?\n\n")
		b.WriteString(activeStyle.Render("[y]") + " Yes, Restart  " + activeStyle.Render("[n]") + " Cancel")

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
			b.WriteString(activeStyle.Render("[y]") + " Confirm  " + activeStyle.Render("[f]") + " Force (discard changes)  " + activeStyle.Render("[n]") + " Cancel")

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
		if m.sessionCfg.ExistingBranch {
			b.WriteString(activeStyle.Render("[b]") + " Pick a Different Branch\n")
		} else {
			b.WriteString(activeStyle.Render("[b]") + " Change Branch Name\n")
		}
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
			return attachDoneMsg{err: err}
		})
	case attachDoneMsg:
		if msg.err != nil {
			m.lastError = fmt.Sprintf("Failed to attach to tmux session: %v", msg.err)
			m.state = viewStateError
			return m, nil
		}
		m.state = viewStateMain
		m.panelMode = panelModePreview
		if sel := m.list.SelectedItem(); sel != nil {
			s := sel.(item).session
			m.activeSession = s.TmuxSession
			m.tmuxOutput = "Loading..."
			m.viewport.SetContent(m.tmuxOutput)
			cmds = append(cmds, tickTmux(), fetchTmuxPane(m.cfg, s))
		}
		return m, tea.Batch(cmds...)
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
			// Git/PR refresh is on a 30s ticker (gitTickMsg) and on session change — not every tmux poll.
			cmds = append(cmds,
				fetchTmuxPane(m.cfg, s),
				checkInputHint(m.cfg, s),
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
			var newOutput string
			if msg.err != nil {
				newOutput = failStyle.Render("Failed to capture pane: " + msg.err.Error())
			} else {
				newOutput = msg.output
			}

			// Sticky scroll: only go to bottom if we were already at the bottom OR if it's the first load for this session.
			wasAtBottom := m.viewport.AtBottom()
			yOffset := m.viewport.YOffset
			isFirstLoad := !m.firstLoad[msg.session] && newOutput != "Loading..." && msg.err == nil
			if isFirstLoad {
				wasAtBottom = true
				m.firstLoad[msg.session] = true
			}

			// Only update viewport content when it actually changed.
			if newOutput != m.tmuxOutput || isFirstLoad {
				m.tmuxOutput = newOutput
				m.viewport.SetContent(m.tmuxOutput)
				if wasAtBottom {
					m.viewport.GotoBottom()
				} else {
					m.viewport.SetYOffset(yOffset)
				}
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
	case aiSummaryMsg:
		m.aiLoading = false
		if msg.err != nil {
			if errors.Is(msg.err, domain.ErrIntelligenceUnavailable) {
				m.aiError = "AI unavailable — enable in config and ensure Ollama is running (brew install ollama)"
			} else {
				m.aiError = fmt.Sprintf("AI error: %v", msg.err)
			}
		} else if msg.summary != nil && msg.session == m.activeSession {
			m.aiSummary[msg.session] = msg.summary
			m.aiError = ""
		}
	case snapshotSavedMsg:
		if msg.err != nil {
			m.snapshotToast = fmt.Sprintf("❌ Snapshot failed: %v", msg.err)
			m.snapshotToastError = true
		} else {
			m.snapshotToast = "📸 Snapshot saved"
			m.snapshotToastError = false
		}
		// Clear toast after a short delay
		return m, tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
			return snapshotToastMsg{text: "", isError: false}
		})
	case snapshotToastMsg:
		m.snapshotToast = msg.text
		m.snapshotToastError = msg.isError
	case snapshotBrowserLoadedMsg:
		var sub tea.Model
		var loadCmd tea.Cmd
		sub, loadCmd = m.snapshotBrowser.Update(msg)
		m.snapshotBrowser = sub.(SnapshotBrowserModel)
		return m, loadCmd
	case archivePreviewReadyMsg:
		// Handled globally in Update() before state dispatch — should not reach here.
		return m, nil
	case archiveStepMsg:
		return m, nil
	case archiveStepErrMsg:
		return m, nil
	case archivePaneCapturedMsg:
		return m, nil
	case archiveCleanedMsg:
		return m, nil
	case tea.KeyMsg:
		m, cmd, handled := m.handleMainKeyMsg(msg)
		if handled {
			return m, cmd
		}
	}

	// Capture list selection changes to trigger immediate fetch
	oldSelID := ""
	if oldSel, ok := m.list.SelectedItem().(item); ok {
		oldSelID = oldSel.session.ID
	}
	var cmd tea.Cmd

	// If it's a mouse event, only forward to the component under the cursor
	if mouseMsg, ok := msg.(tea.MouseMsg); ok {
		m.log("Mouse X: %d, Y: %d, Type: %v, Width: %d", mouseMsg.X, mouseMsg.Y, mouseMsg.Type, m.width)
		if mouseMsg.X < (m.width/3 + 4) {
			m.list, cmd = m.list.Update(msg)
			cmds = append(cmds, cmd)
		} else {
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
		}
	} else {
		// Non-mouse messages go to both (keys are usually handled by focused component)
		m.list, cmd = m.list.Update(msg)
		cmds = append(cmds, cmd)

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
	}

	newSel := m.list.SelectedItem()
	var selItem item
	newSelID := ""
	if typedSel, ok := newSel.(item); ok {
		selItem = typedSel
		newSelID = selItem.session.ID
	}
	if oldSelID != newSelID && newSelID != "" {
		s := selItem.session
		m.activeSession = s.TmuxSession
		m.gitStatus = domain.GitStatus{} // Clear old status
		m.lastGitStatusUpdate = time.Time{}
		m.tmuxOutput = "Loading..."
		m.viewport.SetContent(m.tmuxOutput)
		if m.panelMode == panelModeTerminal {
			cmds = append(cmds, m.initTerminal(s), fetchGitStatus(m.cfg, s))
		} else {
			cmds = append(cmds, fetchTmuxPane(m.cfg, s), fetchGitStatus(m.cfg, s))
		}
	}

	return m, tea.Batch(cmds...)
}

func (m *Model) handleMainKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if msg.String() == "G" || msg.String() == "end" {
		if m.panelMode == panelModePreview {
			m.viewport.GotoBottom()
			return m, nil, true
		}
	}

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
			m.consoleViewport.SetContent(wrapLines(m.consoleLog, m.width-6))
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
	if msg.String() == "ctrl+m" {
		m.mouseEnabled = !m.mouseEnabled
		m.viewport.MouseWheelEnabled = m.mouseEnabled
		if m.mouseEnabled {
			m.log("Mouse reporting enabled (scrolling active)")
			return m, tea.EnableMouseCellMotion, true
		} else {
			m.log("Mouse reporting disabled (native selection unlocked)")
			return m, tea.DisableMouse, true
		}
	}
	if msg.String() == "n" {
		m.sessionCfg = domain.SessionConfig{}
		if len(m.cfg.Remotes) > 1 {
			m.state = viewStateRemotePicker
		} else if len(m.cfg.Remotes) == 1 {
			m.selectedRemote = m.cfg.Remotes[0]
			m.sessionCfg.RemoteHost = m.selectedRemote.Host
			m.state = viewStateModePicker
		} else {
			m.lastError = "No remote servers configured. Go to Admin Menu to add one."
			m.state = viewStateError
		}
		return m, nil, true
	}
	if msg.String() == "f" && len(m.cfg.Remotes) > 1 {
		hosts := []string{""} // "" = all
		for _, r := range m.cfg.Remotes {
			hosts = append(hosts, r.Host)
		}
		idx := 0
		for i, h := range hosts {
			if h == m.remoteFilter {
				idx = i
				break
			}
		}
		m.remoteFilter = hosts[(idx+1)%len(hosts)]
		m.applyRemoteFilter()
		return m, nil, true
	}
	if msg.String() == "m" {
		m.state = viewStateMenu
		return m, nil, true
	}
	if msg.String() == "q" {
		return m, tea.Quit, true
	}
	if msg.String() == "ctrl+c" {
		if m.termCloser != nil {
			m.termCloser.Close()
		}
		return m, tea.Quit, true
	}
	if msg.String() == "ctrl+s" || msg.String() == "a" {
		if sel := m.list.SelectedItem(); sel != nil {
			s := sel.(item).session
			remote, ok := resolveRemote(m.cfg, s)
			if !ok {
				return m, nil, true
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
	if msg.String() == "y" {
		if m.yankSessionOutputToClipboard(false) {
			return m, nil, true
		}
	}
	if msg.String() == "Y" {
		if m.yankSessionOutputToClipboard(true) {
			return m, nil, true
		}
	}
	if msg.String() == "p" {
		m.log("p pressed")
		if sel := m.list.SelectedItem(); sel != nil {
			s := sel.(item).session
			m.log("Selected session: %s, LocalPath: %s", s.TmuxSession, s.LocalPath)
			if s.LocalPath == "" {
				m.log("ERROR: No local path")
				m.lastError = "No local sync path available for this session"
				m.state = viewStateVSCodeError
				return m, nil, true
			}

			if err := copyStringToSystemClipboard(s.LocalPath); err != nil {
				m.log("ERROR: clipboard: %v", err)
				m.lastError = fmt.Sprintf("Could not copy to clipboard: %v", err)
				m.state = viewStateVSCodeError
				return m, nil, true
			}
			m.log("Copied local path to clipboard: %s", s.LocalPath)

			// Detect current terminal app and open a new window there (Warp: copy only + notification)
			termProgram := os.Getenv("TERM_PROGRAM")
			m.log("Terminal program: %s", termProgram)
			var script string

			switch termProgram {
			case "iTerm.app":
				// iTerm2
				cmd := fmt.Sprintf("cd %q && clear", s.LocalPath)
				script = fmt.Sprintf(`tell application "iTerm"
	create window with default profile
	tell current session of current window
		write text %q
	end tell
end tell`, cmd)
			case "WarpTerminal":
				// Path is already on clipboard; optional desktop notification
				script = `display notification "Local path copied to clipboard" with title "Aiman"`
			case "Apple_Terminal":
				// Terminal.app
				cmd := fmt.Sprintf("cd %q && clear", s.LocalPath)
				script = fmt.Sprintf(`tell application "Terminal"
	do script %q
	activate
end tell`, cmd)
			default:
				// Copy only (e.g. Cursor/VS Code integrated terminal, SSH, tmux) — do not spawn Terminal.app
				script = ""
			}

			if runtime.GOOS == "darwin" && script != "" {
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
				m.log("AppleScript completed successfully")
			}
		} else {
			m.log("ERROR: No session selected")
		}
		return m, nil, true
	}
	if msg.String() == "ctrl+r" || msg.String() == "s" {
		if sel := m.list.SelectedItem(); sel != nil {
			selectedSess := sel.(item).session
			if remote, ok := resolveRemote(m.cfg, selectedSess); ok {
				m.selectedRemote = remote
				m.sessionCfg.RemoteHost = remote.Host
			}
			m.restartingSession = &selectedSess
			m.sessionCfg = domain.SessionConfig{
				IssueKey:   selectedSess.IssueKey,
				Branch:     selectedSess.Branch,
				Repo:       domain.Repo{Name: selectedSess.RepoName, URL: ""},
				RemoteHost: selectedSess.RemoteHost,
				Directory:  "",
				PromptFree: true,
			}
			if m.selectedRemote.Host != "" {
				m.sessionCfg.RemoteHost = m.selectedRemote.Host
			}

			m.log("Preparing to restart session %q (ID: %s)", selectedSess.TmuxSession, selectedSess.ID)

			// If session is active or syncing, ask for confirmation
			if selectedSess.Status == domain.SessionStatusActive || selectedSess.Status == domain.SessionStatusSyncing {
				m.state = viewStateRestartConfirm
				return m, nil, true
			}

			m.loadingMsg = "Scanning available agents..."
			m.loadingNext = viewStateRestartAgentPicker
			m.state = viewStateLoading
			return m, m.fetchAgents(), true
		}
	}
	if msg.String() == "w" {
		if sel := m.list.SelectedItem(); sel != nil {
			s := sel.(item).session
			if s.AWSProfileName == "" {
				return m, nil, true
			}
			m.loadingMsg = "Refreshing AWS credentials..."
			m.loadingNext = viewStateMain
			m.state = viewStateLoading
			return m, m.refreshAWSCredentialsCmd(s), true
		}
	}
	if msg.String() == "c" {
		if sel := m.list.SelectedItem(); sel != nil {
			s := sel.(item).session
			if remote, ok := resolveRemote(m.cfg, s); ok {
				m.selectedRemote = remote
				m.sessionCfg.RemoteHost = remote.Host
			}
			m.changingDirSession = &s
			m.loadingMsg = "Scanning directories..."
			m.loadingNext = viewStateChangeDirPicker
			m.state = viewStateLoading
			// Fetch directories from the session's worktree root
			return m, m.fetchDirectories(s.WorktreePath), true
		}
	}
	if msg.String() == "t" {
		if sel := m.list.SelectedItem(); sel != nil {
			s := sel.(item).session
			m.tunnelSession = &s
			m.tunnelError = ""
			m.tunnelList = list.New(nil, list.NewDefaultDelegate(), 76, 12)
			m.tunnelList.Title = "Tunnels"
			m.tunnelList.SetShowStatusBar(false)
			m.tunnelList.SetFilteringEnabled(false)
			m.state = viewStateTunnelManager
			return m, m.refreshTunnelStatesCmd(s), true
		}
	}
	if msg.String() == "r" {

		// Refresh sessions from all remotes
		m.log("Refreshing sessions...")
		if len(m.cfg.Remotes) > 0 {
			remotes := m.cfg.Remotes // capture for closure
			m.loadingMsg = "Refreshing sessions..."
			m.loadingNext = viewStateMain
			m.state = viewStateLoading
			return m, func() tea.Msg {
				ctx := context.Background()
				var allSessions []domain.Session
				scannedHosts := make(map[string]bool)
				for _, r := range config.UniqueRemotes(remotes) {
					mgr := ssh.NewManager(ssh.Config{Host: r.Host, User: r.User, Root: r.Root})
					if err := mgr.Connect(ctx); err != nil {
						continue
					}
					discoverer := usecase.NewSessionDiscoverer(mgr, mutagen.NewEngine())
					sessions, _ := discoverer.Discover(ctx, r.Host)
					allSessions = append(allSessions, sessions...)
					scannedHosts[r.Host] = true
				}
				return discoveryResultMsg{
					sessions:     allSessions,
					scannedHosts: scannedHosts,
				}
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
	if msg.String() == "i" {
		if sel := m.list.SelectedItem(); sel != nil {
			s := sel.(item).session
			if m.aiLoading {
				return m, nil, true
			}
			m.aiLoading = true
			m.aiError = ""
			return m, summariseSessionCmd(m.cfg, m.intelligence, s), true
		}
	}
	if msg.String() == "ctrl+a" {
		if sel := m.list.SelectedItem(); sel != nil {
			s := sel.(item).session
			m.archiveSteps = initArchiveSteps()
			m.archiveStepIdx = 0
			m.archivePreview = &archivePreviewData{session: s} // stash session early
			m.state = viewStateArchiveProgress
			return m, tea.Batch(loadArchivePreviewCmd(m.cfg, m.snapshotManager, s), m.provisionSpinner.Tick), true
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
					m.remotes.width = m.width
					m.remotes.height = m.height
					h, v := docStyle.GetFrameSize()
					m.remotes.list.SetSize(m.width-h-4, m.height-v-6)
					m.state = i.action
					return m, nil
				}
				if i.action == viewStateProvisioningRemotePicker {
					m.remotes = NewRemotesModel(m.cfg)
					m.remotes.width = m.width
					m.remotes.height = m.height
					h, v := docStyle.GetFrameSize()
					m.remotes.list.SetSize(m.width-h-4, m.height-v-6)
					m.state = i.action
					return m, nil
				}
				if i.action == viewStateAuthRemotePicker {
					m.remotes = NewRemotesModel(m.cfg)
					m.remotes.width = m.width
					m.remotes.height = m.height
					h, v := docStyle.GetFrameSize()
					m.remotes.list.SetSize(m.width-h-4, m.height-v-6)
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
				if i.action == viewStateAISettings {
					m.aiSetup = NewAISetupModel(m.cfg)
					m.state = i.action
					return m, m.aiSetup.Init()
				}
				if i.action == viewStateSnapshotBrowser {
					m.snapshotBrowser = NewSnapshotBrowserModel(m.width, m.height, m.snapshotManager)
					m.state = i.action
					return m, loadAllSnapshotsCmd(m.snapshotManager)
				}
				if i.action == viewStateArchiveSession {
					// Go through the progress view, same as ctrl+a.
					if sel := m.list.SelectedItem(); sel != nil {
						s := sel.(item).session
						m.archiveSteps = initArchiveSteps()
						m.archiveStepIdx = 0
						m.archivePreview = &archivePreviewData{session: s}
						m.state = viewStateArchiveProgress
						return m, tea.Batch(loadArchivePreviewCmd(m.cfg, m.snapshotManager, s), m.provisionSpinner.Tick)
					}
					return m, nil
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

func (m *Model) handleProvisioningRemotePickerUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "esc":
			m.state = viewStateMenu
			return m, nil
		case "enter", " ":
			if i, ok := m.remotes.list.SelectedItem().(remoteItem); ok && i.isConfig {
				m.selectedRemote = config.Remote{
					Name: i.name,
					Host: i.host,
					User: i.user,
					Root: i.root,
				}
				steps, err := usecase.NewProvisioner(nil).GetStepsWithLocalSSHKey()
				if err != nil {
					m.lastError = fmt.Sprintf("Failed to prepare provisioning steps: %v", err)
					m.state = viewStateError
					return m, nil
				}
				m.provisionSteps = steps
				m.provisioningIdx = -1
				m.provisioningError = ""
				m.provisioningStatus = fmt.Sprintf("Connecting to %s@%s...", i.user, i.host)
				m.state = viewStateProvisioningProgress
				return m, tea.Batch(m.provisionConnectCmd(m.selectedRemote), m.provisionSpinner.Tick)
			}
		}
	}
	var cmd tea.Cmd
	m.remotes.list, cmd = m.remotes.list.Update(msg)
	return m, cmd
}

func (m *Model) handleProvisioningProgressUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "esc" && (m.provisioningError != "" || m.provisioningIdx >= len(m.provisionSteps)) {
			m.state = viewStateMenu
			return m, nil
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.provisionSpinner, cmd = m.provisionSpinner.Update(msg)
		if m.provisioningError == "" && m.provisioningIdx < len(m.provisionSteps) {
			return m, cmd
		}
		return m, nil
	case provisionConnectMsg:
		if msg.err != nil {
			m.provisioningError = msg.err.Error()
			m.provisioningStatus = "Connection failed."
			return m, nil
		}
		if len(m.provisionSteps) == 0 {
			m.provisioningIdx = 0
			m.provisioningStatus = "No provisioning steps configured."
			return m, nil
		}
		m.provisioningIdx = 0
		m.provisioningStatus = fmt.Sprintf("Running: %s", m.provisionSteps[0].Name)
		return m, m.provisionStepCmd(m.selectedRemote, 0, m.provisionSteps[0])
	case provisionStepDoneMsg:
		if msg.err != nil {
			m.provisioningError = msg.err.Error()
			if msg.idx >= 0 && msg.idx < len(m.provisionSteps) {
				m.provisioningStatus = fmt.Sprintf("Failed: %s", m.provisionSteps[msg.idx].Name)
			}
			return m, nil
		}
		next := msg.idx + 1
		m.provisioningIdx = next
		if next < len(m.provisionSteps) {
			m.provisioningStatus = fmt.Sprintf("Running: %s", m.provisionSteps[next].Name)
			return m, m.provisionStepCmd(m.selectedRemote, next, m.provisionSteps[next])
		}
		m.provisioningStatus = "Provisioning complete."
		return m, nil
	}
	return m, nil
}

func (m *Model) handleAuthRemotePickerUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "esc":
			m.state = viewStateMenu
			return m, nil
		case "enter", " ":
			if i, ok := m.remotes.list.SelectedItem().(remoteItem); ok && i.isConfig {
				m.selectedRemote = config.Remote{
					Name: i.name,
					Host: i.host,
					User: i.user,
					Root: i.root,
				}
				m.authSteps = defaultAuthWizardSteps()
				m.authStepIdx = 0
				m.authStepStatus = map[int]string{}
				m.authStepDetails = map[int]string{}
				m.authStatusMsg = "Select a step and press 'c' to run checks."
				m.authChecking = false
				m.state = viewStateAuthWizard
				return m, nil
			}
		}
	}
	var cmd tea.Cmd
	m.remotes.list, cmd = m.remotes.list.Update(msg)
	return m, cmd
}

func (m *Model) handleAuthWizardUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.provisionSpinner, cmd = m.provisionSpinner.Update(msg)
		if m.authChecking {
			return m, cmd
		}
		return m, nil
	case authCheckDoneMsg:
		m.authChecking = false
		if msg.ok {
			m.authStepStatus[msg.idx] = "ok"
			if strings.TrimSpace(msg.output) == "" {
				m.authStepDetails[msg.idx] = "Check passed."
			} else {
				m.authStepDetails[msg.idx] = strings.TrimSpace(msg.output)
			}
			m.authStatusMsg = fmt.Sprintf("Passed: %s", m.authSteps[msg.idx].Name)
		} else {
			m.authStepStatus[msg.idx] = "fail"
			detail := strings.TrimSpace(msg.output)
			if msg.err != nil {
				if detail != "" {
					detail = fmt.Sprintf("%s (%v)", detail, msg.err)
				} else {
					detail = msg.err.Error()
				}
			}
			if detail == "" {
				detail = "Check failed."
			}
			m.authStepDetails[msg.idx] = detail
			m.authStatusMsg = fmt.Sprintf("Failed: %s", m.authSteps[msg.idx].Name)
		}
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			m.state = viewStateMenu
			return m, nil
		case "up", "k":
			if m.authStepIdx > 0 {
				m.authStepIdx--
			}
			return m, nil
		case "down", "j":
			if m.authStepIdx < len(m.authSteps)-1 {
				m.authStepIdx++
			}
			return m, nil
		case "m":
			m.authStepStatus[m.authStepIdx] = "manual"
			m.authStepDetails[m.authStepIdx] = "Marked done manually."
			m.authStatusMsg = fmt.Sprintf("Marked done: %s", m.authSteps[m.authStepIdx].Name)
			return m, nil
		case "c":
			if len(m.authSteps) == 0 || m.authStepIdx < 0 || m.authStepIdx >= len(m.authSteps) {
				return m, nil
			}
			m.authChecking = true
			m.authStatusMsg = fmt.Sprintf("Checking: %s...", m.authSteps[m.authStepIdx].Name)
			return m, tea.Batch(
				m.authCheckCmd(m.selectedRemote, m.authStepIdx, m.authSteps[m.authStepIdx]),
				m.provisionSpinner.Tick,
			)
		}
	}
	return m, nil
}

func (m *Model) handleTunnelManagerUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tunnelStatesMsg:
		if m.tunnelSession == nil || msg.sessionID != m.tunnelSession.ID {
			return m, nil
		}
		if msg.err != nil {
			m.tunnelError = msg.err.Error()
			return m, nil
		}
		m.tunnelList.SetItems(msg.items)
		return m, nil
	case tunnelToggleMsg:
		if m.tunnelSession == nil || msg.sessionID != m.tunnelSession.ID {
			return m, nil
		}
		if msg.err != nil {
			m.tunnelError = msg.err.Error()
		} else {
			m.tunnelError = ""
		}
		return m, m.refreshTunnelStatesCmd(*m.tunnelSession)
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			m.state = viewStateMain
			m.tunnelSession = nil
			m.tunnelError = ""
			return m, nil
		case "r":
			if m.tunnelSession != nil {
				return m, m.refreshTunnelStatesCmd(*m.tunnelSession)
			}
			return m, nil
		case "a":
			m.tunnelError = ""
			in := textinput.New()
			in.Placeholder = "5173:5173"
			in.Focus()
			in.CharLimit = 24
			in.Width = 30
			m.tunnelInput = in
			m.state = viewStateTunnelAdd
			return m, nil
		case "d":
			if m.tunnelSession == nil {
				return m, nil
			}
			it, ok := m.tunnelList.SelectedItem().(tunnelItem)
			if !ok {
				return m, nil
			}
			session := *m.tunnelSession
			filtered := make([]domain.Tunnel, 0, len(session.Tunnels))
			for _, t := range session.Tunnels {
				if t.LocalPort == it.tunnel.LocalPort && t.RemotePort == it.tunnel.RemotePort {
					continue
				}
				filtered = append(filtered, t)
			}
			session.Tunnels = filtered
			if m.db != nil {
				if err := m.db.Save(context.Background(), &session); err != nil {
					m.tunnelError = fmt.Sprintf("failed to save tunnel removal: %v", err)
					return m, nil
				}
			}
			if remote, ok := resolveRemote(m.cfg, session); ok {
				mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
				_ = mgr.StopTunnel(context.Background(), it.tunnel.LocalPort, it.tunnel.RemotePort)
			}
			m.tunnelSession = &session
			m.updateSessionInMemory(session)
			m.tunnelError = ""
			return m, m.refreshTunnelStatesCmd(session)
		case "enter":
			if m.tunnelSession == nil {
				return m, nil
			}
			it, ok := m.tunnelList.SelectedItem().(tunnelItem)
			if !ok {
				return m, nil
			}
			return m, m.toggleTunnelCmd(*m.tunnelSession, it.tunnel, !it.running)
		}
	}

	var cmd tea.Cmd
	m.tunnelList, cmd = m.tunnelList.Update(msg)
	return m, cmd
}

func (m *Model) handleTunnelAddUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "esc":
			m.state = viewStateTunnelManager
			return m, nil
		case "enter":
			if m.tunnelSession == nil {
				m.state = viewStateMain
				return m, nil
			}
			tunnel, err := parseTunnelSpec(m.tunnelInput.Value())
			if err != nil {
				m.tunnelError = err.Error()
				return m, nil
			}
			session := *m.tunnelSession
			for _, t := range session.Tunnels {
				if t.LocalPort == tunnel.LocalPort && t.RemotePort == tunnel.RemotePort {
					m.tunnelError = "that tunnel already exists on this session"
					return m, nil
				}
			}
			session.Tunnels = append(session.Tunnels, tunnel)
			if m.db != nil {
				if err := m.db.Save(context.Background(), &session); err != nil {
					m.tunnelError = fmt.Sprintf("failed to save tunnel: %v", err)
					return m, nil
				}
			}
			m.tunnelSession = &session
			m.updateSessionInMemory(session)
			m.tunnelError = ""
			m.state = viewStateTunnelManager
			return m, m.refreshTunnelStatesCmd(session)
		}
	}
	var cmd tea.Cmd
	m.tunnelInput, cmd = m.tunnelInput.Update(msg)
	return m, cmd
}

func (m *Model) handleRemotesUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
		if m.remotes.IsAtTopLevel() {
			m.state = viewStateMenu
			return m, nil
		}
	}

	var subModel tea.Model
	var cmd tea.Cmd
	subModel, cmd = m.remotes.Update(msg)
	m.remotes = subModel.(RemotesModel)

	if m.remotes.done {
		m.allSessions = m.remotes.DiscoveredSessions
		m.applyRemoteFilter()
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

func (m *Model) handleAISetupUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
		m.state = viewStateMenu
		return m, nil
	}
	var subModel tea.Model
	var cmd tea.Cmd
	subModel, cmd = m.aiSetup.Update(msg)
	m.aiSetup = subModel.(AISetupModel)
	if m.aiSetup.saved {
		m.aiSetup.saved = false
		m.state = viewStateMenu
	}
	return m, cmd
}

func (m *Model) handleRemotePickerUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "esc":
			m.state = viewStateMain
			return m, nil
		default:
			idx := 0
			if n, err := strconv.Atoi(km.String()); err == nil {
				idx = n
			}
			if idx >= 1 && idx <= len(m.cfg.Remotes) {
				r := m.cfg.Remotes[idx-1]
				m.selectedRemote = r
				m.sessionCfg.RemoteHost = r.Host
				m.state = viewStateModePicker
			}
		}
	}
	return m, nil
}

func (m *Model) handleModePickerUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "esc":
			m.state = viewStateMain
			return m, nil
		case "1":
			m.sessionCfg = domain.SessionConfig{}
			m.issuePicker = NewIssuePickerModel(nil)
			m.issuePicker.loading = true
			m.issuePicker.SetSize(m.width, m.height)
			m.state = viewStateIssuePicker
			return m, m.searchJira("")
		case "2":
			m.sessionCfg = domain.SessionConfig{}
			m.state = viewStateBranchInput
			m.branchInput = NewBranchInputModel("")
			return m, nil
		case "3":
			m.sessionCfg = domain.SessionConfig{ExistingBranch: true}
			m.loadingMsg = "Loading repositories..."
			m.loadingNext = viewStateRepoPicker
			m.state = viewStateLoading
			m.picker = NewRepoPickerModel(nil, &m.cfg.Git)
			return m, m.fetchRepos()
		case "4":
			m.sessionCfg = domain.SessionConfig{AdHoc: true, PromptFree: true}
			m.state = viewStateBranchInput
			m.branchInput = NewAdHocLabelInputModel("")
			return m, nil
		}
	}
	return m, nil
}

func (m *Model) handleIssuePickerUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
		if m.issuePicker.list.FilterState() != list.Filtering {
			m.state = viewStateModePicker
			return m, nil
		}
	}
	if msg, ok := msg.(jiraIssuesMsg); ok {
		if msg.err != nil {
			m.lastError = fmt.Sprintf("Failed to fetch JIRA issues: %v", msg.err)
			m.state = viewStateError
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
	if m.issuePicker.selected != nil {
		slug := m.issuePicker.selected.Slug()
		m.sessionCfg.IssueKey = m.issuePicker.selected.Key
		m.sessionCfg.Issue = m.issuePicker.selected
		m.state = viewStateBranchInput
		m.branchInput = NewBranchInputModel(slug)
		return m, nil
	}
	return m, cmd
}

func (m *Model) handleBranchPickerUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
		if m.branchPicker.list.FilterState() != list.Filtering {
			m.state = viewStateRepoPicker
			return m, nil
		}
	}
	var subModel tea.Model
	var cmd tea.Cmd
	subModel, cmd = m.branchPicker.Update(msg)
	m.branchPicker = subModel.(BranchPickerModel)
	if m.branchPicker.selected != "" {
		branch := m.branchPicker.selected
		// Check DB for active sessions with the same branch + repo
		if m.db != nil {
			ctx := context.Background()
			if sessions, err := m.db.List(ctx); err == nil {
				for _, s := range sessions {
					if s.Branch == branch && s.RepoName == m.sessionCfg.Repo.Name &&
						(s.Status == domain.SessionStatusActive || s.Status == domain.SessionStatusSyncing) {
						m.sessionCfg.Branch = branch
						m.state = viewStateWorktreeExists
						return m, nil
					}
				}
			}
		}
		m.sessionCfg.Branch = branch
		m.loadingMsg = "Scanning directories..."
		m.loadingNext = viewStateDirPicker
		m.state = viewStateLoading
		return m, m.fetchRepoDirectories(&m.sessionCfg.Repo)
	}
	return m, cmd
}

func (m *Model) handleBranchInputUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
		if m.sessionCfg.IssueKey != "" {
			m.state = viewStateIssuePicker
		} else {
			m.state = viewStateModePicker
		}
		return m, nil
	}
	var subModel tea.Model
	var cmd tea.Cmd
	subModel, cmd = m.branchInput.Update(msg)
	m.branchInput = subModel.(BranchInputModel)
	if m.branchInput.Confirmed {
		m.sessionCfg.Branch = m.branchInput.Value()
		if m.sessionCfg.AdHoc {
			// Skip repo picker entirely — go straight to agent selection.
			m.loadingMsg = "Scanning available agents..."
			m.loadingNext = viewStateAgentPicker
			m.state = viewStateLoading
			return m, m.fetchAgents()
		}
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
			if m.sessionCfg.ExistingBranch {
				m.state = viewStateModePicker
			} else {
				m.state = viewStateBranchInput
			}
			return m, nil
		}
	}
	if msg, ok := msg.(reposMsg); ok {
		if msg.err != nil {
			m.lastError = fmt.Sprintf("Failed to fetch repos: %v", msg.err)
			m.state = viewStateError
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
		// Repo selected, now fetch directories (or branches for existing branch mode)
		m.sessionCfg.Repo = *m.picker.selected

		if m.sessionCfg.ExistingBranch {
			m.loadingMsg = "Loading remote branches..."
			m.loadingNext = viewStateBranchPicker
			m.state = viewStateLoading
			return m, m.fetchBranches(*m.picker.selected)
		}

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
			m.state = viewStateError
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

func (m *Model) handleChangeDirPickerUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
		if m.dirPicker.list.FilterState() != list.Filtering {
			m.state = viewStateMain
			m.changingDirSession = nil
			return m, nil
		}
	}

	if msg, ok := msg.(dirsMsg); ok {
		if msg.err != nil {
			m.lastError = fmt.Sprintf("Failed to fetch directories: %v", msg.err)
			m.state = viewStateError
			return m, nil
		}
		// When changing scope, we use the session's repo name if available
		repo := domain.Repo{Name: m.changingDirSession.RepoName}
		m.dirPicker = NewDirPickerModel(msg.dirs, repo)
		h, v := docStyle.GetFrameSize()
		m.dirPicker.SetSize(m.width-h, m.height-v)
		return m, nil
	}

	var subModel tea.Model
	var cmd tea.Cmd
	subModel, cmd = m.dirPicker.Update(msg)
	m.dirPicker = subModel.(DirPickerModel)

	if m.dirPicker.selected != "" {
		m.sessionCfg = domain.SessionConfig{
			IssueKey:   m.changingDirSession.IssueKey,
			Branch:     m.changingDirSession.Branch,
			Repo:       domain.Repo{Name: m.changingDirSession.RepoName},
			Directory:  m.dirPicker.selected,
			PromptFree: true,
		}
		m.state = viewStateChangeDirConfirm
		return m, nil
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
		m.sessionCfg.PromptFree = true
		if m.sessionCfg.AdHoc {
			m.summary = NewAdHocSummaryModel(m.sessionCfg.Branch)
		} else {
			m.summary = NewSummaryModel(m.sessionCfg.IssueKey, m.sessionCfg.Branch, m.sessionCfg.Repo, m.sessionCfg.Directory)
		}
		m.summary.SetAgent(m.sessionCfg.Agent)
		m.summary.SetSize(m.width, m.height)
		// Populate AWS override fields when the remote has SyncCredentials enabled.
		if remote := m.selectedRemote; remote.AWSDelegation != nil && remote.AWSDelegation.SyncCredentials {
			d := remote.AWSDelegation
			m.summary.SetAWSDefaults(&domain.AWSConfig{
				SourceProfile:   d.SourceProfile,
				RoleName:        d.RoleName,
				AccountID:       d.AccountID,
				Region:          d.Region,
				Regions:         d.Regions,
				SessionPolicy:   d.SessionPolicy,
				DurationSeconds: d.DurationSeconds,
			})
		}
		// Pre-fill OpenRouter API key from local environment (user can override).
		m.summary.SetOpenRouterKey(os.Getenv("OPENROUTER_API_KEY"))
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
		// Merge the summary config (including per-session AWS overrides) into sessionCfg.
		summaryCfg := m.summary.GetSessionConfig()
		m.sessionCfg.Agent = summaryCfg.Agent
		m.sessionCfg.PromptFree = summaryCfg.PromptFree
		m.sessionCfg.AWSConfig = summaryCfg.AWSConfig
		m.sessionCfg.OpenRouterAPIKey = summaryCfg.OpenRouterAPIKey
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
		case "f":
			if sel := m.list.SelectedItem(); sel != nil {
				s := sel.(item).session
				m.terminateForced = true
				m.terminatePrecheckError = ""
				m.terminateSession = s
				m.terminateSteps = []string{
					"Stopping mutagen sync",
					"Discarding changes (force)",
					"Killing tmux session",
					"Stopping agent process",
					"Removing git worktree",
					"Cleaning local files",
					"Cleaning up AWS credentials",
					"Updating session status",
				}
				m.terminateErrors = make([]string, len(m.terminateSteps))
				m.terminateIndex = 0
				m.state = viewStateTerminateProgress
				return m, m.runTerminateStepCmd(0)
			}
		case "y":
			if sel := m.list.SelectedItem(); sel != nil {
				s := sel.(item).session
				if err := m.validateTerminationPreconditions(s); err != nil {
					m.terminatePrecheckError = err.Error()
					return m, nil
				}
				m.terminateForced = false
				m.terminatePrecheckError = ""
				m.terminateSession = s
				m.terminateSteps = []string{
					"Stopping mutagen sync",
					"Killing tmux session",
					"Stopping agent process",
					"Removing git worktree",
					"Cleaning local files",
					"Cleaning up AWS credentials",
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
			if m.sessionCfg.ExistingBranch {
				// Go back to branch picker and reset selection
				m.branchPicker.selected = ""
				m.state = viewStateBranchPicker
			} else {
				m.state = viewStateBranchInput
				slug := m.sessionCfg.IssueKey
				if m.sessionCfg.Branch != "" {
					slug = m.sessionCfg.Branch
				}
				m.branchInput = NewBranchInputModel(slug)
			}
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
		// Remove terminated session from allSessions and refresh list
		var remaining []domain.Session
		for _, s := range m.allSessions {
			if s.TmuxSession != m.terminateSession.TmuxSession {
				remaining = append(remaining, s)
			}
		}
		m.allSessions = remaining
		m.applyRemoteFilter()
		m.state = viewStateMain
		return m, nil
	}
	return m, nil
}

func (m *Model) handleLoadingUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case discoveryResultMsg:
		m.log("Discovered %d sessions", len(msg.sessions))
		ctx := context.Background()

		// Save all discovered sessions to DB
		for i := range msg.sessions {
			if m.db != nil {
				_ = m.db.Save(ctx, &msg.sessions[i])
			}
		}

		// Load DB as the authoritative set of known sessions
		dbSessions := make(map[string]domain.Session)
		if m.db != nil {
			if list, err := m.db.List(ctx); err == nil {
				for _, s := range list {
					dbSessions[s.ID] = s
				}
			}
		}

		// Merge: discovered sessions win for live state; DB fills in sessions
		// from remotes we couldn't reach this scan. Sessions from scanned remotes
		// that weren't discovered are dead — skip them.
		seenID := make(map[string]bool)
		seenTmux := make(map[string]bool)
		merged := []domain.Session{}
		for _, s := range msg.sessions {
			if seenID[s.ID] {
				continue
			}
			tk := ""
			if s.TmuxSession != "" {
				tk = s.RemoteHost + "\x00" + s.TmuxSession
			}
			if tk != "" && seenTmux[tk] {
				continue
			}
			merged = append(merged, s)
			seenID[s.ID] = true
			if tk != "" {
				seenTmux[tk] = true
			}
		}
		for id, s := range dbSessions {
			if seenID[id] {
				continue
			}
			tk := ""
			if s.TmuxSession != "" {
				tk = s.RemoteHost + "\x00" + s.TmuxSession
			}
			if tk != "" && seenTmux[tk] {
				continue
			}
			// Skip sessions from remotes that were successfully scanned — they're dead
			if s.RemoteHost != "" && msg.scannedHosts[s.RemoteHost] {
				continue
			}
			merged = append(merged, s)
			seenID[id] = true
			if tk != "" {
				seenTmux[tk] = true
			}
		}

		m.allSessions = merged
		m.applyRemoteFilter()
		m.state = viewStateMain
		return m, nil
	case attachMsg:
		return m, tea.ExecProcess(msg.cmd, func(err error) tea.Msg {
			return attachDoneMsg{err: err}
		})
	case attachDoneMsg:
		if msg.err != nil {
			m.lastError = fmt.Sprintf("Failed to attach to tmux session: %v", msg.err)
			m.state = viewStateError
			return m, nil
		}
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
			m.state = viewStateError
			return m, nil
		}
		m.picker = NewRepoPickerModel(msg.repos, &m.cfg.Git)
		h, v := docStyle.GetFrameSize()
		m.picker.list.SetSize(m.width-h, m.height-v)
		m.state = m.loadingNext
		return m, nil
	case branchesMsg:
		if msg.err != nil {
			m.lastError = fmt.Sprintf("Failed to fetch branches: %v", msg.err)
			m.state = viewStateError
			return m, nil
		}
		m.branchPicker = NewBranchPickerModel(msg.branches)
		h, v := docStyle.GetFrameSize()
		m.branchPicker.SetSize(m.width-h, m.height-v)
		m.state = m.loadingNext
		return m, nil
	case dirsMsg:
		if msg.err != nil {
			m.lastError = fmt.Sprintf("Failed to fetch directories: %v", msg.err)
			m.state = viewStateError
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
			m.state = viewStateError
			return m, nil
		}

		// Update the session in the list
		items := m.list.Items()
		for i, it := range items {
			if sessItem, ok := it.(item); ok && sessItem.session.ID == msg.session.ID {
				sessItem.session = msg.session
				items[i] = sessItem
				break
			}
		}
		m.list.SetItems(items)

		// Persist the updated session (with new sync ID and local path)
		if m.db != nil {
			ctx := context.Background()
			_ = m.db.Save(ctx, &msg.session)
		}

		m.state = viewStateMain
		return m, nil
	case agent.ScanAgentsMsg:
		if msg.Err != nil {
			m.lastError = fmt.Sprintf("Failed to scan agents: %v", msg.Err)
			m.state = viewStateError
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
			m.state = viewStateError
			return m, nil
		}

		items := m.list.Items()
		found := false
		// Update existing session in the list if it already exists (matches by ID)
		for i, it := range items {
			if sessItem, ok := it.(item); ok && sessItem.session.ID == msg.session.ID {
				sessItem.session = msg.session
				items[i] = sessItem
				m.list.Select(i)
				found = true
				break
			}
		}

		if !found {
			// Add new session to allSessions and list
			m.allSessions = append(m.allSessions, msg.session)
			items = append(items, m.makeItem(msg.session))
			m.list.Select(len(items) - 1)
		} else {
			// Update in allSessions too
			for i, s := range m.allSessions {
				if s.ID == msg.session.ID {
					m.allSessions[i] = msg.session
					break
				}
			}
		}

		m.list.SetItems(items)
		m.restartingSession = nil

		// Fetch preview for the session
		m.activeSession = msg.session.TmuxSession
		m.tmuxOutput = "Loading..."
		m.viewport.SetContent(m.tmuxOutput)
		m.state = m.loadingNext

		return m, tea.Batch(tickTmux(), fetchTmuxPane(m.cfg, msg.session))
	}
	return m, nil
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

func prReviewForeground(status string) lipgloss.Color {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "approved":
		return lipgloss.Color("#00FF00")
	case "changes_requested":
		return lipgloss.Color("#FF0000")
	case "pending":
		return lipgloss.Color("#FFA500")
	default:
		return lipgloss.Color("#7D7D7D")
	}
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
		contentW := mainWidth - 4
		if contentW < 20 {
			contentW = 20
		}

		maxPath := min(contentW, 80)
		wtDisp := truncateRunes(s.WorktreePath, maxPath)
		sum := fmt.Sprintf("%s · %s · %s · %s", s.TmuxSession, s.RemoteHost, s.RepoName, s.Status)
		sum = truncateRunes(sum, max(10, contentW-12))
		sessionLines := []string{
			activeStyle.Render("Session") + "  " + sum,
			wtDisp,
		}
		var meta []string
		if s.WorkingDirectory != "" && s.WorkingDirectory != s.WorktreePath {
			scope := s.WorkingDirectory
			if strings.HasPrefix(scope, s.WorktreePath) {
				scope = "." + strings.TrimPrefix(scope, s.WorktreePath)
			}
			meta = append(meta, truncateRunes(scope, 28))
		}
		if s.MutagenSyncID != "" {
			meta = append(meta, "sync:"+successStyle.Render(truncateRunes(s.LocalPath, 32)))
		} else {
			meta = append(meta, failStyle.Render("no sync"))
		}
		if s.IssueKey != "" {
			meta = append(meta, s.IssueKey)
		}
		if len(s.Tunnels) > 0 {
			meta = append(meta, fmt.Sprintf("tunnels:%d", len(s.Tunnels)))
		}
		meta = append(meta, s.CreatedAt.Format("2006-01-02 15:04"))
		sessionLines = append(sessionLines, strings.Join(meta, " · "))

		var gitLines []string
		if m.gitStatus.Branch == "" {
			gitLines = append(gitLines, activeStyle.Render("Git")+"  "+statusStyle.Render("Loading…"))
		} else {
			br := m.gitStatus.Branch
			if m.gitStatus.Ahead > 0 || m.gitStatus.Behind > 0 {
				br += fmt.Sprintf(" ↑%d↓%d", m.gitStatus.Ahead, m.gitStatus.Behind)
			}
			var ch string
			if m.gitStatus.StagedCount > 0 || m.gitStatus.UnstagedCount > 0 || m.gitStatus.UntrackedCount > 0 {
				ch = fmt.Sprintf("%ds · %du · %d?",
					m.gitStatus.StagedCount, m.gitStatus.UnstagedCount, m.gitStatus.UntrackedCount)
			} else {
				ch = "clean"
			}
			gitHead := activeStyle.Render("Git") + "  " + br + " · " + ch
			if !m.lastGitStatusUpdate.IsZero() {
				gitHead += statusStyle.Render(" · PR@" + m.lastGitStatusUpdate.Format("15:04:05"))
			}
			gitLines = append(gitLines, gitHead)

			if pr := m.gitStatus.PullRequest; pr != nil {
				stateLabel := pr.DisplayState
				if stateLabel == "" {
					stateLabel = strings.ToLower(pr.State)
				}
				titleMax := contentW - 24
				if titleMax < 14 {
					titleMax = 14
				}
				prLine := fmt.Sprintf("  #%d %s · %s", pr.Number, truncateRunes(pr.Title, titleMax), strings.ToUpper(stateLabel))
				if pr.IsDraft {
					prLine += " · draft"
				}
				if pr.Merged {
					prLine += " · merged"
				}
				if pr.CommentCount > 0 {
					prLine += fmt.Sprintf(" · %dc", pr.CommentCount)
				}
				gitLines = append(gitLines, truncateRunes(prLine, contentW))

				revKey := pr.ReviewStatus
				if revKey == "" && pr.ReviewDecision != "" {
					revKey = strings.ToLower(pr.ReviewDecision)
				}
				revLabel := "R:" + revKey
				if revLabel == "R:" {
					revLabel = "R:—"
				}
				effRev := pr.ReviewStatus
				if effRev == "" {
					switch strings.ToUpper(strings.TrimSpace(pr.ReviewDecision)) {
					case "APPROVED":
						effRev = "approved"
					case "CHANGES_REQUESTED":
						effRev = "changes_requested"
					}
				}
				revStyled := lipgloss.NewStyle().Foreground(prReviewForeground(effRev)).Render(revLabel)

				var thStyled string
				if pr.UnresolvedReviewThreads >= 0 {
					thStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#7D7D7D"))
					if pr.UnresolvedReviewThreads > 0 {
						thStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FFA500"))
					}
					thStyled = thStyle.Render(fmt.Sprintf("threads:%d", pr.UnresolvedReviewThreads))
				} else {
					thStyled = statusStyle.Render("threads:?")
				}

				mergeColor := lipgloss.Color("#7D7D7D")
				mergeTxt := "merge:?"
				switch strings.ToUpper(strings.TrimSpace(pr.Mergeable)) {
				case "CONFLICTING":
					mergeColor = lipgloss.Color("#FF0000")
					mergeTxt = "merge:conflict"
				case "MERGEABLE":
					mergeColor = lipgloss.Color("#00FF00")
					mergeTxt = "merge:ok"
				case "UNKNOWN":
					mergeTxt = "merge:…"
				}
				if pr.HasMergeConflict || strings.EqualFold(pr.MergeStateStatus, "DIRTY") {
					mergeColor = lipgloss.Color("#FF0000")
					mergeTxt = "merge:dirty"
				}
				mergeStyled := lipgloss.NewStyle().Foreground(mergeColor).Render(mergeTxt)

				var parts []string
				parts = append(parts, revStyled, thStyled, mergeStyled)
				if pr.ChecksStatus != "none" && pr.ChecksSummary != "" {
					chkColor := lipgloss.Color("#7D7D7D")
					switch pr.ChecksStatus {
					case "success":
						chkColor = lipgloss.Color("#00FF00")
					case "failure":
						chkColor = lipgloss.Color("#FF0000")
					case "pending":
						chkColor = lipgloss.Color("#FFA500")
					}
					parts = append(parts, lipgloss.NewStyle().Foreground(chkColor).Render("CI:"+pr.ChecksSummary))
				}
				gitLines = append(gitLines, "  "+strings.Join(parts, "  "))
			} else {
				gitLines = append(gitLines, "  "+statusStyle.Render("No open PR (or gh unavailable)"))
			}
		}

		sep := statusStyle.Render(strings.Repeat("─", contentW))
		infoRaw := strings.Join(sessionLines, "\n") + "\n" + sep + "\n" + strings.Join(gitLines, "\n")
		infoPanel := lipgloss.NewStyle().Width(contentW).Render(infoRaw)

		// AI insight panel — shown below git status when available or loading
		aiPanel := m.renderAIPanel(s, contentW)

		// Snapshot toast
		snapshotBar := ""
		if m.snapshotToast != "" {
			toastStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
			if m.snapshotToastError {
				toastStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
			}
			snapshotBar = "\n" + toastStyle.Render(m.snapshotToast) + "\n"
		}

		var outputPanel strings.Builder
		outputPanel.WriteString("\n" + strings.Repeat("─", contentW) + "\n")
		modeName := "Preview"
		if m.panelMode == panelModeTerminal {
			modeName = "Terminal"
		}
		outputPanel.WriteString(statusStyle.Render(modeName+" · ctrl+s fullscreen") + "\n")

		if m.panelMode == panelModeTerminal && m.terminal != nil {
			outputPanel.WriteString(m.terminal.View())
		} else {
			outputPanel.WriteString(m.viewport.View())
		}

		mainContent = infoPanel + aiPanel + snapshotBar + outputPanel.String()
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

	remoteInfo := fmt.Sprintf("Remotes: %d configured", len(m.cfg.Remotes))
	if m.remoteFilter != "" {
		remoteInfo += " | Filter: " + activeStyle.Render(remoteNameForHost(m.cfg, m.remoteFilter))
	}

	footer := "\n" + remoteInfo + "\n\n" + doctorOutput.String()

	helpBar := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		MarginTop(1).
		Render("n: new • f: filter • c: scope • t: tunnels • s: restart • y: copy view • G/end: latest • r: refresh • i: AI insight • ctrl+y: sync • ctrl+k: term • m: menu • v: vscode • ctrl+s/a: attach • q: quit")

	// PR Buttons (matching Figma)
	var prButtons string
	if sel := m.list.SelectedItem(); sel != nil {
		if m.gitStatus.PullRequest != nil {
			btnStyle := lipgloss.NewStyle().
				Padding(0, 1).
				MarginRight(1).
				Background(lipgloss.Color("235"))

			reviewBtn := btnStyle.Render("Review PR")
			approveBtn := btnStyle.Foreground(lipgloss.Color("#00FF00")).Render("Approved")
			requestBtn := btnStyle.Foreground(lipgloss.Color("#FF0000")).Render("Request Changes")

			prButtons = "\n\n" + reviewBtn + approveBtn + requestBtn
		}
	}

	return docStyle.Render(content + "\n" + footer + prButtons + "\n" + helpBar)
}

// renderAIPanel renders the AI insight section of the main panel.
// It shows a loading indicator, the session summary, action items, or a hint
// to press 'i' if no summary has been generated yet.
func (m *Model) renderAIPanel(s domain.Session, contentW int) string {
	aiHeader := activeStyle.Render("AI") + "  "
	sep := "\n" + statusStyle.Render(strings.Repeat("─", contentW)) + "\n"

	if m.aiLoading {
		return sep + aiHeader + statusStyle.Render("Analysing session…") + "\n"
	}

	if m.aiError != "" {
		return sep + aiHeader + failStyle.Render(m.aiError) + "\n"
	}

	summary, ok := m.aiSummary[s.TmuxSession]
	if !ok {
		if m.intelligence != nil {
			hint := statusStyle.Render("Press i to get AI insight for this session")
			return sep + aiHeader + hint + "\n"
		}
		return ""
	}

	var lines []string

	// Agent state badge
	stateColor := lipgloss.Color("#7D7D7D")
	switch summary.AgentState {
	case domain.AgentStateWorking:
		stateColor = lipgloss.Color("#00FF00")
	case domain.AgentStateWaitingInput:
		stateColor = lipgloss.Color("#FFA500")
	case domain.AgentStateErrored:
		stateColor = lipgloss.Color("#FF0000")
	case domain.AgentStateIdle:
		stateColor = lipgloss.Color("#7D7D7D")
	}
	stateBadge := lipgloss.NewStyle().Foreground(stateColor).Render(string(summary.AgentState))
	lines = append(lines, aiHeader+stateBadge)

	// Topic — what the session is about (muted, above the status line)
	if summary.Topic != "" {
		wrapW := contentW - 4
		if wrapW < 20 {
			wrapW = 20
		}
		topicStyled := lipgloss.NewStyle().
			Foreground(lipgloss.Color("244")).
			Italic(true).
			Width(wrapW).
			Render(summary.Topic)
		for _, l := range strings.Split(topicStyled, "\n") {
			lines = append(lines, "  "+l)
		}
	}

	// Summary text — current status, word-wrapped to content width
	if summary.Summary != "" {
		wrapW := contentW - 4
		if wrapW < 20 {
			wrapW = 20
		}
		wrapped := lipgloss.NewStyle().Width(wrapW).Render(summary.Summary)
		for _, l := range strings.Split(wrapped, "\n") {
			lines = append(lines, "  "+l)
		}
	}

	// Action items
	for _, action := range summary.Actions {
		wrapW := contentW - 6
		if wrapW < 20 {
			wrapW = 20
		}
		actionStyled := lipgloss.NewStyle().Foreground(lipgloss.Color("#7D7D7D")).Width(wrapW).Render("· " + action)
		for _, l := range strings.Split(actionStyled, "\n") {
			lines = append(lines, "  "+l)
		}
	}

	return sep + strings.Join(lines, "\n") + "\n"
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
		m.sessionCfg.OpenRouterAPIKey = os.Getenv("OPENROUTER_API_KEY")
		m.priorSnapshotCandidate = nil
		// Check for a prior snapshot before restarting — let the user preview it.
		return m, loadPriorSnapshotCmd(m.snapshotManager, m.restartingSession.ID)
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

		s.ID = strings.TrimSpace(s.ID)
		if s.ID == "" {
			return sessionCreateMsg{err: fmt.Errorf("session ID is empty (%q), cannot safely restart session", s.ID)}
		}

		remote, ok := resolveRemote(m.cfg, *s)
		if !ok {
			return sessionCreateMsg{err: fmt.Errorf("no active remote configured")}
		}

		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})

		// Ensure working directory exists
		m.log("Restarting: WorktreePath=%q, WorkingDirectory=%q", s.WorktreePath, s.WorkingDirectory)
		workingDir := s.WorkingDirectory
		if workingDir == "" {
			workingDir = s.WorktreePath
		}

		if workingDir == "" {
			return sessionCreateMsg{err: fmt.Errorf("session has no working directory or worktree path defined")}
		}

		if err := mgr.ValidateDir(ctx, workingDir); err != nil {
			return sessionCreateMsg{err: fmt.Errorf("working directory not found (%q): %w", workingDir, err)}
		}

		// Write session ID to git metadata (safe from git status/commits)
		if s.WorktreePath != "" {
			m.log("Ensuring session ID is persisted in git metadata")
			idCmd := fmt.Sprintf("git_dir=$(git -C %q rev-parse --git-dir 2>/dev/null) && if [ -d \"$git_dir\" ]; then echo %q > \"$git_dir/aiman-id\"; fi",
				s.WorktreePath, strings.TrimSpace(s.ID))
			_, _ = mgr.Execute(ctx, idCmd)

			// Optional: cleanup old file if it exists at root
			_, _ = mgr.Execute(ctx, fmt.Sprintf("rm -f %q/.aiman-id", s.WorktreePath))
		}

		// 1. Kill existing tmux session if it exists
		mgr.Execute(ctx, fmt.Sprintf("tmux kill-session -t %q", s.TmuxSession))

		// 2. Terminate existing mutagen sync if it exists
		// We ignore errors here because it might not exist
		mutagenCmd := exec.CommandContext(ctx, "mutagen", "sync", "terminate", s.TmuxSession)
		_ = mutagenCmd.Run()

		// 3. Start tmux session and agent
		agentCmd := m.sessionCfg.Agent.Command
		var sendKeysPrompt string
		issueForPrompt := m.sessionCfg.Issue
		if issueForPrompt == nil && strings.TrimSpace(m.sessionCfg.IssueKey) != "" {
			jiraProvider := jira.NewProvider(jira.Config{
				URL:      m.cfg.Integrations.Jira.URL,
				Email:    m.cfg.Integrations.Jira.Email,
				APIToken: m.cfg.Integrations.Jira.APIToken,
			})
			if iss, err := jiraProvider.GetIssue(ctx, m.sessionCfg.IssueKey); err == nil {
				issueForPrompt = &iss
			}
		}
		if m.flowManager != nil && m.flowManager.SkillEngine != nil {
			prepared, err := m.flowManager.SkillEngine.PrepareSession(ctx, mgr, workingDir, *m.sessionCfg.Agent, m.sessionCfg.Skills, m.sessionCfg.PromptFree, issueForPrompt, m.sessionCfg.PriorSnapshot)
			if err == nil {
				agentCmd = prepared.Command
				sendKeysPrompt = prepared.InitialPrompt
			}
		}

		agentBootstrap := fmt.Sprintf("export PATH=\"$PATH:$HOME/.local/bin:$HOME/.npm-global/bin:$HOME/bin:$HOME/.bun/bin:$HOME/.local/share/pnpm:$HOME/.pnpm:$HOME/.yarn/bin:$HOME/.cargo/bin:/usr/local/bin:/opt/homebrew/bin:$HOME/.opencode/bin\"; %s", agentCmd)
		extraEnvFlags := ""
		if m.sessionCfg.OpenRouterAPIKey != "" {
			extraEnvFlags += fmt.Sprintf(" -e OPENROUTER_API_KEY=%s", m.sessionCfg.OpenRouterAPIKey)
		}
		startCmd := fmt.Sprintf(
			"tmux new-session -d -s %q -c %q -e AIMAN_ID=%s%s \"bash -l -c '%s; exec bash'\" && tmux set-option -p -t %q remain-on-exit on",
			s.TmuxSession, workingDir, strings.TrimSpace(s.ID), extraEnvFlags, agentBootstrap, s.TmuxSession,
		)
		_, tmuxErr := mgr.Execute(ctx, startCmd)
		if tmuxErr != nil {
			return sessionCreateMsg{err: fmt.Errorf("failed to start tmux session: %w", tmuxErr)}
		}

		// For agents that don't support inline initial prompts, send it via
		// tmux send-keys after the agent has had time to start up.
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
				s.TmuxSession,
				s.TmuxSession, sendKeysPrompt,
				s.TmuxSession,
			)
			_, _ = mgr.Execute(ctx, fmt.Sprintf("nohup bash -c %q >/dev/null 2>&1 &", sendCmd))
		}

		// Trust the directory for common agents when supported.
		_, _ = mgr.Execute(ctx, fmt.Sprintf("git config --global --add safe.directory %q", workingDir))
		_, _ = mgr.Execute(ctx, fmt.Sprintf("cd %q && if command -v claude >/dev/null; then claude trust . >/dev/null 2>&1; fi", workingDir))
		_, _ = mgr.Execute(ctx, fmt.Sprintf("cd %q && if command -v copilot >/dev/null; then copilot trust . >/dev/null 2>&1 || copilot trust add . >/dev/null 2>&1; fi", workingDir))
		_, _ = mgr.Execute(ctx, fmt.Sprintf("cd %q && if command -v gh >/dev/null; then gh copilot trust . >/dev/null 2>&1 || gh copilot trust add . >/dev/null 2>&1; fi", workingDir))
		_, _ = mgr.Execute(ctx, geminiGlobalTrustCmd(workingDir))

		// 4. Start mutagen sync
		mutagenEngine := mutagen.NewEngine()
		home, _ := os.UserHomeDir()

		syncName := "aiman-sync-" + s.ID
		m.log("Creating sync %q", syncName)
		localSyncPath := filepath.Join(home, config.DirName, "work", s.ID)

		m.log("Cleaning up local sync path: %s", localSyncPath)
		_ = os.RemoveAll(localSyncPath)
		if err := os.MkdirAll(localSyncPath, 0755); err != nil {
			m.log("Warning: failed to create local sync path: %v", err)
		}

		m.log("Terminating existing sync: %s", syncName)
		_ = exec.CommandContext(ctx, "mutagen", "sync", "terminate", syncName).Run()

		target := remote.Host
		if remote.User != "" {
			target = fmt.Sprintf("%s@%s", remote.User, remote.Host)
		}
		remoteSyncPath := fmt.Sprintf("%s:%s", target, workingDir)
		labels := map[string]string{"aiman-id": s.ID}
		if err := mutagenEngine.StartSync(ctx, syncName, localSyncPath, remoteSyncPath, labels); err != nil {
			// We continue even if sync fails, but log it
			m.log("Warning: failed to restart mutagen sync: %v", err)
		}

		// Update session status
		s.MutagenSyncID = syncName
		s.LocalPath = localSyncPath
		s.Status = domain.SessionStatusSyncing
		s.AgentName = m.sessionCfg.Agent.Name
		s.UpdatedAt = time.Now()

		if m.db != nil {
			_ = m.db.Save(ctx, s)
		}

		return sessionCreateMsg{session: *s, err: nil}
	}
}

func (m *Model) handleChangeDirConfirmUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "y":
			m.loadingMsg = "Restarting session with new scope..."
			m.loadingNext = viewStateMain
			m.state = viewStateLoading

			// We need to update the session's WorkingDirectory
			m.changingDirSession.WorkingDirectory = m.sessionCfg.Directory
			if !strings.HasPrefix(m.changingDirSession.WorkingDirectory, m.changingDirSession.WorktreePath) {
				m.changingDirSession.WorkingDirectory = filepath.Join(m.changingDirSession.WorktreePath, m.sessionCfg.Directory)
			}

			// Reuse restartSession but it will use the updated WorkingDirectory
			m.restartingSession = m.changingDirSession
			m.changingDirSession = nil

			// We need to ensure sessionCfg has an agent.
			// If not set, use the session's current agent if we can find it, or ask.
			// For now, we'll try to find it in the scanner list.
			if m.sessionCfg.Agent == nil {
				m.loadingMsg = "Scanning agents..."
				m.loadingNext = viewStateRestartAgentPicker
				return m, m.fetchAgents()
			}

			return m, m.restartSession()
		case "n", "esc":
			m.state = viewStateMain
			m.changingDirSession = nil
			return m, nil
		}
	}
	return m, nil
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

func (m *Model) handleSnapshotPreviewUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "y":
			m.sessionCfg.PriorSnapshot = m.priorSnapshotCandidate
			if m.priorSnapshotCandidate != nil {
				_ = m.db.MarkSnapshotInjected(context.Background(), m.priorSnapshotCandidate.ID)
			}
			m.priorSnapshotCandidate = nil
			m.loadingMsg = fmt.Sprintf("Restarting session %s...", m.restartingSession.TmuxSession)
			m.loadingNext = viewStateMain
			m.state = viewStateLoading
			return m, m.restartSession()
		case "n":
			m.sessionCfg.PriorSnapshot = nil
			m.priorSnapshotCandidate = nil
			m.loadingMsg = fmt.Sprintf("Restarting session %s...", m.restartingSession.TmuxSession)
			m.loadingNext = viewStateMain
			m.state = viewStateLoading
			return m, m.restartSession()
		case "esc":
			m.priorSnapshotCandidate = nil
			m.restartingSession = nil
			m.state = viewStateMain
			return m, nil
		}
	}
	return m, nil
}

func (m *Model) handleSnapshotBrowserUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		if km.String() == "esc" && m.snapshotBrowser.confirmDelete == nil {
			m.state = viewStateMain
			return m, nil
		}
	}
	var sub tea.Model
	var cmd tea.Cmd
	sub, cmd = m.snapshotBrowser.Update(msg)
	m.snapshotBrowser = sub.(SnapshotBrowserModel)
	return m, cmd
}

func (m *Model) handleArchivePreviewUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "enter":
			p := m.archivePreview
			m.archivePreview = nil
			m.state = viewStateMain
			if p != nil && p.snapshot != nil {
				m.snapshotToast = "📸 Saving archive…"
				m.snapshotToastError = false
				return m, persistSnapshotCmd(m.snapshotManager, p.snapshot)
			}
			return m, nil
		case "esc":
			m.archivePreview = nil
			m.state = viewStateMain
			return m, nil
		case "d":
			// Dump raw and cleaned pane content to /tmp for inspection.
			if p := m.archivePreview; p != nil {
				name := p.session.TmuxSession
				rawPath := filepath.Join(os.TempDir(), "aiman-"+name+"-raw.txt")
				cleanedPath := filepath.Join(os.TempDir(), "aiman-"+name+"-cleaned.txt")
				_ = os.WriteFile(rawPath, []byte(p.rawPane), 0600)
				_ = os.WriteFile(cleanedPath, []byte(p.cleanedPane), 0600)
				m.snapshotToast = fmt.Sprintf("📄 Dumped to /tmp/aiman-%s-{raw,cleaned}.txt", name)
				m.snapshotToastError = false
			}
			return m, nil
		}
	}
	// Forward scroll events to the viewport.
	var cmd tea.Cmd
	m.archivePreviewVP, cmd = m.archivePreviewVP.Update(msg)
	return m, cmd
}

// buildArchivePreviewBody renders the scrollable body of the archive preview dialog.
func buildArchivePreviewBody(p *archivePreviewData, inner int) string {
	sectionStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	bulletStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	wrapStyle := lipgloss.NewStyle().Width(inner - 4) // "  • " = 4 chars
	sentWrap := lipgloss.NewStyle().Width(inner - 2)  // "  " indent = 2 chars

	var b strings.Builder
	if p == nil || p.summary == nil {
		return ""
	}

	if len(p.summary.Overview) > 0 {
		b.WriteString(sectionStyle.Render("Overview") + "\n")
		for _, sentence := range p.summary.Overview {
			for _, l := range strings.Split(sentWrap.Render(sentence), "\n") {
				b.WriteString("  " + strings.TrimRight(l, " ") + "\n")
			}
		}
		b.WriteString("\n")
	}

	if len(p.summary.Details) > 0 {
		b.WriteString(sectionStyle.Render("Details") + "\n")
		for _, d := range p.summary.Details {
			lines := strings.Split(wrapStyle.Render(d), "\n")
			b.WriteString("  " + bulletStyle.Render("•") + " " + strings.TrimRight(lines[0], " ") + "\n")
			for _, l := range lines[1:] {
				b.WriteString("    " + strings.TrimRight(l, " ") + "\n")
			}
		}
		b.WriteString("\n")
	}

	if len(p.summary.Actions) > 0 {
		b.WriteString(sectionStyle.Render("Needs Attention") + "\n")
		for _, a := range p.summary.Actions {
			lines := strings.Split(wrapStyle.Render(a), "\n")
			b.WriteString("  " + warnStyle.Render("⚠") + " " + strings.TrimRight(lines[0], " ") + "\n")
			for _, l := range lines[1:] {
				b.WriteString("    " + strings.TrimRight(l, " ") + "\n")
			}
		}
		b.WriteString("\n")
	}

	if len(p.summary.NextSteps) > 0 {
		b.WriteString(sectionStyle.Render("Next Steps") + "\n")
		for _, s := range p.summary.NextSteps {
			lines := strings.Split(wrapStyle.Render(s), "\n")
			b.WriteString("  " + bulletStyle.Render("→") + " " + strings.TrimRight(lines[0], " ") + "\n")
			for _, l := range lines[1:] {
				b.WriteString("    " + strings.TrimRight(l, " ") + "\n")
			}
		}
		b.WriteString("\n")
	}

	// Prompt preview — shows exactly what was sent to the model.
	codeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	if p.promptHead != "" || p.promptTail != "" {
		b.WriteString(sectionStyle.Render("Prompt Preview") + "\n")
		b.WriteString(codeStyle.Render(fmt.Sprintf(
			"  head: %d chars sent  ·  tail: %d chars sent  ·  cleaned total: %d chars\n",
			min(len(p.cleanedPane), ai.MaxHeadChars),
			min(len(p.cleanedPane), ai.MaxTailChars),
			len(p.cleanedPane),
		)))
		b.WriteString("\n")

		headLabel := fmt.Sprintf("── SESSION START (first %d chars) ──", ai.MaxHeadChars)
		b.WriteString(codeStyle.Render("  "+headLabel) + "\n")
		for _, l := range strings.Split(p.promptHead, "\n") {
			b.WriteString(codeStyle.Render("  "+l) + "\n")
		}
		b.WriteString("\n")

		if len(p.cleanedPane) > ai.MaxHeadChars {
			tailLabel := fmt.Sprintf("── SESSION RECENT ACTIVITY (last %d chars) ──", ai.MaxTailChars)
			b.WriteString(codeStyle.Render("  "+tailLabel) + "\n")
			for _, l := range strings.Split(p.promptTail, "\n") {
				b.WriteString(codeStyle.Render("  "+l) + "\n")
			}
			b.WriteString("\n")
		}
	}

	return b.String()
}

// promptHeadTail returns the head and tail of cleaned pane content that will be
// sent to the AI, mirroring the logic in OllamaIntelligence.SummariseSession.
func promptHeadTail(cleaned string) (head, tail string) {
	head = cleaned
	if len(cleaned) > ai.MaxHeadChars {
		head = cleaned[:ai.MaxHeadChars] + "\n...[truncated]"
	}
	tail = cleaned
	if len(cleaned) > ai.MaxTailChars {
		tail = "...[truncated]\n" + cleaned[len(cleaned)-ai.MaxTailChars:]
	}
	return head, tail
}

func initArchiveSteps() []archiveStep {
	steps := make([]archiveStep, len(archiveStepLabels))
	for i, l := range archiveStepLabels {
		steps[i] = archiveStep{label: l}
	}
	return steps
}

func (m *Model) handleArchiveProgressUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "esc" || msg.String() == "ctrl+c" {
			m.archivePreview = nil
			m.archiveSteps = nil
			m.state = viewStateMain
			return m, nil
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.provisionSpinner, cmd = m.provisionSpinner.Update(msg)
		return m, cmd
	}
	// Forward all other msgs to main Update so step/ready messages are handled
	return m, nil
}
