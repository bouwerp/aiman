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

type remotesFocus int

const (
	remotesFocusList remotesFocus = iota
	remotesFocusHost
	remotesFocusUser
	remotesFocusRoot
)

type remotesState int

const (
	remotesStateActive remotesState = iota
	remotesStateTesting
	remotesStateScanning
	remotesStateResult
)

type remoteItem struct {
	name     string
	host     string
	user     string
	root     string
	isConfig bool
	isActive bool
}

func (i remoteItem) Title() string {
	prefix := ""
	if i.isConfig {
		if i.isActive {
			prefix = "[Active] "
		} else {
			prefix = "[Saved]  "
		}
	} else {
		prefix = "[Suggest] "
	}
	return prefix + i.name
}

func (i remoteItem) Description() string {
	return fmt.Sprintf("%s@%s:%s", i.user, i.host, i.root)
}

func (i remoteItem) FilterValue() string {
	return i.name + " " + i.host
}

type RemotesModel struct {
	cfg                *config.Config
	state              remotesState
	focus              remotesFocus
	list               list.Model
	hostInput          textinput.Model
	userInput          textinput.Model
	rootInput          textinput.Model
	testResult         string
	testingHost        string
	testingUser        string
	testingRoot        string
	scanResults        *scanResults
	DiscoveredSessions []domain.Session
	done               bool
	width, height      int
}

type scanResults struct {
	sessions []domain.Session
	repos    []string
	err      error
}

func NewRemotesModel(cfg *config.Config) RemotesModel {
	items := []list.Item{}
	currentUser := os.Getenv("USER")

	// Add configured remotes
	for _, r := range cfg.Remotes {
		items = append(items, remoteItem{
			name:     r.Name,
			host:     r.Host,
			user:     r.User,
			root:     r.Root,
			isConfig: true,
			isActive: r.Host == cfg.ActiveRemote,
		})
	}

	// Add suggestions (avoid duplicates)
	known := make(map[string]bool)
	for _, r := range cfg.Remotes {
		known[r.Host] = true
	}

	hosts := ssh.ScanKnownHosts()
	for _, h := range hosts {
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
	l.SetFilteringEnabled(true)

	hi := textinput.New()
	hi.Placeholder = "Hostname or IP"

	ui := textinput.New()
	ui.Placeholder = "Username"
	ui.SetValue(currentUser)

	ri := textinput.New()
	ri.Placeholder = "Root Path (e.g. /home/user)"
	ri.SetValue("/home/" + currentUser)

	return RemotesModel{
		cfg:       cfg,
		state:     remotesStateActive,
		focus:     remotesFocusList,
		list:      l,
		hostInput: hi,
		userInput: ui,
		rootInput: ri,
	}
}

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

		// Validate that the root directory exists
		if err := mgr.ValidateDir(ctx, root); err != nil {
			return testConnMsg{host: host, user: user, root: root, err: err}
		}

		return testConnMsg{host: host, user: user, root: root, err: nil}
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

		return scanResults{
			sessions: sessions,
			repos:    repos,
		}
	}
}

func (m RemotesModel) Init() tea.Cmd {
	return nil
}

func (m RemotesModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	var cmds []tea.Cmd

	switch m.state {
	case remotesStateActive:
		switch msg := msg.(type) {
		case tea.WindowSizeMsg:
			m.width = msg.Width
			m.height = msg.Height
			m.list.SetSize(msg.Width-4, msg.Height-14)
		case tea.KeyMsg:
			switch msg.String() {
			case "tab":
				m.focus = (m.focus + 1) % 4
				return m.updateFocus()
			case "shift+tab":
				m.focus = (m.focus - 1 + 4) % 4
				return m.updateFocus()
			case "enter":
				if m.focus == remotesFocusList {
					if i, ok := m.list.SelectedItem().(remoteItem); ok {
						if i.isConfig {
							// Select as active and scan
							m.cfg.ActiveRemote = i.host
							_ = m.cfg.Save()
							m.testingHost = i.host
							m.testingUser = i.user
							m.testingRoot = i.root
							m.state = remotesStateScanning
							return m, scanRemote(i.host, i.user, i.root)
						}
						// Suggestion: fall through to test & add
					}
				}
				if m.hostInput.Value() != "" && m.userInput.Value() != "" {
					m.testingHost = m.hostInput.Value()
					m.testingUser = m.userInput.Value()
					m.testingRoot = m.rootInput.Value()
					m.state = remotesStateTesting
					return m, testConnection(m.testingHost, m.testingUser, m.testingRoot)
				}
			case "esc":
				return m, nil
			}
		}

		switch m.focus {
		case remotesFocusList:
			m.list, cmd = m.list.Update(msg)
			cmds = append(cmds, cmd)

			if i, ok := m.list.SelectedItem().(remoteItem); ok {
				m.hostInput.SetValue(i.host)
				m.userInput.SetValue(i.user)
				m.rootInput.SetValue(i.root)
			}
		case remotesFocusHost:
			m.hostInput, cmd = m.hostInput.Update(msg)
			cmds = append(cmds, cmd)
		case remotesFocusUser:
			oldUser := m.userInput.Value()
			m.userInput, cmd = m.userInput.Update(msg)
			cmds = append(cmds, cmd)
			// If user changed and root is default, update root default
			if m.userInput.Value() != oldUser && m.rootInput.Value() == "/home/"+oldUser {
				m.rootInput.SetValue("/home/" + m.userInput.Value())
			}
		case remotesFocusRoot:
			m.rootInput, cmd = m.rootInput.Update(msg)
			cmds = append(cmds, cmd)
		}

	case remotesStateTesting:
		if res, ok := msg.(testConnMsg); ok {
			if res.err == nil {
				m.testResult = successStyle.Render("✓ Success!")
				// Add to config if not already there
				exists := false
				var existingIdx int
				for idx, r := range m.cfg.Remotes {
					if r.Host == res.host {
						exists = true
						existingIdx = idx
						break
					}
				}
				if !exists {
					m.cfg.Remotes = append(m.cfg.Remotes, config.Remote{
						Name: res.host,
						Host: res.host,
						User: res.user,
						Root: res.root,
					})
				} else {
					// Update root if it changed
					m.cfg.Remotes[existingIdx].Root = res.root
				}
				m.cfg.ActiveRemote = res.host
				_ = m.cfg.Save()

				// Transition to scanning
				m.state = remotesStateScanning
				return m, scanRemote(res.host, res.user, res.root)
			} else {
				m.testResult = failStyle.Render(fmt.Sprintf("✗ Failed: %v", res.err))
				m.state = remotesStateResult
			}
			return m, nil
		}

	case remotesStateScanning:
		if res, ok := msg.(scanResults); ok {
			m.scanResults = &res
			m.DiscoveredSessions = res.sessions
			m.state = remotesStateResult
			return m, nil
		}

	case remotesStateResult:
		if msg, ok := msg.(tea.KeyMsg); ok && msg.String() == "enter" {
			if m.scanResults != nil && m.scanResults.err == nil {
				m.done = true
			} else {
				m.state = remotesStateActive
			}
			return m, nil
		}
	}

	return m, tea.Batch(cmds...)
}

func (m RemotesModel) updateFocus() (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.focus {
	case remotesFocusList:
		m.hostInput.Blur()
		m.userInput.Blur()
		m.rootInput.Blur()
	case remotesFocusHost:
		cmd = m.hostInput.Focus()
		m.userInput.Blur()
		m.rootInput.Blur()
	case remotesFocusUser:
		m.hostInput.Blur()
		cmd = m.userInput.Focus()
		m.rootInput.Blur()
	case remotesFocusRoot:
		m.hostInput.Blur()
		m.userInput.Blur()
		cmd = m.rootInput.Focus()
	}
	return m, cmd
}

func (m RemotesModel) View() string {
	if m.state == remotesStateTesting {
		return fmt.Sprintf("\n  Testing connection to %s@%s...\n", m.testingUser, m.testingHost)
	}
	if m.state == remotesStateScanning {
		return fmt.Sprintf("\n  Scanning %s@%s:%s...\n", m.testingUser, m.testingHost, m.testingRoot)
	}
	if m.state == remotesStateResult {
		var b strings.Builder
		b.WriteString(fmt.Sprintf("\n  Test Result for %s@%s:\n\n", m.testingUser, m.testingHost))
		if m.scanResults != nil {
			if m.scanResults.err != nil {
				b.WriteString(failStyle.Render(fmt.Sprintf("  ✗ Error: %v\n", m.scanResults.err)))
			} else {
				b.WriteString(successStyle.Render("  ✓ Connection & Scan Successful!\n\n"))
				b.WriteString(fmt.Sprintf("  Found %d tmux sessions\n", len(m.scanResults.sessions)))
				b.WriteString(fmt.Sprintf("  Found %d git repositories in %s\n", len(m.scanResults.repos), m.testingRoot))
			}
		} else {
			b.WriteString(m.testResult + "\n")
		}
		b.WriteString("\n  Press Enter to continue")
		return b.String()
	}

	listStyle := lipgloss.NewStyle().Border(lipgloss.NormalBorder()).Padding(1).Width(m.width - 6)
	if m.focus == remotesFocusList {
		listStyle = listStyle.BorderForeground(lipgloss.Color("205"))
	}

	inputStyle := lipgloss.NewStyle().MarginTop(1)
	focusedStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	var b strings.Builder
	b.WriteString(listStyle.Render(m.list.View()))
	b.WriteString("\n")

	hostLabel := "Host: "
	if m.focus == remotesFocusHost {
		hostLabel = focusedStyle.Render(hostLabel)
	}
	b.WriteString(inputStyle.Render(fmt.Sprintf("  %s %s", hostLabel, m.hostInput.View())))

	userLabel := "  User: "
	if m.focus == remotesFocusUser {
		userLabel = focusedStyle.Render(userLabel)
	}
	b.WriteString(fmt.Sprintf(" %s %s", userLabel, m.userInput.View()))

	rootLabel := "  Root: "
	if m.focus == remotesFocusRoot {
		rootLabel = focusedStyle.Render(rootLabel)
	}
	b.WriteString(fmt.Sprintf(" %s %s", rootLabel, m.rootInput.View()))

	b.WriteString("\n\n  [enter] select active / add new • [tab] switch focus • [esc] back")

	return b.String()
}
