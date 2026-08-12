package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/mutagen"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/usecase"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// aimanPixels is a 7-row pixel bitmap for the AIMAN logo.
// 'X' = filled pixel, anything else = empty.
// Letter widths are 5px each with 1px gaps: total 29px wide.
//
//	A (5)  I (5)  M (5)  A (5)  N (5)
var aimanPixels = []string{
	".XXX. .XXX. X...X .XXX. X...X",
	"X...X ..X.. XX.XX X...X XX..X",
	"X...X ..X.. X.X.X X...X X.X.X",
	"XXXXX ..X.. X...X XXXXX X..XX",
	"X...X ..X.. X...X X...X X...X",
	"X...X ..X.. X...X X...X X...X",
	"X...X .XXX. X...X X...X X...X",
}

var (
	logoTagline = "ai coding agent manager"
	logoPalette = []lipgloss.Color{
		"#FF6B9D", // pink
		"#D45BFF", // purple
		"#7B9EFF", // blue
		"#5BFFE8", // teal
		"#5BFFA0", // green
		"#FFD95B", // gold
		"#FF9A6B", // orange
	}
)

type logoTickMsg struct{}

func logoTick() tea.Cmd {
	return tea.Tick(90*time.Millisecond, func(_ time.Time) tea.Msg {
		return logoTickMsg{}
	})
}

type StartupModel struct {
	logoFrame       int
	version         string
	cfg             *config.Config
	doctor          *usecase.Doctor
	db              domain.SessionRepository
	flowManager     *usecase.FlowManager
	intelligence    domain.IntelligenceProvider
	snapshotManager *usecase.SnapshotManager
	Program         *tea.Program
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

func NewStartupModel(cfg *config.Config, doctor *usecase.Doctor, db domain.SessionRepository, flowManager *usecase.FlowManager, intelligence domain.IntelligenceProvider, snapshotManager *usecase.SnapshotManager, version string) StartupModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	return StartupModel{
		logoFrame:       0,
		version:         version,
		cfg:             cfg,
		doctor:          doctor,
		db:              db,
		flowManager:     flowManager,
		intelligence:    intelligence,
		snapshotManager: snapshotManager,
		spinner:         s,
		loadingMsg:      "Initializing Aiman...",
		checks:          make(map[string]*usecase.CheckResult),
		// The three doctor checks (JIRA, Git, SSH). Discovery runs concurrently
		// but is not part of the gate.
		pending: 3,
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

// discoveryTimeout caps the whole startup scan. Without it a wedged remote
// holds the splash screen open indefinitely rather than degrading to whatever
// the database already knows.
const discoveryTimeout = 3 * time.Minute

func runDiscovery(cfg *config.Config) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
		defer cancel()
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

func loadConfiguredSessions(ctx context.Context, cfg *config.Config, db domain.SessionRepository) ([]domain.Session, error) {
	if db == nil {
		return nil, nil
	}
	sessions, err := db.List(ctx)
	if err != nil {
		return nil, err
	}
	filtered := make([]domain.Session, 0, len(sessions))
	for _, s := range sessions {
		if s.RemoteHost != "" {
			if _, ok := resolveRemote(cfg, s); !ok {
				_ = db.Delete(ctx, s.ID)
				continue
			}
		}
		filtered = append(filtered, s)
	}
	return filtered, nil
}

func (m StartupModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		logoTick(),
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
	case SetProgramMsg:
		m.Program = msg.Program
		return m, nil
	case logoTickMsg:
		m.logoFrame++
		return m, logoTick()
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
		// Recorded but deliberately not counted against m.pending: discovery
		// must not gate the handoff. If it lands first the result is replayed
		// into the dashboard below; otherwise the dashboard receives it directly.
		m.sessions = msg.sessions
		m.scannedHosts = msg.scannedHosts
		m.discoveryDone = true
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}

	if m.pending <= 0 {
		m.ready = true

		// Hand off as soon as the doctor checks land, using whatever the database
		// already knows. Discovery is not part of this gate: it costs a remote
		// round trip and previously held the splash screen open for the whole
		// scan. Its result is applied by the dashboard when it arrives.
		ctx := context.Background()
		dbSessions, err := loadConfiguredSessions(ctx, m.cfg, m.db)
		startupLogs := []string{
			fmt.Sprintf("[startup] db=%d discoveryDone=%v err=%v", len(dbSessions), m.discoveryDone, err),
		}
		for _, s := range dbSessions {
			startupLogs = append(startupLogs, fmt.Sprintf("[startup] db:   id=%s tmux=%s worktree=%s", s.ID, s.TmuxSession, s.WorktreePath))
		}

		mainModel := NewModel(m.cfg, m.results, dbSessions, m.db, m.flowManager, m.intelligence, m.snapshotManager, startupLogs...)
		mainModel.version = m.version
		mainModel.Program = m.Program
		mainModel.discoveryPending = !m.discoveryDone
		if m.width > 0 && m.height > 0 {
			mainModel.SetSize(m.width, m.height)
		}

		cmds := []tea.Cmd{mainModel.Init()}
		if m.discoveryDone {
			// Discovery beat the checks. Replay it into the dashboard rather than
			// merging here, so there is a single merge implementation
			// (Model.applyDiscoveryResult) instead of two that can drift.
			result := discoveryResultMsg{sessions: m.sessions, scannedHosts: m.scannedHosts}
			cmds = append(cmds, func() tea.Msg { return result })
		}
		return mainModel, tea.Batch(cmds...)
	}

	return m, nil
}

func (m StartupModel) renderLogo() string {
	n := len(logoPalette)
	rows := aimanPixels
	numRows := len(rows)

	maxCol := 0
	for _, r := range rows {
		if len(r) > maxCol {
			maxCol = len(r)
		}
	}

	var b strings.Builder
	// Process pixel rows in pairs → one half-block character row each.
	// No manual indent here — lipgloss.Place handles centering.
	for i := 0; i < numRows; i += 2 {
		topRow := rows[i]
		bottomRow := ""
		if i+1 < numRows {
			bottomRow = rows[i+1]
		}

		for col := 0; col < maxCol; col++ {
			topOn := col < len(topRow) && topRow[col] == 'X'
			bottomOn := col < len(bottomRow) && bottomRow[col] == 'X'

			// Wave sweeps left→right: one colour step per pixel column, offset by frame
			topIdx := ((col-m.logoFrame)%n + n*100) % n
			// bottom row is one pixel row lower; shift colour slightly for depth
			botIdx := ((col-m.logoFrame+1)%n + n*100) % n

			switch {
			case !topOn && !bottomOn:
				b.WriteRune(' ')
			case topOn && bottomOn && topIdx == botIdx:
				b.WriteString(lipgloss.NewStyle().Foreground(logoPalette[topIdx]).Render("█"))
			case topOn && bottomOn:
				b.WriteString(lipgloss.NewStyle().
					Foreground(logoPalette[topIdx]).
					Background(logoPalette[botIdx]).
					Render("▀"))
			case topOn:
				b.WriteString(lipgloss.NewStyle().Foreground(logoPalette[topIdx]).Render("▀"))
			default:
				b.WriteString(lipgloss.NewStyle().Foreground(logoPalette[botIdx]).Render("▄"))
			}
		}
		b.WriteRune('\n')
	}
	return b.String()
}

func (m StartupModel) buildContent() string {
	// Logo — raw, no indent; Place will center the whole block
	logo := m.renderLogo()

	// Tagline — centred to match the logo's visual width (29 pixel-cols)
	logoVisualWidth := 29
	taglineStyle := lipgloss.NewStyle().
		Foreground(logoPalette[m.logoFrame%len(logoPalette)]).
		Italic(true)
	tagline := lipgloss.NewStyle().
		Width(logoVisualWidth).
		Align(lipgloss.Center).
		Render(taglineStyle.Render(logoTagline))

	// Version
	versionStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	version := lipgloss.NewStyle().
		Width(logoVisualWidth).
		Align(lipgloss.Center).
		Render(versionStyle.Render(m.version))

	// Checks — left-aligned lines inside a fixed-width block; Place centers the block
	var checks strings.Builder
	checks.WriteString(fmt.Sprintf("%s %s\n\n", m.spinner.View(), "Running startup checks..."))

	order := []string{"JIRA", "Git", "SSH"}
	for _, name := range order {
		res := m.checks[name]
		if res == nil {
			checks.WriteString(fmt.Sprintf("%s %-10s: pending...\n", statusStyle.Render("…"), name))
			continue
		}
		status := successStyle.Render("✓")
		if !res.Passed {
			status = failStyle.Render("✗")
		}
		checks.WriteString(fmt.Sprintf("%s %-10s: %s\n", status, res.Name, res.Message))
	}
	if m.discoveryDone {
		checks.WriteString(fmt.Sprintf("%s %-10s: %s\n", successStyle.Render("✓"), "Discover", "sessions loaded"))
	} else {
		checks.WriteString(fmt.Sprintf("%s %-10s: pending...\n", statusStyle.Render("…"), "Discover"))
	}

	return logo + tagline + "\n" + version + "\n\n" + checks.String()
}

func (m StartupModel) View() string {
	content := m.buildContent()
	if m.width > 0 && m.height > 0 {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
	}
	return "\n" + content
}
