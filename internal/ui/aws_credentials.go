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
	"github.com/charmbracelet/bubbles/textinput"
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
	// expiresAt is when the remote credentials stop working; zero when unknown.
	// expiryApprox marks values derived from the credentials file's mtime plus the
	// configured lifetime, for profiles pushed before aiman recorded expiry.
	expiresAt    time.Time
	expiryApprox bool
	// del is the delegation config for this remote, used for renewal.
	del *config.AWSDelegation
	// remote is the resolved config.Remote for SSH operations.
	remote config.Remote
}

// AWSCredentialsModel lists one row per (user@host, profile) pair and shows/manages AWS
// credential validity for remotes that have SyncCredentials enabled.
type AWSCredentialsModel struct {
	cfg         *config.Config
	db          interface{} // domain.SessionRepository — unused after load; kept for Init
	entries     []awsHostEntry
	cursor      int
	width       int
	height      int
	renewing    map[string]bool // entry key values currently being renewed
	message     string          // transient feedback line
	renaming    bool
	renameKey   string
	renameInput textinput.Model
	// editingLifetime is true while the inline credential-lifetime editor is open.
	// lifetimeKey identifies the row being edited.
	editingLifetime bool
	lifetimeKey     string
	lifetimeInput   textinput.Model
	// externalRefresh is true while a dashboard-triggered (shift+R) refresh-all is in
	// flight. That path renews from config rather than from these rows, so it is
	// reported separately instead of via the per-row renewing map.
	externalRefresh bool
	// refreshFailures counts failures in the current refresh wave, so a wave that
	// finishes while this page is closed can be reported accurately. Reset when a new
	// wave starts.
	refreshFailures int
	localNames      []string
	localCursor     int
	focusLocal      bool
}

// Busy reports whether credential work is still running: a renewal in flight or a status
// probe that has not come back yet. Renewals and probes are bubbletea commands owned by
// the program, so they keep running when the user leaves this page; Busy is how the
// dashboard and this view know to keep showing progress.
func (m AWSCredentialsModel) Busy() bool {
	if len(m.renewing) > 0 || m.externalRefresh {
		return true
	}
	for _, e := range m.entries {
		if e.status == awsCredStatusChecking {
			return true
		}
	}
	return false
}

// Refreshing reports whether credentials are actively being re-minted, as opposed to the
// routine status probes Busy also counts. The dashboard uses this so its banner does not
// announce a "refresh" for an ordinary re-check.
func (m AWSCredentialsModel) Refreshing() bool {
	return len(m.renewing) > 0 || m.externalRefresh
}

// inFlightCount returns how many rows are mid-renewal or mid-check.
func (m AWSCredentialsModel) inFlightCount() (renewing, checking int) {
	for _, e := range m.entries {
		switch {
		case m.renewing[e.key]:
			renewing++
		case e.status == awsCredStatusChecking:
			checking++
		}
	}
	return renewing, checking
}

// --- message types ---

type awsCredLoadedMsg struct{ entries []awsHostEntry }

type awsCredCheckResultMsg struct {
	key    string // "user@host|profile"
	status awsCredStatus
	err    error
}

type awsCredRenewResultMsg struct {
	key       string // "user@host|profile"
	err       error
	expiresAt time.Time // expiry of the freshly pushed credentials; zero on failure
}

// awsCredTickMsg repaints the credentials table so the expiry countdown stays current.
type awsCredTickMsg time.Time

type awsCredRemoveResultMsg struct {
	key           string
	userAtHost    string
	remoteProfile string
	err           error
}

type awsCredRenameResultMsg struct {
	key        string
	userAtHost string
	oldProfile string
	newProfile string
	err        error
}

// awsCredBatchRenewResultMsg carries results for multiple profiles renewed sequentially
// on the same remote host (used by renewHostCmd to avoid concurrent file write races).
type awsCredBatchRenewResultMsg []awsCredRenewResultMsg

// --- constructor ---

func NewAWSCredentialsModel(cfg *config.Config, db interface{}) AWSCredentialsModel {
	input := textinput.New()
	input.Prompt = ""
	input.CharLimit = 128

	lifetime := textinput.New()
	lifetime.Prompt = ""
	lifetime.CharLimit = 5
	lifetime.Width = 12

	m := AWSCredentialsModel{
		cfg:           cfg,
		db:            db,
		renewing:      make(map[string]bool),
		renameInput:   input,
		lifetimeInput: lifetime,
	}
	m.refreshLocalNames()
	return m
}

func dropRemoteDelegation(cfg *config.Config, userAtHost, remoteProfile string) {
	if cfg == nil {
		return
	}
	remoteProfile = normalizeAWSProfileName(remoteProfile)
	for i := range cfg.Remotes {
		r := &cfg.Remotes[i]
		uh := r.Host
		if r.User != "" {
			uh = r.User + "@" + r.Host
		}
		if uh != userAtHost {
			continue
		}
		if delegationProfileName(r.AWSDelegation) == remoteProfile {
			r.AWSDelegation = nil
		}
		kept := r.AWSDelegations[:0]
		for _, d := range r.AWSDelegations {
			if delegationProfileName(d) != remoteProfile {
				kept = append(kept, d)
			}
		}
		r.AWSDelegations = kept
	}
}

func (m *AWSCredentialsModel) toggleLocalIncluded(name string) {
	if m.cfg == nil {
		return
	}
	var current []string
	if m.cfg.AWS.IncludeProfiles == nil {
		current = append([]string{}, m.localNames...)
	} else {
		current = append([]string{}, *m.cfg.AWS.IncludeProfiles...)
	}
	found := -1
	for i, p := range current {
		if strings.EqualFold(strings.TrimSpace(p), name) {
			found = i
			break
		}
	}
	if found >= 0 {
		current = append(current[:found], current[found+1:]...)
	} else {
		current = append(current, name)
	}
	if localProfileListComplete(current, m.localNames) {
		m.cfg.AWS.IncludeProfiles = nil
	} else {
		m.cfg.AWS.IncludeProfiles = &current
	}
	_ = m.cfg.Save()
}

func localProfileListComplete(current, all []string) bool {
	if len(current) != len(all) {
		return false
	}
	have := map[string]bool{}
	for _, p := range current {
		have[strings.ToLower(strings.TrimSpace(p))] = true
	}
	for _, p := range all {
		if !have[strings.ToLower(p)] {
			return false
		}
	}
	return true
}

func delegationProfileName(d *config.AWSDelegation) string {
	if d == nil {
		return ""
	}
	p := strings.TrimSpace(d.Profile)
	if p == "" {
		return "default"
	}
	return normalizeAWSProfileName(p)
}

// --- tea.Model ---

// Init starts a fresh scan. The repaint tick is owned by the dashboard (a single
// long-lived chain), so re-entering this page never stacks up timers.
func (m AWSCredentialsModel) Init() tea.Cmd {
	return m.buildEntries()
}

func (m *AWSCredentialsModel) refreshLocalNames() {
	names, _ := awsdelegation.ListLocalAWSProfileNames()
	m.localNames = names
	if m.localCursor >= len(m.localNames) {
		m.localCursor = 0
	}
}

// awsCredTablePaintInterval is how often the expiry countdown is redrawn. No SSH work is
// involved — the remaining time is recomputed from the stored expiry.
const awsCredTablePaintInterval = 30 * time.Second

func tickAWSCredTable() tea.Cmd {
	return tea.Tick(awsCredTablePaintInterval, func(t time.Time) tea.Msg {
		return awsCredTickMsg(t)
	})
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

			// Expiry comes from the remote credentials file itself, so it costs one extra
			// round trip per host and needs no STS call.
			var expiry awsdelegation.RemoteCredentialExpiry
			if sshErr == nil {
				ctx, cancel = context.WithTimeout(context.Background(), 20*time.Second)
				expiry, _ = awsdelegation.ReadCredentialExpirations(ctx, mgr)
				cancel()
			}

			remoteSet := map[string]bool{}
			for _, p := range remoteProfiles {
				remoteSet[p] = true
			}
			var profiles []string
			for p := range hi.configProfiles {
				src := hi.configProfiles[p]
				if src != "" && !m.cfg.AWSLocalProfileAllowed(src) {
					continue
				}
				profiles = append(profiles, p)
			}
			sort.Strings(profiles)

			for _, p := range profiles {
				localProfile := hi.configProfiles[p]
				del := hi.dels[p]

				status := awsCredStatusChecking
				if sshErr != nil {
					status = awsCredStatusSSHError
				} else if !remoteSet[p] {
					status = awsCredStatusNotPushed
				}

				durationSeconds := 0
				if del != nil {
					durationSeconds = del.DurationSeconds
				}
				expiresAt, approx, _ := expiry.For(p, durationSeconds)

				entryKey := hi.userAtHost + "|" + localProfile + "|" + p
				entries = append(entries, awsHostEntry{
					key:           entryKey,
					userAtHost:    hi.userAtHost,
					localProfile:  localProfile,
					remoteProfile: p,
					status:        status,
					err:           sshErr,
					expiresAt:     expiresAt,
					expiryApprox:  approx,
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

// pushFreshCredentials mints temporary credentials locally from d.SourceProfile and pushes
// them to remoteProfile on the remote, then re-applies the profile block in ~/.aws/config.
// It returns the credentials' expiry as reported by STS.
//
// When d.ManagedRole is true, the IAM role is created automatically if missing before
// credentials are obtained — an entirely separate code path that can be disabled by
// setting managed_role: false (the default).
//
// This is the single mint-and-push path shared by renewing one entry, renewing every
// entry on a host, and the dashboard's refresh-all.
func pushFreshCredentials(ctx context.Context, mgr awsdelegation.RemoteRunner, d *config.AWSDelegation, remoteProfile string) (time.Time, error) {
	if d == nil {
		return time.Time{}, fmt.Errorf("no AWS delegation config")
	}
	src := strings.TrimSpace(d.SourceProfile)

	sessionPolicy := d.SessionPolicy
	if sessionPolicy == "" && len(d.Regions) > 0 {
		sessionPolicy = awsdelegation.BuildRegionPolicy(d.Regions)
	}

	var roleARN string
	if d.ManagedRole {
		accountID := strings.TrimSpace(d.AccountID)
		roleName := strings.TrimSpace(d.RoleName)
		if roleName == "" {
			roleName = awsdelegation.DefaultDelegatedRoleName
		}
		if accountID == "" {
			return time.Time{}, fmt.Errorf("managed_role requires account_id")
		}
		var err error
		roleARN, err = awsdelegation.EnsureRole(ctx, src, accountID, roleName)
		if err != nil {
			return time.Time{}, fmt.Errorf("ensure managed role: %w", err)
		}
	} else if sessionPolicy != "" && strings.TrimSpace(d.AccountID) != "" {
		// Use a role ARN only when a session policy restricts the credentials.
		var err error
		roleARN, err = awsdelegation.RoleARNFromParts(d.AccountID, d.RoleName)
		if err != nil {
			return time.Time{}, fmt.Errorf("build role ARN: %w", err)
		}
	}

	creds, err := awsdelegation.GetTemporaryCredentials(ctx, src, awsdelegation.CredentialOptions{
		SessionPolicy:   sessionPolicy,
		DurationSeconds: d.DurationSeconds,
		RoleARN:         roleARN,
		SessionName:     "aiman",
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("get temporary credentials: %w", err)
	}

	if err := awsdelegation.ApplyDelegatedCredentials(ctx, mgr, remoteProfile, creds); err != nil {
		return time.Time{}, fmt.Errorf("push credentials: %w", err)
	}

	// Only embed role_arn/source_profile when NOT syncing creds (synced creds make those
	// fields redundant and potentially confusing).
	configRoleARN := ""
	configSrc := ""
	if !d.SyncCredentials {
		configRoleARN = roleARN
		configSrc = src
	}
	if err := awsdelegation.ApplyDelegatedProfile(ctx, mgr, remoteProfile, configRoleARN, configSrc, d.Region); err != nil {
		return time.Time{}, fmt.Errorf("push profile config: %w", err)
	}

	return creds.Expiration, nil
}

// renewCmd pushes fresh temporary credentials for one entry.
func (m AWSCredentialsModel) renewCmd(e awsHostEntry) tea.Cmd {
	key := e.key
	remote := e.remote
	d := e.del
	profile := e.remoteProfile
	return func() tea.Msg {
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		expiresAt, err := pushFreshCredentials(ctx, mgr, d, profile)
		return awsCredRenewResultMsg{key: key, err: err, expiresAt: expiresAt}
	}
}

func (m AWSCredentialsModel) removeCmd(e awsHostEntry) tea.Cmd {
	key := e.key
	remote := e.remote
	profile := e.remoteProfile
	userAtHost := e.userAtHost
	return func() tea.Msg {
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := awsdelegation.RemoveSessionProfile(ctx, mgr, profile)
		return awsCredRemoveResultMsg{key: key, userAtHost: userAtHost, remoteProfile: profile, err: err}
	}
}

func (m AWSCredentialsModel) renameCmd(e awsHostEntry, newProfile string) tea.Cmd {
	key := e.key
	remote := e.remote
	oldProfile := e.remoteProfile
	userAtHost := e.userAtHost
	return func() tea.Msg {
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := awsdelegation.RenameSessionProfile(ctx, mgr, oldProfile, newProfile)
		return awsCredRenameResultMsg{
			key:        key,
			userAtHost: userAtHost,
			oldProfile: oldProfile,
			newProfile: normalizeAWSProfileName(newProfile),
			err:        err,
		}
	}
}

// renewHostCmd renews multiple profiles on the same remote host sequentially within a
// single goroutine. This prevents the read-modify-write race that occurs when concurrent
// goroutines all read ~/.aws/credentials, merge their own profile, and write back —
// causing all but the last writer to be silently overwritten.
func (m AWSCredentialsModel) renewHostCmd(entries []awsHostEntry) tea.Cmd {
	if len(entries) == 0 {
		return nil
	}
	remote := entries[0].remote
	entriesCopy := append([]awsHostEntry(nil), entries...)
	return func() tea.Msg {
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		timeout := time.Duration(len(entriesCopy)+1) * 90 * time.Second
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		results := make(awsCredBatchRenewResultMsg, 0, len(entriesCopy))
		for _, e := range entriesCopy {
			expiresAt, err := pushFreshCredentials(ctx, mgr, e.del, e.remoteProfile)
			results = append(results, awsCredRenewResultMsg{key: e.key, err: err, expiresAt: expiresAt})
		}
		return results
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
		if msg.err != nil {
			m.refreshFailures++
		}
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
					m.entries[i].expiresAt = msg.expiresAt
					m.entries[i].expiryApprox = false
					m.message = fmt.Sprintf("Renewed %s [%s] — verifying…", e.userAtHost, e.remoteProfile)
				}
				break
			}
		}
		return m, m.checkCredsCmd()

	case awsCredBatchRenewResultMsg:
		var failMsgs []string
		for _, r := range msg {
			delete(m.renewing, r.key)
			if r.err != nil {
				m.refreshFailures++
			}
			for i, e := range m.entries {
				if e.key == r.key {
					if r.err != nil {
						m.entries[i].status = awsCredStatusExpired
						m.entries[i].err = r.err
						failMsgs = append(failMsgs, fmt.Sprintf("%s [%s]: %v", e.userAtHost, e.remoteProfile, r.err))
					} else {
						m.entries[i].status = awsCredStatusChecking
						m.entries[i].err = nil
						m.entries[i].expiresAt = r.expiresAt
						m.entries[i].expiryApprox = false
					}
					break
				}
			}
		}
		if len(failMsgs) > 0 {
			m.message = "✗ " + strings.Join(failMsgs, "; ")
		} else {
			m.message = "Renewed — verifying…"
		}
		return m, m.checkCredsCmd()

	case awsCredRemoveResultMsg:
		delete(m.renewing, msg.key)
		if msg.err != nil {
			m.message = fmt.Sprintf("✗ Remove failed for %s [%s]: %v", msg.userAtHost, msg.remoteProfile, msg.err)
			return m, nil
		}
		dropRemoteDelegation(m.cfg, msg.userAtHost, msg.remoteProfile)
		if err := m.cfg.Save(); err != nil {
			m.message = fmt.Sprintf("Removed %s [%s] from remote AWS config, but config save failed: %v", msg.userAtHost, msg.remoteProfile, err)
			return m, m.buildEntries()
		}
		m.message = fmt.Sprintf("Removed %s [%s] from remote AWS config and aiman settings.", msg.userAtHost, msg.remoteProfile)
		return m, m.buildEntries()

	case awsCredRenameResultMsg:
		delete(m.renewing, msg.key)
		m.renaming = false
		m.renameKey = ""
		m.renameInput.Blur()
		if msg.err != nil {
			m.message = fmt.Sprintf("✗ Rename failed for %s [%s → %s]: %v", msg.userAtHost, msg.oldProfile, msg.newProfile, msg.err)
			return m, nil
		}
		if err := renameManagedDelegationProfile(m.cfg, m.entryByKey(msg.key), msg.oldProfile, msg.newProfile); err != nil {
			m.message = fmt.Sprintf("✗ Renamed remote profile, but local settings update failed: %v", err)
			return m, m.buildEntries()
		}
		m.message = fmt.Sprintf("Renamed %s [%s → %s].", msg.userAtHost, msg.oldProfile, msg.newProfile)
		return m, m.buildEntries()

	case tea.KeyMsg:
		m.message = ""
		if m.renaming {
			return m.updateRenameEditor(msg)
		}
		if m.editingLifetime {
			return m.updateLifetimeEditor(msg)
		}
		return m.handleCredentialTableKey(msg)
	}
	return m, nil
}

// updateRenameEditor handles keys while the profile-rename editor is open.
func (m AWSCredentialsModel) updateRenameEditor(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.renaming = false
		m.renameKey = ""
		m.renameInput.Blur()
		m.message = "Rename cancelled."
		return m, nil
	case "enter":
		entry := m.entryByKey(m.renameKey)
		if entry == nil {
			m.renaming = false
			m.renameKey = ""
			m.renameInput.Blur()
			m.message = "Rename target disappeared."
			return m, nil
		}
		newProfile := normalizeAWSProfileName(m.renameInput.Value())
		if newProfile == entry.remoteProfile {
			m.renaming = false
			m.renameKey = ""
			m.renameInput.Blur()
			m.message = "Profile name unchanged."
			return m, nil
		}
		if err := validateRenameTarget(m.entries, *entry, newProfile); err != nil {
			m.message = err.Error()
			return m, nil
		}
		m.renaming = false
		m.renameKey = ""
		m.renameInput.Blur()
		m.renewing[entry.key] = true
		m.message = fmt.Sprintf("Renaming %s [%s → %s]…", entry.userAtHost, entry.remoteProfile, newProfile)
		return m, m.renameCmd(*entry, newProfile)
	}
	var cmd tea.Cmd
	m.renameInput, cmd = m.renameInput.Update(msg)
	return m, cmd
}

// handleCredentialTableKey handles keys for the table itself: moving the cursor, renewing,
// re-checking, renaming, removing, and editing a profile's lifetime.
func (m AWSCredentialsModel) handleCredentialTableKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "tab":
		if len(m.localNames) > 0 {
			m.focusLocal = !m.focusLocal
		}
		return m, nil
	case " ":
		if m.focusLocal && m.localCursor >= 0 && m.localCursor < len(m.localNames) {
			m.toggleLocalIncluded(m.localNames[m.localCursor])
			return m, m.buildEntries()
		}
	case "up", "k":
		if m.focusLocal {
			if m.localCursor > 0 {
				m.localCursor--
			}
			return m, nil
		}
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.focusLocal {
			if m.localCursor < len(m.localNames)-1 {
				m.localCursor++
			}
			return m, nil
		}
		if m.cursor < len(m.entries)-1 {
			m.cursor++
		}
	case "r":
		if m.cursor < len(m.entries) {
			e := m.entries[m.cursor]
			if e.del == nil {
				m.message = fmt.Sprintf("Cannot renew [%s]: no local delegation config for this profile.", e.remoteProfile)
			} else if e.status != awsCredStatusNoConf && !m.renewing[e.key] {
				if len(m.renewing) == 0 {
					m.refreshFailures = 0
				}
				m.renewing[e.key] = true
				m.entries[m.cursor].status = awsCredStatusChecking
				m.message = fmt.Sprintf("Renewing %s [%s]…", e.userAtHost, e.remoteProfile)
				return m, m.renewCmd(e)
			}
		}
	case "R":
		// Refresh everything renewable, whatever its current status — valid
		// credentials included, so the user is never blocked from re-minting.
		// Group entries by host so that profiles on the same remote are renewed
		// sequentially: concurrent reads+writes to ~/.aws/credentials would race,
		// leaving only the last writer's profile in the file.
		targets := entriesToRenewAll(m.entries, m.renewing)
		if len(targets) == 0 {
			m.message = "Nothing to refresh — no profiles with a local delegation config."
			break
		}
		if len(m.renewing) == 0 {
			m.refreshFailures = 0
		}
		hostOrder := []string{}
		groups := map[string][]awsHostEntry{}
		for _, e := range targets {
			m.renewing[e.key] = true
			for i := range m.entries {
				if m.entries[i].key == e.key {
					m.entries[i].status = awsCredStatusChecking
					break
				}
			}
			if _, ok := groups[e.userAtHost]; !ok {
				hostOrder = append(hostOrder, e.userAtHost)
			}
			groups[e.userAtHost] = append(groups[e.userAtHost], e)
		}
		var cmds []tea.Cmd
		for _, host := range hostOrder {
			g := groups[host]
			if len(g) == 1 {
				cmds = append(cmds, m.renewCmd(g[0]))
			} else {
				cmds = append(cmds, m.renewHostCmd(g))
			}
		}
		m.message = fmt.Sprintf("Refreshing %d credential(s)…", len(targets))
		return m, tea.Batch(cmds...)
	case "c":
		m.message = "Re-scanning remote profiles…"
		return m, m.buildEntries()
	case "d":
		if m.cursor < len(m.entries) {
			e := m.entries[m.cursor]
			if !m.renewing[e.key] {
				m.renewing[e.key] = true
				m.message = fmt.Sprintf("Removing %s [%s] from remote AWS config…", e.userAtHost, e.remoteProfile)
				return m, m.removeCmd(e)
			}
		}
	case "t":
		if m.cursor < len(m.entries) {
			return m.openLifetimeEditor(m.entries[m.cursor])
		}
	case "e":
		if m.cursor < len(m.entries) {
			e := m.entries[m.cursor]
			if !m.renewing[e.key] {
				m.renaming = true
				m.renameKey = e.key
				m.renameInput.SetValue(e.remoteProfile)
				m.renameInput.CursorEnd()
				m.renameInput.Focus()
				m.message = fmt.Sprintf("Rename %s [%s] and press Enter.", e.userAtHost, e.remoteProfile)
				return m, textinput.Blink
			}
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
	headerStyle := dimStyle

	b.WriteString("\n  " + titleStyle.Render("AWS Credential Status") + "\n\n")

	// Renewals and probes keep running when this page is closed, so say what is still
	// outstanding — both for work started here and for a dashboard-wide refresh.
	if m.Busy() {
		renewing, checking := m.inFlightCount()
		var parts []string
		if renewing > 0 {
			parts = append(parts, fmt.Sprintf("%d refresh(es)", renewing))
		}
		if checking > 0 {
			parts = append(parts, fmt.Sprintf("%d check(s)", checking))
		}
		if m.externalRefresh {
			parts = append(parts, "a shift+R refresh of all profiles")
		}
		if len(parts) > 0 {
			b.WriteString("  " + warnStyle.Render("⟳ "+strings.Join(parts, " and ")+" in flight — this continues in the background if you leave this page.") + "\n\n")
		}
	}

	if len(m.entries) == 0 {
		b.WriteString(dimStyle.Render("  No remotes with AWS delegation found.\n"))
		b.WriteString(dimStyle.Render("  (Remotes need aws_delegation.sync_credentials: true in config)\n"))
	} else {
		hdr := fmt.Sprintf("  %-12s  %-30s  %-20s  %-20s  %-8s  %-10s",
			"Status", "Host", "Local profile", "Remote profile", "Lifetime", "Expires in")
		b.WriteString(headerStyle.Render(hdr) + "\n")
		b.WriteString(headerStyle.Render("  "+strings.Repeat("─", 112)) + "\n")

		now := time.Now()

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

			expiresStr := fmt.Sprintf("%-10s", formatExpiresIn(e.expiresAt, e.expiryApprox, now))
			switch urgencyOf(e.expiresAt, now) {
			case expiryExpired:
				expiresStr = expiredStyle.Render(expiresStr)
			case expiryWarn:
				expiresStr = warnStyle.Render(expiresStr)
			case expiryOK:
				expiresStr = validStyle.Render(expiresStr)
			default:
				expiresStr = dimStyle.Render(expiresStr)
			}

			// Profiles with no local delegation config have no lifetime to show — aiman
			// cannot mint for them, so there is nothing configurable.
			lifetimeStr := dimStyle.Render(fmt.Sprintf("%-8s", "—"))
			if e.del != nil {
				lifetimeStr = fmt.Sprintf("%-8s", formatLifetime(e.del.DurationSeconds))
			}

			line := fmt.Sprintf("  %s  %-30s  %-20s  %-20s  %s  %s",
				statusStr,
				truncateRunes(e.userAtHost, 30),
				truncateRunes(localP, 20),
				truncateRunes(remoteP, 20),
				lifetimeStr,
				expiresStr,
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
	if m.renaming {
		b.WriteString("  New remote profile: " + m.renameInput.View() + "\n")
		b.WriteString("  Press Enter to rename or Esc to cancel.\n\n")
	}
	if m.editingLifetime {
		b.WriteString("  Credential lifetime (seconds): " + m.lifetimeInput.View() + "\n")
		b.WriteString(dimStyle.Render(fmt.Sprintf("  Enter to save · Esc to cancel   (%d–%d, empty = default %s)",
			minCredentialLifetime, maxCredentialLifetime, formatLifetime(0))) + "\n\n")
	}

	helpStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	b.WriteString("\n  " + titleStyle.Render("Local AWS profiles") + "\n")
	b.WriteString(dimStyle.Render("  Only checked names are used for sync and shown above. Space toggles; Tab switches list.") + "\n")
	if len(m.localNames) == 0 {
		b.WriteString(dimStyle.Render("  No profiles in ~/.aws on this machine.\n"))
	} else {
		for i, name := range m.localNames {
			mark := "[ ]"
			if m.cfg == nil || m.cfg.AWSLocalProfileAllowed(name) {
				mark = "[x]"
			}
			line := fmt.Sprintf("  %s %s", mark, name)
			if m.focusLocal && i == m.localCursor {
				line = selectedBg.Render(line)
			}
			b.WriteString(line + "\n")
		}
	}

	b.WriteString(helpStyle.Render("  r renew selected  •  shift+R refresh ALL  •  e rename selected profile  •  t lifetime of selected profile  •  d delete (remote + settings)  •  space toggle local  •  tab lists  •  c re-check all  •  ESC back") + "\n")
	b.WriteString(helpStyle.Render("  \"~\" marks an expiry estimated from the credentials file's age (pushed before aiman recorded expiry) — refresh to replace it with the exact time.") + "\n")

	return b.String()
}

// entriesToRenewAll returns every entry shift+R should refresh: anything with a local
// delegation config that is not already being renewed, regardless of current status.
// Entries without a delegation config are skipped because there is nothing to mint from.
func entriesToRenewAll(entries []awsHostEntry, renewing map[string]bool) []awsHostEntry {
	var out []awsHostEntry
	for _, e := range entries {
		if e.del == nil || renewing[e.key] {
			continue
		}
		out = append(out, e)
	}
	return out
}

func (m AWSCredentialsModel) entryByKey(key string) *awsHostEntry {
	for i := range m.entries {
		if m.entries[i].key == key {
			return &m.entries[i]
		}
	}
	return nil
}

func validateRenameTarget(entries []awsHostEntry, selected awsHostEntry, newProfile string) error {
	if newProfile == "" {
		return fmt.Errorf("profile name is required")
	}
	for _, e := range entries {
		if e.key == selected.key {
			continue
		}
		if e.userAtHost == selected.userAtHost && e.remoteProfile == newProfile {
			return fmt.Errorf("cannot rename to [%s]: that profile already exists on %s", newProfile, selected.userAtHost)
		}
	}
	return nil
}

func renameManagedDelegationProfile(cfg *config.Config, entry *awsHostEntry, oldProfile, newProfile string) error {
	if cfg == nil || entry == nil || entry.del == nil {
		return nil
	}
	targetRemote := entry.remote
	targetOld := normalizeAWSProfileName(oldProfile)
	targetNew := normalizeAWSProfileName(newProfile)

	for i := range cfg.Remotes {
		r := &cfg.Remotes[i]
		if strings.TrimSpace(r.Host) != strings.TrimSpace(targetRemote.Host) ||
			strings.TrimSpace(r.User) != strings.TrimSpace(targetRemote.User) ||
			strings.TrimSpace(r.Root) != strings.TrimSpace(targetRemote.Root) {
			continue
		}
		if hasDelegationProfile(r, targetNew) && targetOld != targetNew {
			return fmt.Errorf("local settings already use profile %q on %s", targetNew, entry.userAtHost)
		}

		updated := false
		if r.AWSDelegation != nil && normalizeAWSProfileName(r.AWSDelegation.Profile) == targetOld {
			r.AWSDelegation.Profile = targetNew
			updated = true
		}
		for _, d := range r.AWSDelegations {
			if d != nil && normalizeAWSProfileName(d.Profile) == targetOld {
				d.Profile = targetNew
				updated = true
			}
		}
		if !updated {
			return nil
		}
		if err := cfg.Save(); err != nil {
			if r.AWSDelegation != nil && normalizeAWSProfileName(r.AWSDelegation.Profile) == targetNew {
				r.AWSDelegation.Profile = targetOld
			}
			for _, d := range r.AWSDelegations {
				if d != nil && normalizeAWSProfileName(d.Profile) == targetNew {
					d.Profile = targetOld
				}
			}
			return err
		}
		return nil
	}
	return nil
}

func hasDelegationProfile(r *config.Remote, profile string) bool {
	if r == nil {
		return false
	}
	profile = normalizeAWSProfileName(profile)
	for _, d := range r.AllDelegations() {
		if d != nil && normalizeAWSProfileName(d.Profile) == profile {
			return true
		}
	}
	return false
}

func normalizeAWSProfileName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "default"
	}
	return name
}
