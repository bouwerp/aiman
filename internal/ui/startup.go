package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/mutagen"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/usecase"
)

type StartupModel struct {
	cfg           *config.Config
	doctor        *usecase.Doctor
	spinner       spinner.Model
	loadingMsg    string
	results       []usecase.CheckResult
	sessions      []domain.Session
	ready         bool
	err           error
	width, height int
	step          int
}

func NewStartupModel(cfg *config.Config, doctor *usecase.Doctor) StartupModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	return StartupModel{
		cfg:        cfg,
		doctor:     doctor,
		spinner:    s,
		loadingMsg: "Initializing Aiman...",
		step:       0,
	}
}

type checkResultMsg usecase.CheckResult
type discoveryResultMsg []domain.Session

func runNextCheck(m StartupModel) tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		switch m.step {
		case 0:
			return checkResultMsg(m.doctor.CheckJira(ctx))
		case 1:
			return checkResultMsg(m.doctor.CheckGit(ctx))
		case 2:
			return checkResultMsg(m.doctor.CheckSSH(ctx))
		case 3:
			if m.cfg.ActiveRemote == "" {
				return discoveryResultMsg{}
			}
			// Find remote config
			var remote config.Remote
			for _, r := range m.cfg.Remotes {
				if r.Host == m.cfg.ActiveRemote {
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
		default:
			return nil
		}
	}
}

func (m StartupModel) Init() tea.Cmd {
	return tea.Batch(
		m.spinner.Tick,
		runNextCheck(m),
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
		m.results = append(m.results, usecase.CheckResult(msg))
		m.step++
		return m, runNextCheck(m)
	case discoveryResultMsg:
		m.sessions = msg
		m.ready = true
		mainModel := NewModel(m.cfg, m.results, m.sessions)
		if m.width > 0 && m.height > 0 {
			mainModel.SetSize(m.width, m.height)
		}
		return mainModel, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}

	return m, nil
}

func (m StartupModel) View() string {
	var b strings.Builder
	b.WriteString("\n\n")
	
	currentAction := ""
	switch m.step {
	case 0:
		currentAction = "Checking JIRA connectivity..."
	case 1:
		currentAction = "Verifying GitHub credentials..."
	case 2:
		currentAction = "Probing remote dev servers..."
	case 3:
		currentAction = "Discovering remote sessions & repositories..."
	}

	b.WriteString(fmt.Sprintf("  %s %s\n\n", m.spinner.View(), currentAction))
	
	for _, res := range m.results {
		status := successStyle.Render("✓")
		if !res.Passed {
			status = failStyle.Render("✗")
		}
		b.WriteString(fmt.Sprintf("  %s %-10s: %s\n", status, res.Name, res.Message))
	}
	
	return b.String()
}
