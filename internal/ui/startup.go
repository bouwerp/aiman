package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/mutagen"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/usecase"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type StartupModel struct {
	cfg           *config.Config
	doctor        *usecase.Doctor
	db            domain.SessionRepository
	flowManager   *usecase.FlowManager
	spinner       spinner.Model
	loadingMsg    string
	results       []usecase.CheckResult
	sessions      []domain.Session
	ready         bool
	width, height int
	checks        map[string]*usecase.CheckResult
	discoveryDone bool
	pending       int
}

func NewStartupModel(cfg *config.Config, doctor *usecase.Doctor, db domain.SessionRepository, flowManager *usecase.FlowManager) StartupModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	return StartupModel{
		cfg:         cfg,
		doctor:      doctor,
		db:          db,
		flowManager: flowManager,
		spinner:     s,
		loadingMsg:  "Initializing Aiman...",
		checks:      make(map[string]*usecase.CheckResult),
		pending:     4,
	}
}

type checkResultMsg usecase.CheckResult
type discoveryResultMsg []domain.Session

func runCheckJira(doctor *usecase.Doctor) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		return checkResultMsg(doctor.CheckJira(ctx))
	}
}

func runCheckGit(doctor *usecase.Doctor) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		return checkResultMsg(doctor.CheckGit(ctx))
	}
}

func runCheckSSH(doctor *usecase.Doctor) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		return checkResultMsg(doctor.CheckSSH(ctx))
	}
}

func runDiscovery(cfg *config.Config) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		if cfg.ActiveRemote == "" {
			return discoveryResultMsg{}
		}
		// Find remote config
		var remote config.Remote
		for _, r := range cfg.Remotes {
			if r.Host == cfg.ActiveRemote {
				remote = r
				break
			}
		}
		if remote.Host == "" {
			return discoveryResultMsg{}
		}

		mgr := ssh.NewManager(ssh.Config{
			Host: remote.Host,
			User: remote.User,
			Root: remote.Root,
		})
		if err := mgr.Connect(ctx); err != nil {
			return discoveryResultMsg{}
		}
		discoverer := usecase.NewSessionDiscoverer(mgr, mutagen.NewEngine())
		sessions, _ := discoverer.Discover(ctx, remote.Host)
		return discoveryResultMsg(sessions)
	}
}

func (m StartupModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		runCheckJira(m.doctor),
		runCheckGit(m.doctor),
		runCheckSSH(m.doctor),
		runDiscovery(m.cfg),
	)
}

func (m StartupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case checkResultMsg:
		res := usecase.CheckResult(msg)
		m.checks[res.Name] = &res
		m.results = append(m.results, res)
		m.pending--
	case discoveryResultMsg:
		m.sessions = msg
		m.discoveryDone = true
		m.pending--
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}

	if m.pending <= 0 {
		m.ready = true

		// Load sessions from database and merge with discovered sessions
		ctx := context.Background()
		dbSessions, err := m.db.List(ctx)
		if err == nil {
			// Merge logic:
			// 1. Start with discovered sessions (most accurate current state)
			// 2. Link them to DB entries if IDs match to preserve RepoName/IssueKey
			// 3. Add any DB sessions that weren't discovered (orphaned from perspective of this machine)
			
			sessMap := make(map[string]domain.Session)
			for _, s := range dbSessions {
				sessMap[s.ID] = s
			}
			
			merged := []domain.Session{}
			seenInMerged := make(map[string]bool)
			
			// First, process all discovered sessions
			for _, s := range m.sessions {
				if dbSess, ok := sessMap[s.ID]; ok {
					// Discovered session exists in DB, merge them
					// Keep discovered paths/sync but preserve DB metadata
					if s.RepoName == "" { s.RepoName = dbSess.RepoName }
					if s.IssueKey == "" { s.IssueKey = dbSess.IssueKey }
					if s.Branch == "" { s.Branch = dbSess.Branch }
				}
				merged = append(merged, s)
				seenInMerged[s.ID] = true
				// Update DB with latest discovered state
				_ = m.db.Save(ctx, &s)
			}
			
			// Then add any remaining DB sessions
			for id, s := range sessMap {
				if !seenInMerged[id] {
					merged = append(merged, s)
				}
			}
			
			m.sessions = merged
		}

		mainModel := NewModel(m.cfg, m.results, m.sessions, m.db, m.flowManager)
		if m.width > 0 && m.height > 0 {
			mainModel.SetSize(m.width, m.height)
		}
		return mainModel, mainModel.Init()
	}

	return m, nil
}

func (m StartupModel) View() string {
	var b strings.Builder
	b.WriteString("\n\n")

	b.WriteString(fmt.Sprintf("  %s %s\n\n", m.spinner.View(), "Running startup checks..."))

	// Fixed order display
	order := []string{"JIRA", "Git", "SSH"}
	for _, name := range order {
		res := m.checks[name]
		if res == nil {
			b.WriteString(fmt.Sprintf("  %s %-10s: pending...\n", statusStyle.Render("…"), name))
			continue
		}
		status := successStyle.Render("✓")
		if !res.Passed {
			status = failStyle.Render("✗")
		}
		b.WriteString(fmt.Sprintf("  %s %-10s: %s\n", status, res.Name, res.Message))
	}

	// Discovery status
	if m.discoveryDone {
		b.WriteString(fmt.Sprintf("  %s %-10s: %s\n", successStyle.Render("✓"), "Discover", "sessions loaded"))
	} else {
		b.WriteString(fmt.Sprintf("  %s %-10s: pending...\n", statusStyle.Render("…"), "Discover"))
	}

	return b.String()
}
