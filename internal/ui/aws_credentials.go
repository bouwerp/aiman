package ui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// awsCredStatus represents the validity state of AWS credentials for a remote host.
type awsCredStatus int

const (
	awsCredStatusChecking awsCredStatus = iota
	awsCredStatusValid
	awsCredStatusExpired
	awsCredStatusNotPushed // profile doesn't exist on the remote yet
	awsCredStatusNoConf    // remote has no delegation config
	awsCredStatusSSHError  // SSH connection failed (can't reach remote)
)

// awsHostEntry is one row in the credentials manager — one per (user@host, profile) pair.
// Credentials are written to ~/.aws/credentials on the remote and are shared
// by all sessions running as the same user on that host. The profile name is
// taken from AWSDelegation.Profile (not the per-session aiman-XXXX name).
type awsHostEntry struct {
	key           string // unique key: "user@host|profile" — used for message routing
	userAtHost    string // e.g. "ubuntu@server.example.com"
	localProfile  string // source_profile used locally to assume the role
	remoteProfile string // profile name in remote ~/.aws/credentials
	status        awsCredStatus
	err           error
	// del is the delegation config for this remote, used for renewal.
	del *config.AWSDelegation
	// remote is the resolved config.Remote for SSH operations.
	remote config.Remote
}

// AWSCredentialsModel lists one row per (user@host, profile) pair and shows/manages AWS
// credential validity for remotes that have SyncCredentials enabled.
type AWSCredentialsModel struct {
	cfg      *config.Config
	db       interface{} // domain.SessionRepository — unused after load; kept for Init
	entries  []awsHostEntry
	cursor   int
	width    int
	height   int
	renewing map[string]bool // entry key values currently being renewed
	message  string          // transient feedback line
}

// --- message types ---

type awsCredLoadedMsg struct{ entries []awsHostEntry }

type awsCredCheckResultMsg struct {
	key    string // "user@host|profile"
	status awsCredStatus
	err    error
}

type awsCredRenewResultMsg struct {
	key string // "user@host|profile"
	err error
}

// --- constructor ---

func NewAWSCredentialsModel(cfg *config.Config, db interface{}) AWSCredentialsModel {
	return AWSCredentialsModel{
		cfg:      cfg,
		db:       db,
		renewing: make(map[string]bool),
	}
}

// --- tea.Model ---

func (m AWSCredentialsModel) Init() tea.Cmd {
	return m.buildEntries()
}

// buildEntries builds one row per (user@host, remoteProfile) found on the
// remote by enumerating ~/.aws/credentials on each unique host. Profiles from
// the config (AWSDelegation.Profile) are included even if not yet pushed.
// The local source_profile is looked up from config by matching remoteProfile.
func (m AWSCredentialsModel) buildEntries() tea.Cmd {
	// Collect unique user@host entries that have SyncCredentials enabled,
	// keeping the best representative remote config for each host.
	type hostInfo struct {
		userAtHost string
		remote     config.Remote
		// map remoteProfile → localProfile derived from config
		configProfiles map[string]string
		// delegation config keyed by remoteProfile (for renewal)
		dels map[string]*config.AWSDelegation
	}
	hosts := map[string]*hostInfo{}

	for _, r := range m.cfg.Remotes {
		userAtHost := r.Host
		if r.User != "" {
			userAtHost = r.User + "@" + r.Host
		}
		hasSyncEnabled := false
		for _, d := range r.AllDelegations() {
			if d.SyncCredentials {
				hasSyncEnabled = true
				break
			}
		}
		if !hasSyncEnabled {
			continue
		}
		if _, ok := hosts[userAtHost]; !ok {
			hosts[userAtHost] = &hostInfo{
				userAtHost:     userAtHost,
				remote:         r,
				configProfiles: map[string]string{},
				dels:           map[string]*config.AWSDelegation{},
			}
		}
		hi := hosts[userAtHost]
		for _, d := range r.AllDelegations() {
			if !d.SyncCredentials {
				continue
			}
			remoteProfile := strings.TrimSpace(d.Profile)
			if remoteProfile == "" {
				remoteProfile = "default"
			}
			hi.configProfiles[remoteProfile] = strings.TrimSpace(d.SourceProfile)
			hi.dels[remoteProfile] = d
		}
	}

	if len(hosts) == 0 {
		return func() tea.Msg { return awsCredLoadedMsg{} }
	}

	return func() tea.Msg {
		var entries []awsHostEntry

		for _, hi := range hosts {
			mgr := ssh.NewManager(ssh.Config{
				Host: hi.remote.Host,
				User: hi.remote.User,
				Root: hi.remote.Root,
			})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			remoteProfiles, sshErr := awsdelegation.ListCredentialProfiles(ctx, mgr)
			cancel()

			// Build a set of all profiles to show:
			// • every profile found on the remote (excluding aiman-managed session profiles)
			// • every profile declared in config (even if not pushed yet)
			seen := map[string]bool{}
			var profiles []string
			for _, p := range remoteProfiles {
				// Skip session-scoped profiles created by aiman (e.g. "aiman-a1b2c3d4").
				// These are managed internally and are not user-configured delegation profiles.
				if strings.HasPrefix(p, "aiman-") {
					continue
				}
				if !seen[p] {
					seen[p] = true
					profiles = append(profiles, p)
				}
			}
			for p := range hi.configProfiles {
				if !seen[p] {
					seen[p] = true
					profiles = append(profiles, p)
				}
			}
			sort.Strings(profiles)

			for _, p := range profiles {
				localProfile := hi.configProfiles[p] // empty string if not in config
				del := hi.dels[p]                    // nil if not in config

				status := awsCredStatusChecking
				if sshErr != nil {
					status = awsCredStatusSSHError
				} else if !seen[p] {
					status = awsCredStatusNotPushed
				}

				entryKey := hi.userAtHost + "|" + localProfile + "|" + p
				entries = append(entries, awsHostEntry{
					key:           entryKey,
					userAtHost:    hi.userAtHost,
					localProfile:  localProfile,
					remoteProfile: p,
					status:        status,
					err:           sshErr,
					del:           del,
					remote:        hi.remote,
				})
			}
		}

		sort.Slice(entries, func(i, j int) bool {
			if entries[i].userAtHost != entries[j].userAtHost {
				return entries[i].userAtHost < entries[j].userAtHost
			}
			return entries[i].remoteProfile < entries[j].remoteProfile
		})

		return awsCredLoadedMsg{entries: entries}
	}
}

// checkCredsCmd fires a credential probe for every entry in Checking state.
func (m AWSCredentialsModel) checkCredsCmd() tea.Cmd {
	var cmds []tea.Cmd
	for _, e := range m.entries {
		if e.status != awsCredStatusChecking {
			continue
		}
		key := e.key
		remote := e.remote
		profile := e.remoteProfile
		cmds = append(cmds, func() tea.Msg {
			mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			err := awsdelegation.CheckCredentials(ctx, mgr, profile)
			if err == nil {
				return awsCredCheckResultMsg{key: key, status: awsCredStatusValid}
			}
			if errors.Is(err, awsdelegation.ErrProfileNotFound) {
				return awsCredCheckResultMsg{key: key, status: awsCredStatusNotPushed, err: err}
			}
			if errors.Is(err, awsdelegation.ErrSSHFailure) {
				return awsCredCheckResultMsg{key: key, status: awsCredStatusSSHError, err: err}
			}
			return awsCredCheckResultMsg{key: key, status: awsCredStatusExpired, err: err}
		})
	}
	return tea.Batch(cmds...)
}

// renewCmd pushes fresh temporary credentials to the remote using the same
// approach as the remotes-config page: AWSDelegation.SourceProfile locally
// → ApplyDelegatedCredentials to AWSDelegation.Profile on remote.
// When d.ManagedRole is true, the IAM role is created automatically if missing
// before credentials are obtained — this is an entirely separate code path that
// can be disabled by setting managed_role: false (the default).
func (m AWSCredentialsModel) renewCmd(e awsHostEntry) tea.Cmd {
	key := e.key
	remote := e.remote
	d := e.del
	profile := e.remoteProfile
	return func() tea.Msg {
		if d == nil {
			return awsCredRenewResultMsg{key: key, err: fmt.Errorf("no AWS delegation config")}
		}

		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		src := strings.TrimSpace(d.SourceProfile)

		sessionPolicy := d.SessionPolicy
		if sessionPolicy == "" && len(d.Regions) > 0 {
			sessionPolicy = awsdelegation.BuildRegionPolicy(d.Regions)
		}

		var roleARN string

		if d.ManagedRole {
			// --- managed role path: create role automatically if missing ---
			accountID := strings.TrimSpace(d.AccountID)
			roleName := strings.TrimSpace(d.RoleName)
			if roleName == "" {
				roleName = awsdelegation.DefaultDelegatedRoleName
			}
			if accountID == "" {
				return awsCredRenewResultMsg{key: key, err: fmt.Errorf("managed_role requires account_id")}
			}
			var err error
			roleARN, err = awsdelegation.EnsureRole(ctx, src, accountID, roleName)
			if err != nil {
				return awsCredRenewResultMsg{key: key, err: fmt.Errorf("ensure managed role: %w", err)}
			}
		} else if sessionPolicy != "" && strings.TrimSpace(d.AccountID) != "" {
			// --- existing path: use role ARN only when a session policy restricts it ---
			var err error
			roleARN, err = awsdelegation.RoleARNFromParts(d.AccountID, d.RoleName)
			if err != nil {
				return awsCredRenewResultMsg{key: key, err: fmt.Errorf("build role ARN: %w", err)}
			}
		}

		opts := awsdelegation.CredentialOptions{
			SessionPolicy:   sessionPolicy,
			DurationSeconds: d.DurationSeconds,
			RoleARN:         roleARN,
			SessionName:     "aiman",
		}
		creds, err := awsdelegation.GetTemporaryCredentials(ctx, src, opts)
		if err != nil {
			return awsCredRenewResultMsg{key: key, err: fmt.Errorf("get temporary credentials: %w", err)}
		}

		if err := awsdelegation.ApplyDelegatedCredentials(ctx, mgr, profile, creds); err != nil {
			return awsCredRenewResultMsg{key: key, err: fmt.Errorf("push credentials: %w", err)}
		}

		// Re-apply the profile block (role_arn + source_profile) in ~/.aws/config.
		configRoleARN := ""
		configSrc := ""
		if !d.SyncCredentials {
			// Only embed role_arn/source_profile when NOT syncing creds
			// (synced creds make those fields redundant and potentially confusing).
			configRoleARN = roleARN
			configSrc = src
		}
		if err := awsdelegation.ApplyDelegatedProfile(ctx, mgr, profile, configRoleARN, configSrc, d.Region); err != nil {
			return awsCredRenewResultMsg{key: key, err: fmt.Errorf("push profile config: %w", err)}
		}

		return awsCredRenewResultMsg{key: key, err: nil}
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
			if e.key == msg.key {
				m.entries[i].status = msg.status
				m.entries[i].err = msg.err
				break
			}
		}
		return m, nil

	case awsCredRenewResultMsg:
		delete(m.renewing, msg.key)
		for i, e := range m.entries {
			if e.key == msg.key {
				if msg.err != nil {
					m.entries[i].status = awsCredStatusExpired
					m.entries[i].err = msg.err
					m.message = fmt.Sprintf("✗ Renew failed for %s [%s]: %v", e.userAtHost, e.remoteProfile, msg.err)
				} else {
					// Re-probe to confirm rather than optimistically setting Valid.
					m.entries[i].status = awsCredStatusChecking
					m.entries[i].err = nil
					m.message = fmt.Sprintf("Renewed %s [%s] — verifying…", e.userAtHost, e.remoteProfile)
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
				if e.del == nil {
					m.message = fmt.Sprintf("Cannot renew [%s]: no local delegation config for this profile.", e.remoteProfile)
				} else if e.status != awsCredStatusNoConf && !m.renewing[e.key] {
					m.renewing[e.key] = true
					m.entries[m.cursor].status = awsCredStatusChecking
					m.message = fmt.Sprintf("Renewing %s [%s]…", e.userAtHost, e.remoteProfile)
					return m, m.renewCmd(e)
				}
			}
		case "R":
			var cmds []tea.Cmd
			count := 0
			for i, e := range m.entries {
				if e.del == nil {
					continue // can't renew without local config
				}
				if (e.status == awsCredStatusExpired || e.status == awsCredStatusNotPushed) && !m.renewing[e.key] {
					m.renewing[e.key] = true
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
			m.message = "Re-scanning remote profiles…"
			return m, m.buildEntries()
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
				if m.renewing[e.key] {
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
