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

// awsCredStatus represents the validity state of AWS credentials for a remote host.
type awsCredStatus int

const (
	awsCredStatusChecking  awsCredStatus = iota
	awsCredStatusValid
	awsCredStatusExpired
	awsCredStatusNotPushed // profile doesn't exist on the remote yet
	awsCredStatusNoConf    // remote has no delegation config
	awsCredStatusSSHError  // SSH connection failed (can't reach remote)
)

// awsHostEntry is one row in the credentials manager — one per user@host.
// Credentials are written to ~/.aws/credentials on the remote and are shared
// by all sessions running as the same user on that host.
type awsHostEntry struct {
	userAtHost    string        // e.g. "ubuntu@server.example.com"
	localProfile  string        // source_profile used locally to assume the role
	remoteProfile string        // profile name in remote ~/.aws/credentials
	status        awsCredStatus
	err           error
	// repSession is the session chosen to perform operations (probe / renew).
	// When multiple sessions exist for the same host we pick the most recently
	// updated one that has a known AWSConfig.
	repSession domain.Session
}

// AWSCredentialsModel lists one row per user@host and shows/manages AWS
// credential validity for remotes that have SyncCredentials enabled.
type AWSCredentialsModel struct {
	cfg      *config.Config
	db       domain.SessionRepository
	entries  []awsHostEntry
	cursor   int
	width    int
	height   int
	renewing map[string]bool // userAtHost keys currently being renewed
	message  string          // transient feedback line
}

// --- message types ---

type awsCredLoadedMsg struct{ entries []awsHostEntry }

type awsCredCheckResultMsg struct {
	userAtHost string
	status     awsCredStatus
	err        error
}

type awsCredRenewResultMsg struct {
	userAtHost string
	err        error
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

// loadAndCheck fetches all sessions, collapses them to one entry per user@host
// for remotes with SyncCredentials enabled, then fires credential probes.
func (m AWSCredentialsModel) loadAndCheck() tea.Cmd {
	return func() tea.Msg {
		sessions, err := m.db.List(context.Background())
		if err != nil {
			return awsCredLoadedMsg{entries: nil}
		}

		// Index remotes by host.
		type remoteInfo struct {
			remote config.Remote
			del    *config.AWSDelegation
		}
		remotes := map[string]remoteInfo{}
		for _, r := range m.cfg.Remotes {
			if r.AWSDelegation != nil && r.AWSDelegation.SyncCredentials {
				remotes[r.Host] = remoteInfo{remote: r, del: r.AWSDelegation}
			}
		}

		// Collapse sessions to one per user@host; prefer the session with the
		// most recently updated AWSConfig / AWSProfileName.
		type candidate struct {
			session   domain.Session
			userAtHost string
			ri        remoteInfo
		}
		best := map[string]candidate{} // keyed by userAtHost

		for _, s := range sessions {
			if s.Status == domain.SessionStatusCleanup || s.Status == domain.SessionStatusError {
				continue
			}
			remote, ok := resolveRemote(m.cfg, s)
			if !ok {
				continue
			}
			ri, has := remotes[remote.Host]
			if !has {
				continue
			}
			userAtHost := remote.Host
			if remote.User != "" {
				userAtHost = remote.User + "@" + remote.Host
			}
			prev, exists := best[userAtHost]
			// Prefer sessions that have AWSConfig, breaking ties by UpdatedAt.
			if !exists ||
				(s.AWSConfig != nil && prev.session.AWSConfig == nil) ||
				(s.AWSConfig != nil && s.UpdatedAt.After(prev.session.UpdatedAt)) ||
				s.UpdatedAt.After(prev.session.UpdatedAt) {
				best[userAtHost] = candidate{session: s, userAtHost: userAtHost, ri: ri}
			}
		}

		var entries []awsHostEntry
		for uah, c := range best {
			s := c.session
			del := c.ri.del

			localProfile := ""
			if s.AWSConfig != nil && s.AWSConfig.SourceProfile != "" {
				localProfile = s.AWSConfig.SourceProfile
			} else if del != nil && del.SourceProfile != "" {
				localProfile = del.SourceProfile
			}

			// Profile name is deterministic: "aiman-" + first 8 chars of session ID.
			remoteProfile := s.AWSProfileName
			if remoteProfile == "" {
				remoteProfile = "aiman-" + s.ID[:8]
			}

			entries = append(entries, awsHostEntry{
				userAtHost:    uah,
				localProfile:  localProfile,
				remoteProfile: remoteProfile,
				status:        awsCredStatusChecking,
				repSession:    s,
			})
		}

		sort.Slice(entries, func(i, j int) bool {
			return entries[i].userAtHost < entries[j].userAtHost
		})

		return awsCredLoadedMsg{entries: entries}
	}
}

// checkCredsCmd fires a credential probe for every entry currently in Checking state.
func (m AWSCredentialsModel) checkCredsCmd() tea.Cmd {
	var cmds []tea.Cmd
	for _, e := range m.entries {
		if e.status != awsCredStatusChecking {
			continue
		}
		uah := e.userAtHost
		s := e.repSession
		cfg := m.cfg
		profile := e.remoteProfile
		cmds = append(cmds, func() tea.Msg {
			remote, ok := resolveRemote(cfg, s)
			if !ok {
				return awsCredCheckResultMsg{userAtHost: uah, status: awsCredStatusNoConf}
			}
			mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			err := awsdelegation.CheckCredentials(ctx, mgr, profile)
			if err == nil {
				return awsCredCheckResultMsg{userAtHost: uah, status: awsCredStatusValid}
			}
			if errors.Is(err, awsdelegation.ErrProfileNotFound) {
				return awsCredCheckResultMsg{userAtHost: uah, status: awsCredStatusNotPushed, err: err}
			}
			if errors.Is(err, awsdelegation.ErrSSHFailure) {
				return awsCredCheckResultMsg{userAtHost: uah, status: awsCredStatusSSHError, err: err}
			}
			return awsCredCheckResultMsg{userAtHost: uah, status: awsCredStatusExpired, err: err}
		})
	}
	return tea.Batch(cmds...)
}

func (m AWSCredentialsModel) renewCmd(e awsHostEntry) tea.Cmd {
	cfg := m.cfg
	s := e.repSession
	uah := e.userAtHost
	return func() tea.Msg {
		remote, ok := resolveRemote(cfg, s)
		if !ok {
			return awsCredRenewResultMsg{userAtHost: uah, err: fmt.Errorf("no remote configured")}
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
			return awsCredRenewResultMsg{userAtHost: uah, err: fmt.Errorf("no AWS config found")}
		}
		_, err := usecase.PushSessionAWSCredentials(ctx, mgr, s.ID, awsCfg)
		return awsCredRenewResultMsg{userAtHost: uah, err: err}
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
			if e.userAtHost == msg.userAtHost {
				m.entries[i].status = msg.status
				m.entries[i].err = msg.err
				break
			}
		}
		return m, nil

	case awsCredRenewResultMsg:
		delete(m.renewing, msg.userAtHost)
		for i, e := range m.entries {
			if e.userAtHost == msg.userAtHost {
				if msg.err != nil {
					m.entries[i].status = awsCredStatusExpired
					m.entries[i].err = msg.err
					m.message = fmt.Sprintf("✗ Renew failed for %s: %v", e.userAtHost, msg.err)
				} else {
					// Re-probe to confirm rather than optimistically setting Valid.
					m.entries[i].status = awsCredStatusChecking
					m.entries[i].err = nil
					m.message = fmt.Sprintf("Renewed %s — re-checking…", e.userAtHost)
				}
				break
			}
		}
		return m, m.checkCredsCmd()

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
				if e.status != awsCredStatusNoConf && !m.renewing[e.userAtHost] {
					m.renewing[e.userAtHost] = true
					m.entries[m.cursor].status = awsCredStatusChecking
					m.message = fmt.Sprintf("Renewing %s…", e.userAtHost)
					return m, m.renewCmd(e)
				}
			}
		case "R":
			var cmds []tea.Cmd
			count := 0
			for i, e := range m.entries {
				if (e.status == awsCredStatusExpired || e.status == awsCredStatusNotPushed) && !m.renewing[e.userAtHost] {
					m.renewing[e.userAtHost] = true
					m.entries[i].status = awsCredStatusChecking
					cmds = append(cmds, m.renewCmd(e))
					count++
				}
			}
			if count > 0 {
				m.message = fmt.Sprintf("Renewing %d host(s)…", count)
				return m, tea.Batch(cmds...)
			}
			m.message = "No expired or unprovisioned credentials to renew."
		case "c":
			for i := range m.entries {
				m.entries[i].status = awsCredStatusChecking
				m.entries[i].err = nil
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
	warnStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	errorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	selectedBg := lipgloss.NewStyle().Background(lipgloss.Color("236"))
	headerStyle := dimStyle.Copy()

	b.WriteString("\n  " + titleStyle.Render("AWS Credential Status") + "\n\n")

	if len(m.entries) == 0 {
		b.WriteString(dimStyle.Render("  No remotes with AWS delegation found.\n"))
		b.WriteString(dimStyle.Render("  (Remotes need aws_delegation.sync_credentials: true in config)\n"))
	} else {
		hdr := fmt.Sprintf("  %-12s  %-30s  %-20s  %-20s",
			"Status", "Host", "Local profile", "Remote profile")
		b.WriteString(headerStyle.Render(hdr) + "\n")
		b.WriteString(headerStyle.Render("  "+strings.Repeat("─", 88)) + "\n")

		for i, e := range m.entries {
			var statusStr string
			switch e.status {
			case awsCredStatusValid:
				statusStr = validStyle.Render("✓ Valid     ")
			case awsCredStatusExpired:
				statusStr = expiredStyle.Render("✗ Expired   ")
			case awsCredStatusChecking:
				if m.renewing[e.userAtHost] {
					statusStr = warnStyle.Render("⟳ Renewing  ")
				} else {
					statusStr = warnStyle.Render("· Checking  ")
				}
			case awsCredStatusNotPushed:
				statusStr = warnStyle.Render("! Not pushed")
			case awsCredStatusNoConf:
				statusStr = dimStyle.Render("? No config ")
			case awsCredStatusSSHError:
				statusStr = errorStyle.Render("⚠ SSH error ")
			}

			localP := e.localProfile
			if localP == "" {
				localP = "—"
			}
			remoteP := e.remoteProfile
			if remoteP == "" {
				remoteP = "—"
			}

			line := fmt.Sprintf("  %s  %-30s  %-20s  %-20s",
				statusStr,
				truncateRunes(e.userAtHost, 30),
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
	b.WriteString(helpStyle.Render("  r renew selected  •  R renew all expired  •  c re-check all  •  ESC back") + "\n")

	return b.String()
}
