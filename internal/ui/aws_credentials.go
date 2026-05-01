package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/usecase"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// awsCredStatus represents the known validity state of a session's AWS credentials.
type awsCredStatus int

const (
	awsCredStatusChecking awsCredStatus = iota
	awsCredStatusValid
	awsCredStatusExpired
	awsCredStatusNoAWS  // session has no AWS profile
	awsCredStatusNoConf // session has no delegation config
)

// awsCredEntry is one row in the credentials manager list.
type awsCredEntry struct {
	session domain.Session
	status  awsCredStatus
	err     error
}

// AWSCredentialsModel is the standalone view that lists all sessions with their
// AWS credential status and allows individual or bulk renewal.
type AWSCredentialsModel struct {
	cfg      *config.Config
	db       domain.SessionRepository
	entries  []awsCredEntry
	cursor   int
	width    int
	height   int
	renewing map[string]bool // session IDs currently being renewed
	message  string          // transient feedback line
}

// --- message types ---

type awsCredLoadedMsg struct{ entries []awsCredEntry }

type awsCredCheckResultMsg struct {
	sessionID string
	status    awsCredStatus
	err       error
}

type awsCredRenewResultMsg struct {
	sessionID string
	err       error
}

// --- constructor ---

func NewAWSCredentialsModel(cfg *config.Config, db domain.SessionRepository) AWSCredentialsModel {
	return AWSCredentialsModel{
		cfg:      cfg,
		db:       db,
		renewing: make(map[string]bool),
	}
}

// --- tea.Model ---

func (m AWSCredentialsModel) Init() tea.Cmd {
	return m.loadAndCheck()
}

func (m AWSCredentialsModel) loadAndCheck() tea.Cmd {
	return func() tea.Msg {
		sessions, err := m.db.List(context.Background())
		if err != nil {
			return awsCredLoadedMsg{entries: nil}
		}
		var entries []awsCredEntry
		for _, s := range sessions {
			if s.Status == domain.SessionStatusCleanup || s.Status == domain.SessionStatusError {
				continue
			}
			if s.AWSProfileName == "" {
				entries = append(entries, awsCredEntry{session: s, status: awsCredStatusNoAWS})
				continue
			}
			// Determine if delegation config is resolvable.
			hasCfg := s.AWSConfig != nil
			if !hasCfg {
				for _, r := range m.cfg.Remotes {
					if r.Host == s.RemoteHost && r.AWSDelegation != nil && r.AWSDelegation.SyncCredentials {
						hasCfg = true
						break
					}
				}
			}
			if !hasCfg {
				entries = append(entries, awsCredEntry{session: s, status: awsCredStatusNoConf})
				continue
			}
			entries = append(entries, awsCredEntry{session: s, status: awsCredStatusChecking})
		}
		return awsCredLoadedMsg{entries: entries}
	}
}

// checkCredsCmd fires credential checks for all entries that are in Checking state.
func (m AWSCredentialsModel) checkCredsCmd() tea.Cmd {
	var cmds []tea.Cmd
	for _, e := range m.entries {
		if e.status != awsCredStatusChecking {
			continue
		}
		s := e.session
		cfg := m.cfg
		cmds = append(cmds, func() tea.Msg {
			remote, ok := resolveRemote(cfg, s)
			if !ok {
				return awsCredCheckResultMsg{sessionID: s.ID, status: awsCredStatusNoConf}
			}
			mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			err := awsdelegation.CheckCredentials(ctx, mgr, s.AWSProfileName)
			if err != nil {
				return awsCredCheckResultMsg{sessionID: s.ID, status: awsCredStatusExpired, err: err}
			}
			return awsCredCheckResultMsg{sessionID: s.ID, status: awsCredStatusValid}
		})
	}
	return tea.Batch(cmds...)
}

func (m AWSCredentialsModel) renewCmd(s domain.Session) tea.Cmd {
	cfg := m.cfg
	return func() tea.Msg {
		remote, ok := resolveRemote(cfg, s)
		if !ok {
			return awsCredRenewResultMsg{sessionID: s.ID, err: fmt.Errorf("no remote configured")}
		}
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		var awsCfg *domain.AWSConfig
		if s.AWSConfig != nil {
			awsCfg = s.AWSConfig
		} else if remote.AWSDelegation != nil {
			d := remote.AWSDelegation
			awsCfg = &domain.AWSConfig{
				SourceProfile:   d.SourceProfile,
				RoleName:        d.RoleName,
				AccountID:       d.AccountID,
				Region:          d.Region,
				Regions:         d.Regions,
				SessionPolicy:   d.SessionPolicy,
				DurationSeconds: d.DurationSeconds,
			}
		}
		if awsCfg == nil {
			return awsCredRenewResultMsg{sessionID: s.ID, err: fmt.Errorf("no AWS config found")}
		}
		_, err := usecase.PushSessionAWSCredentials(ctx, mgr, s.ID, awsCfg)
		return awsCredRenewResultMsg{sessionID: s.ID, err: err}
	}
}

func (m AWSCredentialsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case awsCredLoadedMsg:
		m.entries = msg.entries
		if m.cursor >= len(m.entries) && len(m.entries) > 0 {
			m.cursor = len(m.entries) - 1
		}
		return m, m.checkCredsCmd()

	case awsCredCheckResultMsg:
		for i, e := range m.entries {
			if e.session.ID == msg.sessionID {
				m.entries[i].status = msg.status
				m.entries[i].err = msg.err
				break
			}
		}
		return m, nil

	case awsCredRenewResultMsg:
		delete(m.renewing, msg.sessionID)
		for i, e := range m.entries {
			if e.session.ID == msg.sessionID {
				if msg.err != nil {
					m.entries[i].status = awsCredStatusExpired
					m.entries[i].err = msg.err
					m.message = fmt.Sprintf("✗ Renew failed for %s: %v", e.session.TmuxSession, msg.err)
				} else {
					m.entries[i].status = awsCredStatusValid
					m.entries[i].err = nil
					m.message = fmt.Sprintf("✓ Renewed %s", e.session.TmuxSession)
				}
				break
			}
		}
		return m, nil

	case tea.KeyMsg:
		m.message = ""
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.entries)-1 {
				m.cursor++
			}
		case "r":
			if m.cursor < len(m.entries) {
				e := m.entries[m.cursor]
				if e.status != awsCredStatusNoAWS && e.status != awsCredStatusNoConf && !m.renewing[e.session.ID] {
					m.renewing[e.session.ID] = true
					m.entries[m.cursor].status = awsCredStatusChecking
					m.message = fmt.Sprintf("Renewing %s…", e.session.TmuxSession)
					return m, m.renewCmd(e.session)
				}
			}
		case "R":
			// Renew all expired sessions.
			var cmds []tea.Cmd
			count := 0
			for i, e := range m.entries {
				if e.status == awsCredStatusExpired && !m.renewing[e.session.ID] {
					m.renewing[e.session.ID] = true
					m.entries[i].status = awsCredStatusChecking
					cmds = append(cmds, m.renewCmd(e.session))
					count++
				}
			}
			if count > 0 {
				m.message = fmt.Sprintf("Renewing %d expired session(s)…", count)
				return m, tea.Batch(cmds...)
			}
			m.message = "No expired sessions to renew."
		case "c":
			// Re-check all.
			for i := range m.entries {
				if m.entries[i].status != awsCredStatusNoAWS && m.entries[i].status != awsCredStatusNoConf {
					m.entries[i].status = awsCredStatusChecking
					m.entries[i].err = nil
				}
			}
			m.message = "Re-checking all credentials…"
			return m, m.checkCredsCmd()
		}
	}
	return m, nil
}

func (m AWSCredentialsModel) View() string {
	var b strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	validStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	expiredStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	checkingStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	selectedStyle := lipgloss.NewStyle().Background(lipgloss.Color("236"))

	b.WriteString("\n  " + titleStyle.Render("AWS Credential Status") + "\n\n")

	if len(m.entries) == 0 {
		b.WriteString(dimStyle.Render("  No sessions found.\n"))
	} else {
		for i, e := range m.entries {
			label := e.session.TmuxSession
			if label == "" {
				label = e.session.ID[:8]
			}
			profile := e.session.AWSProfileName
			if profile == "" {
				profile = "—"
			}

			var statusStr string
			switch e.status {
			case awsCredStatusValid:
				statusStr = validStyle.Render("✓ Valid  ")
			case awsCredStatusExpired:
				statusStr = expiredStyle.Render("✗ Expired")
			case awsCredStatusChecking:
				if m.renewing[e.session.ID] {
					statusStr = checkingStyle.Render("⟳ Renewing")
				} else {
					statusStr = checkingStyle.Render("· Checking")
				}
			case awsCredStatusNoAWS:
				statusStr = dimStyle.Render("— No AWS ")
			case awsCredStatusNoConf:
				statusStr = dimStyle.Render("? No config")
			}

			remote := e.session.RemoteHost
			line := fmt.Sprintf("  %s  %-30s  %-24s  %s",
				statusStr,
				truncateRunes(label, 30),
				truncateRunes(profile, 24),
				dimStyle.Render(truncateRunes(remote, 28)),
			)
			if i == m.cursor {
				line = selectedStyle.Render(line)
			}
			b.WriteString(line + "\n")
		}
	}

	b.WriteString("\n")
	if m.message != "" {
		b.WriteString("  " + m.message + "\n\n")
	}

	helpStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	b.WriteString(helpStyle.Render("  r renew selected  •  R renew all expired  •  c re-check all  •  ESC back") + "\n")

	return b.String()
}
