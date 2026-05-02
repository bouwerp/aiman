package ui

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
	awsCredStatusNotFound // profile doesn't exist on the remote yet
	awsCredStatusNoConf   // remote has no delegation config
)

// awsCredEntry is one selectable row in the credentials manager list.
type awsCredEntry struct {
	session       domain.Session
	status        awsCredStatus
	err           error
	localProfile  string // source profile name on the local machine
	remoteProfile string // aiman-xxxx profile on the remote machine
	userAtHost    string // user@host grouping key
}

// AWSCredentialsModel is the standalone view that lists all sessions with their
// AWS credential status, grouped by remote host, and allows individual or bulk renewal.
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

// loadAndCheck fetches all sessions and builds entries only for remotes that
// have AWS delegation with SyncCredentials enabled.
func (m AWSCredentialsModel) loadAndCheck() tea.Cmd {
	return func() tea.Msg {
		sessions, err := m.db.List(context.Background())
		if err != nil {
			return awsCredLoadedMsg{entries: nil}
		}

		// Index remotes by host for quick lookup.
		remoteDel := map[string]*config.AWSDelegation{}
		for _, r := range m.cfg.Remotes {
			if r.AWSDelegation != nil && r.AWSDelegation.SyncCredentials {
				remoteDel[r.Host] = r.AWSDelegation
			}
		}

		var entries []awsCredEntry
		for _, s := range sessions {
			if s.Status == domain.SessionStatusCleanup || s.Status == domain.SessionStatusError {
				continue
			}

			remote, ok := resolveRemote(m.cfg, s)
			if !ok {
				continue
			}

			del, hasDelegation := remoteDel[remote.Host]
			if !hasDelegation {
				// Remote has no AWS delegation — skip entirely.
				continue
			}

			// Determine the local (source) profile name.
			localProfile := ""
			if s.AWSConfig != nil && s.AWSConfig.SourceProfile != "" {
				localProfile = s.AWSConfig.SourceProfile
			} else if del != nil && del.SourceProfile != "" {
				localProfile = del.SourceProfile
			}

			// The remote profile name is deterministic even if the DB field was lost.
			remoteProfile := s.AWSProfileName
			if remoteProfile == "" {
				remoteProfile = "aiman-" + s.ID[:8]
			}

			userAtHost := remote.Host
			if remote.User != "" {
				userAtHost = remote.User + "@" + remote.Host
			}

			entries = append(entries, awsCredEntry{
				session:       s,
				status:        awsCredStatusChecking,
				localProfile:  localProfile,
				remoteProfile: remoteProfile,
				userAtHost:    userAtHost,
			})
		}

		// Sort by user@host then session name so grouping is stable.
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].userAtHost != entries[j].userAtHost {
				return entries[i].userAtHost < entries[j].userAtHost
			}
			return entries[i].session.TmuxSession < entries[j].session.TmuxSession
		})

		return awsCredLoadedMsg{entries: entries}
	}
}

// checkCredsCmd fires credential checks for all entries in Checking state.
func (m AWSCredentialsModel) checkCredsCmd() tea.Cmd {
	var cmds []tea.Cmd
	for _, e := range m.entries {
		if e.status != awsCredStatusChecking {
			continue
		}
		s := e.session
		cfg := m.cfg
		profile := e.remoteProfile
		cmds = append(cmds, func() tea.Msg {
			remote, ok := resolveRemote(cfg, s)
			if !ok {
				return awsCredCheckResultMsg{sessionID: s.ID, status: awsCredStatusNoConf}
			}
			mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			err := awsdelegation.CheckCredentials(ctx, mgr, profile)
			if err == nil {
				return awsCredCheckResultMsg{sessionID: s.ID, status: awsCredStatusValid}
			}
			if errors.Is(err, awsdelegation.ErrProfileNotFound) {
				return awsCredCheckResultMsg{sessionID: s.ID, status: awsCredStatusNotFound, err: err}
			}
			return awsCredCheckResultMsg{sessionID: s.ID, status: awsCredStatusExpired, err: err}
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
					// Update the remote profile name in case it changed.
					if m.entries[i].remoteProfile == "" {
						m.entries[i].remoteProfile = "aiman-" + e.session.ID[:8]
					}
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
				if e.status != awsCredStatusNoConf && !m.renewing[e.session.ID] {
					m.renewing[e.session.ID] = true
					m.entries[m.cursor].status = awsCredStatusChecking
					m.message = fmt.Sprintf("Renewing %s…", e.session.TmuxSession)
					return m, m.renewCmd(e.session)
				}
			}
		case "R":
			// Renew all expired and not-found sessions.
			var cmds []tea.Cmd
			count := 0
			for i, e := range m.entries {
				if (e.status == awsCredStatusExpired || e.status == awsCredStatusNotFound) && !m.renewing[e.session.ID] {
					m.renewing[e.session.ID] = true
					m.entries[i].status = awsCredStatusChecking
					cmds = append(cmds, m.renewCmd(e.session))
					count++
				}
			}
			if count > 0 {
				m.message = fmt.Sprintf("Renewing %d session(s)…", count)
				return m, tea.Batch(cmds...)
			}
			m.message = "No expired or unprovisioned sessions to renew."
		case "c":
			// Re-check all sessions that have a remote profile.
			for i := range m.entries {
				if m.entries[i].remoteProfile != "" {
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
	hostStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))
	validStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	expiredStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	selectedBg := lipgloss.NewStyle().Background(lipgloss.Color("236"))
	headerStyle := dimStyle.Copy()

	b.WriteString("\n  " + titleStyle.Render("AWS Credential Status") + "\n\n")

	if len(m.entries) == 0 {
		b.WriteString(dimStyle.Render("  No sessions with AWS delegation found.\n"))
		b.WriteString(dimStyle.Render("  (Remotes need aws_delegation.sync_credentials: true in config)\n"))
	} else {
		// Column header
		hdr := fmt.Sprintf("  %-12s  %-28s  %-20s  %-20s",
			"Status", "Session", "Local profile", "Remote profile")
		b.WriteString(headerStyle.Render(hdr) + "\n")

		lastHost := ""
		for i, e := range m.entries {
			groupKey := e.userAtHost
			if groupKey == "" {
				groupKey = e.session.RemoteHost
			}
			if groupKey == "" {
				groupKey = "(unknown host)"
			}
			if groupKey != lastHost {
				if lastHost != "" {
					b.WriteString("\n")
				}
				b.WriteString("  " + hostStyle.Render("⬡ "+groupKey) + "\n")
				lastHost = groupKey
			}

			var statusStr string
			switch e.status {
			case awsCredStatusValid:
				statusStr = validStyle.Render("✓ Valid     ")
			case awsCredStatusExpired:
				statusStr = expiredStyle.Render("✗ Expired   ")
			case awsCredStatusChecking:
				if m.renewing[e.session.ID] {
					statusStr = warnStyle.Render("⟳ Renewing  ")
				} else {
					statusStr = warnStyle.Render("· Checking  ")
				}
			case awsCredStatusNotFound:
				statusStr = warnStyle.Render("! Not pushed")
			case awsCredStatusNoConf:
				statusStr = dimStyle.Render("? No config ")
			}

			label := e.session.TmuxSession
			if label == "" {
				label = e.session.ID[:8]
			}
			localP := e.localProfile
			if localP == "" {
				localP = "—"
			}
			remoteP := e.remoteProfile
			if remoteP == "" {
				remoteP = "—"
			}

			line := fmt.Sprintf("  %s  %-28s  %-20s  %-20s",
				statusStr,
				truncateRunes(label, 28),
				truncateRunes(localP, 20),
				truncateRunes(remoteP, 20),
			)
			if i == m.cursor {
				line = selectedBg.Render(line)
			}
			b.WriteString(line + "\n")
		}
	}

	b.WriteString("\n")
	if m.message != "" {
		b.WriteString("  " + m.message + "\n\n")
	}

	helpStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	b.WriteString(helpStyle.Render("  r renew selected  •  R renew all expired/unprovisioned  •  c re-check all  •  ESC back") + "\n")

	return b.String()
}
