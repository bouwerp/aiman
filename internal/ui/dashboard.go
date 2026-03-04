package ui

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/agent"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/git"
	"github.com/bouwerp/aiman/internal/infra/jira"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/usecase"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	docStyle     = lipgloss.NewStyle().Margin(1, 2)
	statusStyle  = lipgloss.NewStyle().PaddingLeft(2).Italic(true).Foreground(lipgloss.Color("#7D7D7D"))
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#00FF00"))
	failStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF0000"))
	activeStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Bold(true)
)

type viewState int

const (
	viewStateMain viewState = iota
	viewStateMenu
	viewStateRemotes
	viewStateSetup
	viewStateGitSetup
	viewStatePicker
	viewStateVSCodeError
	viewStateIssuePicker
	viewStateBranchInput
	viewStateRepoPicker
	viewStateDirPicker
	viewStateAgentPicker
	viewStateSummary
	viewStateLoading
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
	session domain.Session
}

func (i item) Title() string {
	if i.session.IssueKey != "" {
		return fmt.Sprintf("%s (%s)", i.session.IssueKey, i.session.TmuxSession)
	}
	return i.session.TmuxSession
}

func (i item) Description() string {
	return fmt.Sprintf("Repo: %s | Host: %s", i.session.RepoName, i.session.RemoteHost)
}

func (i item) FilterValue() string {
	return i.session.IssueKey + " " + i.session.TmuxSession + " " + i.session.RepoName
}

type Model struct {
	cfg           *config.Config
	state         viewState
	panelMode     panelMode
	list          list.Model
	menu          list.Model
	remotes       RemotesModel
	setup         SetupModel
	gitSetup      GitSetupModel
	picker        RepoPickerModel
	issuePicker   IssuePickerModel
	branchInput   BranchInputModel
	dirPicker     DirPickerModel
	agentPicker   AgentPickerModel
	summary       SummaryModel
	doctorResults []usecase.CheckResult
	width, height int
	viewport      viewport.Model
	terminal      *TerminalModel
	tmuxOutput    string
	activeSession string
	termCloser    io.Closer
	lastError     string
	loadingMsg    string
	sessionCfg    SessionConfig
	loadingNext   viewState
}

func NewModel(cfg *config.Config, doctorResults []usecase.CheckResult, initialSessions []domain.Session) Model {
	items := make([]list.Item, len(initialSessions))
	for i, s := range initialSessions {
		items[i] = item{session: s}
	}

	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Aiman Dashboard - Active Sessions"
	l.AdditionalFullHelpKeys = func() []key.Binding {
		return []key.Binding{
			key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new session")),
			key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "admin menu")),
			key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "attach full terminal")),
			key.NewBinding(key.WithKeys("ctrl+t"), key.WithHelp("ctrl+t", "toggle preview/terminal")),
			key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "open vscode")),
			key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh status")),
		}
	}

	menuItems := []list.Item{
		menuItem{title: "Manage Remote Servers", desc: "Select active, add new, or scan suggestions", action: viewStateRemotes},
		menuItem{title: "JIRA Configuration", desc: "Update URL, Email, and Token", action: viewStateSetup},
		menuItem{title: "Git Configuration", desc: "Configure repositories and organizations", action: viewStateGitSetup},
	}
	m := list.New(menuItems, list.NewDefaultDelegate(), 0, 0)
	m.Title = "Administrative Menu"

	vp := viewport.New(0, 0)
	vp.Style = lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false). // Top border
		PaddingTop(1)

	return Model{
		cfg:           cfg,
		state:         viewStateMain,
		panelMode:     panelModePreview,
		list:          l,
		menu:          m,
		remotes:       NewRemotesModel(cfg),
		setup:         NewSetupModel(cfg),
		gitSetup:      NewGitSetupModel(cfg),
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

func tickTmux() tea.Cmd {
	return tea.Tick(time.Second*2, func(t time.Time) tea.Msg {
		return tmuxTickMsg(t)
	})
}

func fetchTmuxPane(cfg *config.Config, session domain.Session) tea.Cmd {
	return func() tea.Msg {
		if cfg.ActiveRemote == "" {
			return nil
		}
		var remote config.Remote
		for _, r := range cfg.Remotes {
			if r.Host == cfg.ActiveRemote {
				remote = r
				break
			}
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

func (m *Model) initTerminal(session domain.Session) tea.Cmd {
	if m.termCloser != nil {
		m.termCloser.Close()
		m.termCloser = nil
	}

	var remote config.Remote
	for _, r := range m.cfg.Remotes {
		if r.Host == m.cfg.ActiveRemote {
			remote = r
			break
		}
	}

	mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
	stream, err := mgr.StreamTmuxSession(context.Background(), session.TmuxSession)
	if err != nil {
		m.tmuxOutput = failStyle.Render("Failed to stream session: " + err.Error())
		return nil
	}

	m.termCloser = stream
	term := NewTerminalModel(stream, m.viewport.Width, m.viewport.Height)
	m.terminal = &term
	return nil
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

		// Extract just the repo name (not org/repo)
		repoName := extractRepoName(repo.Name)
		repoPath := fmt.Sprintf("%s/%s", remote.Root, repoName)

		// Check if repo exists on remote, clone if not
		if err := mgr.ValidateDir(ctx, repoPath); err != nil {
			// Repo doesn't exist, need to clone it
			_, cloneErr := mgr.Execute(ctx, fmt.Sprintf("cd %s && git clone %s %s", remote.Root, repo.URL, repoName))
			if cloneErr != nil {
				return dirsMsg{err: fmt.Errorf("failed to clone repository: %w", cloneErr)}
			}
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

func extractRepoName(fullName string) string {
	// Extract just the repo name from "org/repo" format
	parts := strings.Split(fullName, "/")
	if len(parts) > 1 {
		return parts[len(parts)-1]
	}
	return fullName
}

func (m Model) Init() tea.Cmd {
	var cmds []tea.Cmd
	cmds = append(cmds, tickTmux())

	// Trigger initial fetch for the first selected session
	if sel := m.list.SelectedItem(); sel != nil {
		s := sel.(item).session
		m.activeSession = s.TmuxSession
		cmds = append(cmds, fetchTmuxPane(m.cfg, s))
	}

	return tea.Batch(cmds...)
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
	m.viewport.Height = mainHeight - 11 // Reserve 11 lines for details

	if m.issuePicker.list.Title != "" {
		m.issuePicker.SetSize(width, height)
	}

	if m.agentPicker.list.Title != "" {
		m.agentPicker.SetSize(width, height)
	}

	m.summary.SetSize(width, height)
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
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
		switch msg := msg.(type) {
		case tmuxTickMsg:
			cmds = append(cmds, tickTmux())
			if sel := m.list.SelectedItem(); sel != nil {
				s := sel.(item).session
				if m.activeSession != s.TmuxSession {
					m.activeSession = s.TmuxSession
				}
				cmds = append(cmds, fetchTmuxPane(m.cfg, s))
			}
		case tmuxOutputMsg:
			if msg.session == m.activeSession {
				if msg.err != nil {
					m.tmuxOutput = failStyle.Render("Failed to capture pane: " + msg.err.Error())
				} else {
					m.tmuxOutput = msg.output
				}
				m.viewport.SetContent(m.tmuxOutput)
				m.viewport.GotoBottom()
			}
		case tea.KeyMsg:
			if msg.String() == "n" {
				m.state = viewStateIssuePicker
				m.issuePicker = NewIssuePickerModel(nil)
				m.issuePicker.loading = true
				m.issuePicker.SetSize(m.width, m.height)
				return m, m.searchJira("")
			}
			if msg.String() == "m" {
				m.state = viewStateMenu
				return m, nil
			}
			if msg.String() == "ctrl+c" {
				if m.termCloser != nil {
					m.termCloser.Close()
				}
				return m, tea.Quit
			}
			if msg.String() == "ctrl+t" {
				if m.panelMode == panelModePreview {
					m.panelMode = panelModeTerminal
					if sel := m.list.SelectedItem(); sel != nil {
						cmds = append(cmds, m.initTerminal(sel.(item).session))
					}
				} else {
					m.panelMode = panelModePreview
					if m.termCloser != nil {
						m.termCloser.Close()
						m.termCloser = nil
					}
				}
				return m, tea.Batch(cmds...)
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
					return m, tea.ExecProcess(c, func(err error) tea.Msg {
						// Resume and refresh after detaching
						return tmuxTickMsg(time.Now())
					})
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
							return m, nil
						}
						err = exec.Command("code", s.LocalPath).Start()
						if err != nil {
							m.lastError = fmt.Sprintf("Failed to start VS Code: %v", err)
							m.state = viewStateVSCodeError
							return m, nil
						}
					}
				}
			}
			if msg.String() == "r" {
				// Re-init remotes scan to refresh
				m.state = viewStateRemotes
				m.remotes = NewRemotesModel(m.cfg)
				// Automatically trigger scan if we have an active remote
				if m.cfg.ActiveRemote != "" {
					var remote config.Remote
					for _, r := range m.cfg.Remotes {
						if r.Host == m.cfg.ActiveRemote {
							remote = r
							break
						}
					}
					return m, scanRemote(remote.Host, remote.User, remote.Root)
				}
				return m, nil
			}
		}

		// Capture list selection changes to trigger immediate fetch
		oldSel := m.list.SelectedItem()
		m.list, cmd = m.list.Update(msg)
		cmds = append(cmds, cmd)
		newSel := m.list.SelectedItem()

		if oldSel != newSel && newSel != nil {
			s := newSel.(item).session
			m.activeSession = s.TmuxSession
			if m.panelMode == panelModeTerminal {
				cmds = append(cmds, m.initTerminal(s))
			} else {
				m.tmuxOutput = "Loading..."
				m.viewport.SetContent(m.tmuxOutput)
				cmds = append(cmds, fetchTmuxPane(m.cfg, s))
			}
		}

		if m.panelMode == panelModeTerminal && m.terminal != nil {
			var tModel tea.Model
			tModel, cmd = m.terminal.Update(msg)
			m.terminal = tModel.(*TerminalModel)
			cmds = append(cmds, cmd)
		} else {
			m.viewport, cmd = m.viewport.Update(msg)
			cmds = append(cmds, cmd)
		}

	case viewStateMenu:
		switch msg := msg.(type) {
		case tea.KeyMsg:
			if msg.String() == "enter" {
				if i, ok := m.menu.SelectedItem().(menuItem); ok {
					if i.action == viewStateRemotes {
						// Re-init remotes model to pick up new config/status
						m.remotes = NewRemotesModel(m.cfg)
						m.state = i.action
						return m, nil
					}
					if i.action == viewStateGitSetup {
						// Re-init git setup model and trigger org fetch
						m.gitSetup = NewGitSetupModel(m.cfg)
						m.state = i.action
						return m, m.gitSetup.Init()
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
		m.menu, cmd = m.menu.Update(msg)
		cmds = append(cmds, cmd)

	case viewStateRemotes:
		if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
			if m.remotes.list.FilterState() != list.Filtering {
				m.state = viewStateMenu
				return m, nil
			}
		}

		var subModel tea.Model
		subModel, cmd = m.remotes.Update(msg)
		m.remotes = subModel.(RemotesModel)
		cmds = append(cmds, cmd)

		if m.remotes.done {
			// Populate list with discovered sessions
			items := make([]list.Item, len(m.remotes.DiscoveredSessions))
			for i, s := range m.remotes.DiscoveredSessions {
				items[i] = item{session: s}
			}
			m.list.SetItems(items)

			m.remotes.done = false // Reset
			m.state = viewStateMain
			return m, nil
		}

	case viewStateSetup:
		if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
			m.state = viewStateMenu
			return m, nil
		}

		var subModel tea.Model
		subModel, cmd = m.setup.Update(msg)
		m.setup = subModel.(SetupModel)
		cmds = append(cmds, cmd)

		if m.setup.saved {
			m.setup.saved = false // Reset
			m.state = viewStateMenu
		}

	case viewStateGitSetup:
		if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
			m.state = viewStateMenu
			return m, nil
		}

		var subModel tea.Model
		subModel, cmd = m.gitSetup.Update(msg)
		m.gitSetup = subModel.(GitSetupModel)
		cmds = append(cmds, cmd)

		if m.gitSetup.saved {
			m.gitSetup.saved = false // Reset
			m.state = viewStateMenu
		}

	case viewStateVSCodeError:
		if _, ok := msg.(tea.KeyMsg); ok {
			m.state = viewStateMain
		}

	case viewStateIssuePicker:
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
		subModel, cmd = m.issuePicker.Update(msg)
		m.issuePicker = subModel.(IssuePickerModel)
		cmds = append(cmds, cmd)

		if m.issuePicker.selected != nil {
			// Issue selected, transition to branch input
			slug := m.issuePicker.selected.Slug()
			m.sessionCfg.IssueKey = m.issuePicker.selected.Key
			m.state = viewStateBranchInput
			m.branchInput = NewBranchInputModel(slug)
			return m, nil
		}

	case viewStateBranchInput:
		if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
			m.state = viewStateIssuePicker
			return m, nil
		}

		var subModel tea.Model
		subModel, cmd = m.branchInput.Update(msg)
		m.branchInput = subModel.(BranchInputModel)
		cmds = append(cmds, cmd)

		if m.branchInput.Confirmed {
			m.sessionCfg.Branch = m.branchInput.Value()
			m.loadingMsg = "Loading repositories..."
			m.loadingNext = viewStateRepoPicker
			m.state = viewStateLoading
			m.picker = NewRepoPickerModel(nil)
			return m, m.fetchRepos()
		}

	case viewStateRepoPicker:
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
			m.picker = NewRepoPickerModel(msg.repos)
			// Resize picker
			h, v := docStyle.GetFrameSize()
			m.picker.list.SetSize(m.width-h, m.height-v)
			return m, nil
		}

		var subModel tea.Model
		subModel, cmd = m.picker.Update(msg)
		m.picker = subModel.(RepoPickerModel)
		cmds = append(cmds, cmd)

		if m.picker.selected != nil {
			// Repo selected, now fetch directories
			m.sessionCfg.Repo = *m.picker.selected
			m.loadingMsg = "Scanning directories..."
			m.loadingNext = viewStateDirPicker
			m.state = viewStateLoading
			return m, m.fetchRepoDirectories(m.picker.selected)
		}

	case viewStateDirPicker:
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
			// Resize picker
			h, v := docStyle.GetFrameSize()
			m.dirPicker.SetSize(m.width-h, m.height-v)
			return m, nil
		}

		var subModel tea.Model
		subModel, cmd = m.dirPicker.Update(msg)
		m.dirPicker = subModel.(DirPickerModel)
		cmds = append(cmds, cmd)

		if m.dirPicker.selected != "" {
			// Directory selected
			m.sessionCfg.Directory = m.dirPicker.selected
			m.loadingMsg = "Scanning available agents..."
			m.loadingNext = viewStateAgentPicker
			m.state = viewStateLoading
			return m, m.fetchAgents()
		}

	case viewStateAgentPicker:
		if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
			if m.agentPicker.list.FilterState() != list.Filtering {
				m.state = viewStateDirPicker
				return m, nil
			}
		}

		var subModel tea.Model
		subModel, cmd = m.agentPicker.Update(msg)
		m.agentPicker = subModel.(AgentPickerModel)
		cmds = append(cmds, cmd)

		if m.agentPicker.selected != nil {
			m.sessionCfg.Agent = m.agentPicker.selected
			m.summary = NewSummaryModel(m.sessionCfg.IssueKey, m.sessionCfg.Branch, m.sessionCfg.Repo, m.sessionCfg.Directory)
			m.summary.SetAgent(m.sessionCfg.Agent)
			m.summary.SetSize(m.width, m.height)
			m.state = viewStateSummary
			return m, nil
		}

	case viewStateSummary:
		if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
			m.state = viewStateAgentPicker
			return m, nil
		}

		var subModel tea.Model
		subModel, cmd = m.summary.Update(msg)
		m.summary = subModel.(SummaryModel)
		cmds = append(cmds, cmd)

		if m.summary.IsConfirmed() {
			// TODO: Step 8: Session Bootstrapping
			m.state = viewStateMain
			return m, nil
		}

	case viewStateLoading:
		switch msg := msg.(type) {
		case reposMsg:
			if msg.err != nil {
				m.lastError = fmt.Sprintf("Failed to fetch repos: %v", msg.err)
				m.state = viewStateVSCodeError
				return m, nil
			}
			m.picker = NewRepoPickerModel(msg.repos)
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
		}
	}

	return m, tea.Batch(cmds...)
}

func (m Model) View() string {
	switch m.state {
	case viewStateMain:
		// Split View
		h, v := docStyle.GetFrameSize()
		sidebarWidth := m.width / 3
		mainWidth := m.width - sidebarWidth - h - 2

		// Sidebar
		sidebar := m.list.View()

		// Main Panel
		var mainPanel strings.Builder
		if sel := m.list.SelectedItem(); sel != nil {
			s := sel.(item).session
			mainPanel.WriteString(activeStyle.Render("SESSION DETAILS") + "\n\n")
			mainPanel.WriteString(fmt.Sprintf("Tmux: %s\n", s.TmuxSession))
			mainPanel.WriteString(fmt.Sprintf("Host: %s\n", s.RemoteHost))
			mainPanel.WriteString(fmt.Sprintf("Repo: %s\n", s.RepoName))
			mainPanel.WriteString(fmt.Sprintf("Path: %s\n", s.WorktreePath))
			if s.MutagenSyncID != "" {
				mainPanel.WriteString(fmt.Sprintf("Local Sync: %s\n", successStyle.Render(s.LocalPath)))
				mainPanel.WriteString(fmt.Sprintf("Mutagen ID: %s\n", s.MutagenSyncID))
			} else {
				mainPanel.WriteString("Local Sync: " + failStyle.Render("None") + "\n")
			}
			if s.IssueKey != "" {
				mainPanel.WriteString(fmt.Sprintf("JIRA: %s\n", s.IssueKey))
			}
			mainPanel.WriteString(fmt.Sprintf("\nStatus: %s\n", s.Status))
			mainPanel.WriteString(fmt.Sprintf("Created: %s\n", s.CreatedAt.Format("2006-01-02 15:04:05")))

			// Add separator and Viewport for Tmux Output
			mainPanel.WriteString("\n" + strings.Repeat("─", mainWidth-4) + "\n")
			modeName := "PREVIEW"
			if m.panelMode == panelModeTerminal {
				modeName = "TERMINAL"
			}
			mainPanel.WriteString(activeStyle.Render(modeName) + " (ctrl+t toggle, ctrl+s full screen)\n\n")

			if m.panelMode == panelModeTerminal && m.terminal != nil {
				mainPanel.WriteString(m.terminal.View())
			} else {
				mainPanel.WriteString(m.viewport.View())
			}
		} else {
			mainPanel.WriteString("\n\n  No session selected.\n  Press 'm' for Admin Menu.")
		}

		mainStyle := lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, false, true). // Left border only
			PaddingLeft(2).
			Width(mainWidth).
			Height(m.height - v - len(m.doctorResults) - 10)

		content := lipgloss.JoinHorizontal(lipgloss.Top, sidebar, mainStyle.Render(mainPanel.String()))

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

	case viewStateMenu:
		return docStyle.Render(m.menu.View())

	case viewStateRemotes:
		return docStyle.Render(m.remotes.View())

	case viewStateSetup:
		return docStyle.Render(m.setup.View())

	case viewStateGitSetup:
		return docStyle.Render(m.gitSetup.View())

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

	case viewStateSummary:
		return m.summary.View()

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
