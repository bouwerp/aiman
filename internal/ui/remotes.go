package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/mutagen"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/usecase"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------------------
// States
// ---------------------------------------------------------------------------

type remotesState int

const (
	remotesStateList     remotesState = iota // main list
	remotesStateAdd                          // modal: add new remote
	remotesStateEdit                         // modal: edit selected remote
	remotesStateDelete                       // modal: confirm delete
	remotesStateTesting                      // testing connection (overlay)
	remotesStateScanning                     // scanning remote (overlay)
	remotesStateResult                       // show scan/test result
)

type dialogFocus int

const (
	dialogFocusHost dialogFocus = iota
	dialogFocusUser
	dialogFocusRoot
	dialogFocusCount // sentinel for modular arithmetic
)

// ---------------------------------------------------------------------------
// List item
// ---------------------------------------------------------------------------

type remoteItem struct {
	name     string
	host     string
	user     string
	root     string
	isConfig bool
}

func (i remoteItem) Title() string {
	if !i.isConfig {
		return "  " + i.name + "  (suggestion)"
	}
	return "  " + i.name
}

func (i remoteItem) Description() string {
	return fmt.Sprintf("%s@%s:%s", i.user, i.host, i.root)
}

func (i remoteItem) FilterValue() string {
	return i.name + " " + i.host
}

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

type RemotesModel struct {
	cfg *config.Config

	state       remotesState
	dialogFocus dialogFocus

	list      list.Model
	hostInput textinput.Model
	userInput textinput.Model
	rootInput textinput.Model

	editingIndex int // index into cfg.Remotes; -1 for add

	testResult  string
	testingHost string
	testingUser string
	testingRoot string
	scanResults *scanResults

	DiscoveredSessions []domain.Session
	done               bool
	width, height      int
}

type scanResults struct {
	sessions []domain.Session
	repos    []string
	err      error
}

// IsAtTopLevel returns true when esc should leave the remotes screen entirely.
func (m RemotesModel) IsAtTopLevel() bool {
	return m.state == remotesStateList
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

func NewRemotesModel(cfg *config.Config) RemotesModel {
	m := RemotesModel{
		cfg:          cfg,
		state:        remotesStateList,
		editingIndex: -1,
	}
	m.list = m.buildList()
	return m
}

func (m *RemotesModel) buildList() list.Model {
	items := []list.Item{}
	currentUser := os.Getenv("USER")

	for _, r := range m.cfg.Remotes {
		items = append(items, remoteItem{
			name:     r.Name,
			host:     r.Host,
			user:     r.User,
			root:     r.Root,
			isConfig: true,
		})
	}

	known := make(map[string]bool)
	for _, r := range m.cfg.Remotes {
		known[r.Host] = true
	}
	for _, h := range ssh.ScanKnownHosts() {
		if !known[h] {
			items = append(items, remoteItem{
				name:     h,
				host:     h,
				user:     currentUser,
				root:     "/home/" + currentUser,
				isConfig: false,
			})
		}
	}

	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Remote Dev Servers"
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false) // single-key bindings conflict with filter input
	return l
}

func (m *RemotesModel) refreshList() {
	sel := m.list.Index()
	m.list = m.buildList()
	if m.width > 0 {
		m.list.SetSize(m.width-4, m.height-6)
	}
	if sel < len(m.list.Items()) {
		m.list.Select(sel)
	}
}

// ---------------------------------------------------------------------------
// Dialog helpers
// ---------------------------------------------------------------------------

func (m *RemotesModel) initDialog(host, user, root string) {
	m.hostInput = textinput.New()
	m.hostInput.Placeholder = "hostname or IP"
	m.hostInput.SetValue(host)
	m.hostInput.CharLimit = 120
	m.hostInput.Width = 40

	if user == "" {
		user = os.Getenv("USER")
	}
	m.userInput = textinput.New()
	m.userInput.Placeholder = "username"
	m.userInput.SetValue(user)
	m.userInput.CharLimit = 60
	m.userInput.Width = 40

	if root == "" {
		root = "/home/" + user
	}
	m.rootInput = textinput.New()
	m.rootInput.Placeholder = "/home/user"
	m.rootInput.SetValue(root)
	m.rootInput.CharLimit = 200
	m.rootInput.Width = 40

	m.dialogFocus = dialogFocusHost
}

func (m RemotesModel) applyDialogFocus() (RemotesModel, tea.Cmd) {
	m.hostInput.Blur()
	m.userInput.Blur()
	m.rootInput.Blur()
	switch m.dialogFocus {
	case dialogFocusHost:
		return m, m.hostInput.Focus()
	case dialogFocusUser:
		return m, m.userInput.Focus()
	case dialogFocusRoot:
		return m, m.rootInput.Focus()
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

type testConnMsg struct {
	host, user, root string
	err              error
}

func testConnection(host, user, root string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		mgr := ssh.NewManager(ssh.Config{Host: host, User: user, Root: root})
		if err := mgr.Connect(ctx); err != nil {
			return testConnMsg{host: host, user: user, root: root, err: err}
		}
		if err := mgr.ValidateDir(ctx, root); err != nil {
			return testConnMsg{host: host, user: user, root: root, err: err}
		}
		return testConnMsg{host: host, user: user, root: root}
	}
}

func scanRemote(host, user, root string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		mgr := ssh.NewManager(ssh.Config{Host: host, User: user, Root: root})
		if err := mgr.Connect(ctx); err != nil {
			return scanResults{err: err}
		}

		discoverer := usecase.NewSessionDiscoverer(mgr, mutagen.NewEngine())
		sessions, sessErr := discoverer.Discover(ctx, host)
		repos, repoErr := mgr.ScanGitRepos(ctx)

		if sessErr != nil || repoErr != nil {
			return scanResults{err: fmt.Errorf("scan failed: sess(%v), git(%v)", sessErr, repoErr)}
		}
		return scanResults{sessions: sessions, repos: repos}
	}
}

// ---------------------------------------------------------------------------
// Init / Update
// ---------------------------------------------------------------------------

func (m RemotesModel) Init() tea.Cmd { return nil }

func (m RemotesModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = msg.Width
		m.height = msg.Height
		m.list.SetSize(msg.Width-4, msg.Height-6)
		return m, nil
	}

	switch m.state {
	case remotesStateList:
		return m.updateList(msg)
	case remotesStateAdd, remotesStateEdit:
		return m.updateDialog(msg)
	case remotesStateDelete:
		return m.updateDelete(msg)
	case remotesStateTesting:
		return m.updateTesting(msg)
	case remotesStateScanning:
		return m.updateScanning(msg)
	case remotesStateResult:
		return m.updateResult(msg)
	}
	return m, nil
}

// --- List state ---

func (m RemotesModel) updateList(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "a":
			m.initDialog("", "", "")
			m.editingIndex = -1
			m.state = remotesStateAdd
			return m.applyDialogFocus()

		case "e", "enter":
			if i, ok := m.list.SelectedItem().(remoteItem); ok {
				m.initDialog(i.host, i.user, i.root)
				if i.isConfig {
					for idx, r := range m.cfg.Remotes {
						if r.Host == i.host {
							m.editingIndex = idx
							break
						}
					}
					m.state = remotesStateEdit
				} else {
					// suggestion → treat as add, pre-filled
					m.editingIndex = -1
					m.state = remotesStateAdd
				}
				return m.applyDialogFocus()
			}

		case "d":
			if i, ok := m.list.SelectedItem().(remoteItem); ok && i.isConfig {
				m.state = remotesStateDelete
				return m, nil
			}

		case " ":
			// Scan the selected remote to verify connectivity and discover sessions
			if i, ok := m.list.SelectedItem().(remoteItem); ok && i.isConfig {
				m.testingHost = i.host
				m.testingUser = i.user
				m.testingRoot = i.root
				m.state = remotesStateScanning
				return m, scanRemote(i.host, i.user, i.root)
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// --- Add / Edit dialog ---

func (m RemotesModel) updateDialog(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "esc":
			m.state = remotesStateList
			return m, nil
		case "tab":
			m.dialogFocus = (m.dialogFocus + 1) % dialogFocusCount
			return m.applyDialogFocus()
		case "shift+tab":
			m.dialogFocus = (m.dialogFocus - 1 + dialogFocusCount) % dialogFocusCount
			return m.applyDialogFocus()
		case "enter":
			host := strings.TrimSpace(m.hostInput.Value())
			user := strings.TrimSpace(m.userInput.Value())
			root := strings.TrimSpace(m.rootInput.Value())
			if host == "" || user == "" {
				return m, nil
			}
			if root == "" {
				root = "/home/" + user
			}
			m.testingHost = host
			m.testingUser = user
			m.testingRoot = root
			m.state = remotesStateTesting
			return m, testConnection(host, user, root)
		}
	}

	// Auto-update root when user changes
	var cmd tea.Cmd
	switch m.dialogFocus {
	case dialogFocusHost:
		m.hostInput, cmd = m.hostInput.Update(msg)
	case dialogFocusUser:
		oldUser := m.userInput.Value()
		m.userInput, cmd = m.userInput.Update(msg)
		if m.userInput.Value() != oldUser && m.rootInput.Value() == "/home/"+oldUser {
			m.rootInput.SetValue("/home/" + m.userInput.Value())
		}
	case dialogFocusRoot:
		m.rootInput, cmd = m.rootInput.Update(msg)
	}
	return m, cmd
}

// --- Delete confirmation ---

func (m RemotesModel) updateDelete(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "y":
			if i, ok := m.list.SelectedItem().(remoteItem); ok {
				newRemotes := []config.Remote{}
				for _, r := range m.cfg.Remotes {
					if r.Host != i.host {
						newRemotes = append(newRemotes, r)
					}
				}
				m.cfg.Remotes = newRemotes
				_ = m.cfg.Save()
				m.refreshList()
			}
			m.state = remotesStateList
		case "n", "esc":
			m.state = remotesStateList
		}
	}
	return m, nil
}

// --- Connection test ---

func (m RemotesModel) updateTesting(msg tea.Msg) (tea.Model, tea.Cmd) {
	res, ok := msg.(testConnMsg)
	if !ok {
		return m, nil
	}

	if res.err != nil {
		m.testResult = failStyle.Render(fmt.Sprintf("Connection failed: %v", res.err))
		m.state = remotesStateResult
		return m, nil
	}

	// Save remote
	if m.editingIndex >= 0 && m.editingIndex < len(m.cfg.Remotes) {
		m.cfg.Remotes[m.editingIndex].Host = res.host
		m.cfg.Remotes[m.editingIndex].User = res.user
		m.cfg.Remotes[m.editingIndex].Root = res.root
		m.cfg.Remotes[m.editingIndex].Name = res.host
	} else {
		m.cfg.Remotes = append(m.cfg.Remotes, config.Remote{
			Name: res.host, Host: res.host, User: res.user, Root: res.root,
		})
	}
	_ = m.cfg.Save()

	m.state = remotesStateScanning
	return m, scanRemote(res.host, res.user, res.root)
}

// --- Scanning ---

func (m RemotesModel) updateScanning(msg tea.Msg) (tea.Model, tea.Cmd) {
	if res, ok := msg.(scanResults); ok {
		m.scanResults = &res
		m.DiscoveredSessions = res.sessions
		m.state = remotesStateResult
	}
	return m, nil
}

// --- Result ---

func (m RemotesModel) updateResult(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "enter", "esc":
			if m.scanResults != nil && m.scanResults.err == nil {
				m.done = true
			} else {
				m.refreshList()
				m.state = remotesStateList
			}
		}
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m RemotesModel) View() string {
	switch m.state {
	case remotesStateTesting:
		return m.viewOverlay(fmt.Sprintf("Testing connection to %s@%s...", m.testingUser, m.testingHost))
	case remotesStateScanning:
		return m.viewOverlay(fmt.Sprintf("Scanning %s@%s:%s...", m.testingUser, m.testingHost, m.testingRoot))
	case remotesStateResult:
		return m.viewResult()
	case remotesStateAdd, remotesStateEdit:
		return m.viewDialog()
	case remotesStateDelete:
		return m.viewDeleteConfirm()
	default:
		return m.viewList()
	}
}

func (m RemotesModel) viewList() string {
	var b strings.Builder
	b.WriteString(m.list.View())
	b.WriteString("\n  ")
	b.WriteString(activeStyle.Render("[a]") + " add  ")
	b.WriteString(activeStyle.Render("[e]") + " edit  ")
	b.WriteString(activeStyle.Render("[d]") + " delete  ")
	b.WriteString(activeStyle.Render("[space]") + " scan  ")
	b.WriteString(activeStyle.Render("[esc]") + " back")
	return b.String()
}

func (m RemotesModel) viewDialog() string {
	title := "Add Remote"
	if m.state == remotesStateEdit {
		title = "Edit Remote"
	}

	focusLabel := func(label string, f dialogFocus) string {
		if m.dialogFocus == f {
			return activeStyle.Render(label)
		}
		return label
	}

	var b strings.Builder
	b.WriteString(activeStyle.Render(title) + "\n\n")
	b.WriteString(fmt.Sprintf("  %s %s\n\n", focusLabel("Host:", dialogFocusHost), m.hostInput.View()))
	b.WriteString(fmt.Sprintf("  %s %s\n\n", focusLabel("User:", dialogFocusUser), m.userInput.View()))
	b.WriteString(fmt.Sprintf("  %s %s\n\n", focusLabel("Root:", dialogFocusRoot), m.rootInput.View()))
	b.WriteString("  " + activeStyle.Render("[tab]") + " next field  ")
	b.WriteString(activeStyle.Render("[enter]") + " save & test  ")
	b.WriteString(activeStyle.Render("[esc]") + " cancel")

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("205")).
		Padding(1, 2).
		Width(60)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(b.String()))
}

func (m RemotesModel) viewDeleteConfirm() string {
	i, _ := m.list.SelectedItem().(remoteItem)

	var b strings.Builder
	b.WriteString(failStyle.Render("Delete Remote?") + "\n\n")
	b.WriteString(fmt.Sprintf("  %s@%s:%s\n\n", i.user, i.host, i.root))
	b.WriteString("  " + activeStyle.Render("[y]") + " confirm  ")
	b.WriteString(activeStyle.Render("[n]") + " cancel")

	dialog := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(lipgloss.Color("196")).
		Padding(1, 2).
		Width(50)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(b.String()))
}

func (m RemotesModel) viewOverlay(msg string) string {
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(1, 2).
		Width(60)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, style.Render(msg))
}

func (m RemotesModel) viewResult() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Result for %s@%s\n\n", m.testingUser, m.testingHost))

	if m.scanResults != nil {
		if m.scanResults.err != nil {
			b.WriteString(failStyle.Render(fmt.Sprintf("  Error: %v\n", m.scanResults.err)))
		} else {
			b.WriteString(successStyle.Render("  Connection & scan successful!") + "\n\n")
			b.WriteString(fmt.Sprintf("  Found %d tmux sessions\n", len(m.scanResults.sessions)))
			b.WriteString(fmt.Sprintf("  Found %d git repositories in %s\n", len(m.scanResults.repos), m.testingRoot))
		}
	} else {
		b.WriteString(m.testResult + "\n")
	}
	b.WriteString("\n  Press " + activeStyle.Render("Enter") + " to continue")

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(1, 2).
		Width(60)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(b.String()))
}
