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
	cfg             *config.Config
	doctor          *usecase.Doctor
	db              domain.SessionRepository
	flowManager     *usecase.FlowManager
	intelligence    domain.IntelligenceProvider
	snapshotManager *usecase.SnapshotManager
	spinner         spinner.Model
	loadingMsg      string
	results         []usecase.CheckResult
	sessions        []domain.Session
	scannedHosts    map[string]bool
	ready           bool
	width, height   int
	checks          map[string]*usecase.CheckResult
	discoveryDone   bool
	pending         int
}

func NewStartupModel(cfg *config.Config, doctor *usecase.Doctor, db domain.SessionRepository, flowManager *usecase.FlowManager, intelligence domain.IntelligenceProvider, snapshotManager *usecase.SnapshotManager) StartupModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	return StartupModel{
		cfg:             cfg,
		doctor:          doctor,
		db:              db,
		flowManager:     flowManager,
		intelligence:    intelligence,
		snapshotManager: snapshotManager,
		spinner:         s,
		loadingMsg:      "Initializing Aiman...",
		checks:          make(map[string]*usecase.CheckResult),
		pending:         4,
	}
}

type checkResultMsg usecase.CheckResult

type discoveryResult struct {
	sessions     []domain.Session
	scannedHosts map[string]bool // remotes that were successfully connected and scanned
}

type discoveryResultMsg discoveryResult

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
		result := discoveryResult{scannedHosts: make(map[string]bool)}
		if len(cfg.Remotes) == 0 {
			return discoveryResultMsg(result)
		}

		for _, remote := range config.UniqueRemotes(cfg.Remotes) {
			if remote.Host == "" {
				continue
			}
			mgr := ssh.NewManager(ssh.Config{
				Host: remote.Host,
				User: remote.User,
				Root: remote.Root,
			})
			if err := mgr.Connect(ctx); err != nil {
				// Skip unreachable remotes — don't block startup
				continue
			}
			result.scannedHosts[remote.Host] = true
			discoverer := usecase.NewSessionDiscoverer(mgr, mutagen.NewEngine())
			sessions, _ := discoverer.Discover(ctx, remote.Host)
			result.sessions = append(result.sessions, sessions...)
		}
		return discoveryResultMsg(result)
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
		m.sessions = msg.sessions
		m.scannedHosts = msg.scannedHosts
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
		startupLogs := []string{
			fmt.Sprintf("[startup] discovered=%d db=%d err=%v", len(m.sessions), len(dbSessions), err),
		}
		for _, s := range m.sessions {
			startupLogs = append(startupLogs, fmt.Sprintf("[startup] disc: id=%s tmux=%s worktree=%s", s.ID, s.TmuxSession, s.WorktreePath))
		}
		for _, s := range dbSessions {
			startupLogs = append(startupLogs, fmt.Sprintf("[startup] db:   id=%s tmux=%s worktree=%s", s.ID, s.TmuxSession, s.WorktreePath))
		}
		// Merge logic (always runs — dedup discovered sessions even if DB failed):
		// 1. Start with discovered sessions (most accurate current state)
		// 2. Link them to DB entries if IDs match to preserve user-chosen metadata
		// 3. Add any DB sessions that weren't discovered (orphaned from perspective of this machine)
		//
		// Key principle: DB fields that represent user intent (WorkingDirectory,
		// RepoName in org/repo format, IssueKey, Branch, AgentName) are
		// authoritative. Discovery fields that represent live state (Status,
		// TmuxSession, WorktreePath) should win.
		sessMap := make(map[string]domain.Session)
		for _, s := range dbSessions {
			sessMap[s.ID] = s
		}

		merged := []domain.Session{}
		seenInMerged := make(map[string]bool)  // keyed by ID
		seenTmuxNames := make(map[string]bool) // keyed by RemoteHost + TmuxSession

		// First, process all discovered sessions
		for _, s := range m.sessions {
			if seenInMerged[s.ID] {
				continue // same ID seen already
			}
			tmuxKey := ""
			if s.TmuxSession != "" {
				tmuxKey = s.RemoteHost + "\x00" + s.TmuxSession
			}
			if tmuxKey != "" && seenTmuxNames[tmuxKey] {
				continue // same host + tmux session — phantom duplicate
			}
			if dbSess, ok := sessMap[s.ID]; ok {
				// WorkingDirectory from DB is the user's chosen subdirectory
				// scope. Discovery gets tmux CWD which drifts as the agent
				// navigates. Always prefer the DB value.
				if dbSess.WorkingDirectory != "" {
					s.WorkingDirectory = dbSess.WorkingDirectory
				}
				// Prefer DB's org/repo format over discovery's basename
				if dbSess.RepoName != "" && (s.RepoName == "" || (!strings.Contains(s.RepoName, "/") && strings.Contains(dbSess.RepoName, "/"))) {
					s.RepoName = dbSess.RepoName
				}
				if s.IssueKey == "" {
					s.IssueKey = dbSess.IssueKey
				}
				if s.Branch == "" {
					s.Branch = dbSess.Branch
				}
				if s.AgentName == "" {
					s.AgentName = dbSess.AgentName
				}
				if s.MutagenSyncID == "" {
					s.MutagenSyncID = dbSess.MutagenSyncID
				}
				if s.LocalPath == "" {
					s.LocalPath = dbSess.LocalPath
				}
			}
			merged = append(merged, s)
			seenInMerged[s.ID] = true
			if tmuxKey != "" {
				seenTmuxNames[tmuxKey] = true
			}
			// Update DB with latest merged state
			_ = m.db.Save(ctx, &s)
		}

		// Then add any remaining DB sessions from remotes we couldn't reach.
		// Sessions from remotes we DID scan but didn't discover are dead — skip them.
		for id, s := range sessMap {
			if seenInMerged[id] {
				continue
			}
			tk := ""
			if s.TmuxSession != "" {
				tk = s.RemoteHost + "\x00" + s.TmuxSession
			}
			if tk != "" && seenTmuxNames[tk] {
				continue
			}
			// Skip if the session's remote was successfully scanned — it's dead
			if s.RemoteHost != "" && m.scannedHosts[s.RemoteHost] {
				continue
			}
			merged = append(merged, s)
			seenInMerged[id] = true
			if tk != "" {
				seenTmuxNames[tk] = true
			}
		}

		m.sessions = merged

		startupLogs = append(startupLogs, fmt.Sprintf("[startup] merged=%d", len(m.sessions)))
		for _, s := range m.sessions {
			startupLogs = append(startupLogs, fmt.Sprintf("[startup] final: id=%s tmux=%s worktree=%s", s.ID, s.TmuxSession, s.WorktreePath))
		}

		mainModel := NewModel(m.cfg, m.results, m.sessions, m.db, m.flowManager, m.intelligence, m.snapshotManager, startupLogs...)
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
