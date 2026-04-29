package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/awsdelegation"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/mutagen"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/usecase"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------------------
// States
// ---------------------------------------------------------------------------

type remotesState int

const (
	remotesStateList       remotesState = iota // main list
	remotesStateAdd                            // modal: add new remote
	remotesStateEdit                           // modal: edit selected remote
	remotesStateDelete                         // modal: confirm delete
	remotesStateTesting                        // testing connection (overlay)
	remotesStateScanning                       // scanning remote (overlay)
	remotesStateResult                         // show scan/test result
	remotesStateAWS                            // configure delegated AWS profile for selected remote
	remotesStateAWSPushing                     // applying ~/.aws/config on remote
)

type dialogFocus int

const (
	dialogFocusHost dialogFocus = iota
	dialogFocusUser
	dialogFocusRoot
	dialogFocusCount // sentinel for modular arithmetic
)

type awsDialogFocus int

const (
	awsFocusProfile awsDialogFocus = iota
	awsFocusRoleName
	awsFocusSource
	awsFocusSync
	awsFocusRegions // comma-separated region restriction list (generates aws:RequestedRegion policy)
	awsFocusRegion
	awsFocusSessionPolicy
	awsFocusDuration
	awsFocusCount
)

// ---------------------------------------------------------------------------
// List item
// ---------------------------------------------------------------------------

type remoteItem struct {
	name     string
	host     string
	user     string
	root     string
	isConfig bool
}

func (i remoteItem) Title() string {
	if !i.isConfig {
		return "  " + i.name + "  (suggestion)"
	}
	return "  " + i.name
}

func (i remoteItem) Description() string {
	return fmt.Sprintf("%s@%s:%s", i.user, i.host, i.root)
}

func (i remoteItem) FilterValue() string {
	return i.name + " " + i.host
}

// ---------------------------------------------------------------------------
// Model
// ---------------------------------------------------------------------------

type RemotesModel struct {
	cfg *config.Config

	state       remotesState
	dialogFocus dialogFocus

	list      list.Model
	hostInput textinput.Model
	userInput textinput.Model
	rootInput textinput.Model

	editingIndex int // index into cfg.Remotes; -1 for add

	testResult  string
	testingHost string
	testingUser string
	testingRoot string
	scanResults *scanResults
	spinner     spinner.Model

	DiscoveredSessions []domain.Session
	done               bool
	width, height      int

	awsRemoteIdx        int
	awsFocus            awsDialogFocus
	awsLocalProfiles    []string
	awsLocalPick        int
	awsProfile          textinput.Model
	awsRoleName         textinput.Model
	awsSource           textinput.Model
	awsRegions          textinput.Model // comma-separated region restriction list
	awsRegion           textinput.Model
	awsSessionPolicy    textinput.Model
	awsDuration         textinput.Model
	awsDerivedAccountID string
	awsAccountLookupErr string
	awsAccountResolving bool
	awsSyncCreds        bool
}

type scanResults struct {
	sessions []domain.Session
	repos    []string
	err      error
}

type awsPushMsg struct {
	err         error
	profile     string
	syncedCreds bool
}

type awsAccountLookupMsg struct {
	accountID string
	err       error
}

func lookupAWSAccountIDCmd(profile string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		id, err := awsdelegation.AccountIDFromLocalProfile(ctx, profile)
		return awsAccountLookupMsg{accountID: id, err: err}
	}
}

// IsAtTopLevel returns true when esc should leave the remotes screen entirely.
func (m RemotesModel) IsAtTopLevel() bool {
	return m.state == remotesStateList
}

// ---------------------------------------------------------------------------
// Constructor
// ---------------------------------------------------------------------------

func NewRemotesModel(cfg *config.Config) RemotesModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	m := RemotesModel{
		cfg:          cfg,
		state:        remotesStateList,
		editingIndex: -1,
		spinner:      s,
	}
	m.list = m.buildList()
	return m
}

func (m *RemotesModel) buildList() list.Model {
	items := []list.Item{}
	currentUser := os.Getenv("USER")

	for _, r := range m.cfg.Remotes {
		items = append(items, remoteItem{
			name:     r.Name,
			host:     r.Host,
			user:     r.User,
			root:     r.Root,
			isConfig: true,
		})
	}

	known := make(map[string]bool)
	for _, r := range m.cfg.Remotes {
		known[r.Host] = true
	}
	for _, h := range ssh.ScanKnownHosts() {
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
	l.SetFilteringEnabled(false) // single-key bindings conflict with filter input
	return l
}

func (m *RemotesModel) refreshList() {
	sel := m.list.Index()
	m.list = m.buildList()
	if m.width > 0 {
		m.list.SetSize(m.width-4, m.height-6)
	}
	if sel < len(m.list.Items()) {
		m.list.Select(sel)
	}
}

// ---------------------------------------------------------------------------
// Dialog helpers
// ---------------------------------------------------------------------------

func (m *RemotesModel) initDialog(host, user, root string) {
	m.hostInput = textinput.New()
	m.hostInput.Placeholder = "hostname or IP"
	m.hostInput.SetValue(host)
	m.hostInput.CharLimit = 120
	m.hostInput.Width = 40

	if user == "" {
		user = os.Getenv("USER")
	}
	m.userInput = textinput.New()
	m.userInput.Placeholder = "username"
	m.userInput.SetValue(user)
	m.userInput.CharLimit = 60
	m.userInput.Width = 40

	if root == "" {
		root = "/home/" + user
	}
	m.rootInput = textinput.New()
	m.rootInput.Placeholder = "/home/user"
	m.rootInput.SetValue(root)
	m.rootInput.CharLimit = 200
	m.rootInput.Width = 40

	m.dialogFocus = dialogFocusHost
}

func (m *RemotesModel) initAWSDialog() {
	var d *config.AWSDelegation
	if m.awsRemoteIdx >= 0 && m.awsRemoteIdx < len(m.cfg.Remotes) {
		d = m.cfg.Remotes[m.awsRemoteIdx].AWSDelegation
	}

	names, _ := awsdelegation.ListLocalAWSProfileNames()
	m.awsLocalProfiles = names
	m.awsLocalPick = -1
	if len(names) > 0 {
		m.awsLocalPick = 0
		for i, n := range names {
			if n == "default" {
				m.awsLocalPick = i
				break
			}
		}
	}

	m.awsProfile = textinput.New()
	m.awsProfile.Placeholder = awsdelegation.DefaultDelegatedProfileName
	m.awsProfile.CharLimit = 80
	m.awsProfile.Width = 56
	if d != nil && strings.TrimSpace(d.Profile) != "" {
		m.awsProfile.SetValue(d.Profile)
	} else {
		m.awsProfile.SetValue(awsdelegation.DefaultDelegatedProfileName)
	}

	m.awsDerivedAccountID = ""
	m.awsAccountLookupErr = ""
	m.awsAccountResolving = false
	m.awsSyncCreds = false
	if d != nil {
		if strings.TrimSpace(d.AccountID) != "" {
			m.awsDerivedAccountID = d.AccountID
		}
		m.awsSyncCreds = d.SyncCredentials
	}

	m.awsRoleName = textinput.New()
	m.awsRoleName.Placeholder = awsdelegation.DefaultDelegatedRoleName + " (default if empty)"
	m.awsRoleName.CharLimit = 64
	m.awsRoleName.Width = 56
	if d != nil {
		m.awsRoleName.SetValue(d.RoleName)
	}

	m.awsSource = textinput.New()
	m.awsSource.Placeholder = "source profile (default is recommended)"
	m.awsSource.CharLimit = 120
	m.awsSource.Width = 56
	if d != nil && strings.TrimSpace(d.SourceProfile) != "" {
		m.awsSource.SetValue(d.SourceProfile)
		for i, n := range m.awsLocalProfiles {
			if n == d.SourceProfile {
				m.awsLocalPick = i
				break
			}
		}
	} else if m.awsLocalPick >= 0 {
		m.awsSource.SetValue(m.awsLocalProfiles[m.awsLocalPick])
	}

	m.awsRegions = textinput.New()
	m.awsRegions.Placeholder = "us-east-2 (comma-sep, empty = no restriction)"
	m.awsRegions.CharLimit = 256
	m.awsRegions.Width = 56
	if d != nil && len(d.Regions) > 0 {
		m.awsRegions.SetValue(strings.Join(d.Regions, ", "))
	} else if d == nil {
		m.awsRegions.SetValue("us-east-2")
	}

	m.awsRegion = textinput.New()
	m.awsRegion.Placeholder = "us-east-1 (optional — sets default region in profile)"
	m.awsRegion.CharLimit = 32
	m.awsRegion.Width = 56
	if d != nil {
		m.awsRegion.SetValue(d.Region)
	}

	m.awsSessionPolicy = textinput.New()
	m.awsSessionPolicy.Placeholder = `{"Version":"2012-10-17","Statement":[...]} (optional)`
	m.awsSessionPolicy.CharLimit = 2048
	m.awsSessionPolicy.Width = 56
	if d != nil {
		m.awsSessionPolicy.SetValue(d.SessionPolicy)
	}

	m.awsDuration = textinput.New()
	m.awsDuration.Placeholder = "900–43200 seconds (optional)"
	m.awsDuration.CharLimit = 6
	m.awsDuration.Width = 20
	if d != nil && d.DurationSeconds > 0 {
		m.awsDuration.SetValue(fmt.Sprintf("%d", d.DurationSeconds))
	}

	m.awsFocus = awsFocusProfile
	m.awsProfile.Focus()
	m.awsRoleName.Blur()
	m.awsSource.Blur()
	m.awsRegion.Blur()
	m.awsSessionPolicy.Blur()
	m.awsDuration.Blur()
}

func (m RemotesModel) applyAWSFocus() (RemotesModel, tea.Cmd) {
	m.awsProfile.Blur()
	m.awsRoleName.Blur()
	m.awsSource.Blur()
	m.awsRegions.Blur()
	m.awsRegion.Blur()
	m.awsSessionPolicy.Blur()
	m.awsDuration.Blur()
	switch m.awsFocus {
	case awsFocusProfile:
		return m, m.awsProfile.Focus()
	case awsFocusRoleName:
		return m, m.awsRoleName.Focus()
	case awsFocusSource:
		m = m.onEnterSourceFocus()
		src := strings.TrimSpace(m.awsSource.Value())
		focusCmd := m.awsSource.Focus()
		m.awsAccountResolving = true
		m.awsAccountLookupErr = ""
		return m, tea.Batch(focusCmd, lookupAWSAccountIDCmd(src))
	case awsFocusRegions:
		return m, m.awsRegions.Focus()
	case awsFocusRegion:
		return m, m.awsRegion.Focus()
	case awsFocusSessionPolicy:
		return m, m.awsSessionPolicy.Focus()
	case awsFocusDuration:
		return m, m.awsDuration.Focus()
	}
	return m, nil
}

// onEnterSourceFocus seeds source_profile from ~/.aws on this Mac when empty (same name is usually used on the remote).
func (m RemotesModel) onEnterSourceFocus() RemotesModel {
	if len(m.awsLocalProfiles) == 0 {
		return m
	}
	if m.awsLocalPick < 0 {
		m.awsLocalPick = 0
	}
	if m.awsLocalPick >= len(m.awsLocalProfiles) {
		m.awsLocalPick = len(m.awsLocalProfiles) - 1
	}
	if strings.TrimSpace(m.awsSource.Value()) == "" {
		m.awsSource.SetValue(m.awsLocalProfiles[m.awsLocalPick])
	}
	return m
}

func (m RemotesModel) cycleLocalProfile(delta int) RemotesModel {
	if len(m.awsLocalProfiles) == 0 {
		return m
	}
	if m.awsLocalPick < 0 {
		m.awsLocalPick = 0
	}
	m.awsLocalPick += delta
	for m.awsLocalPick < 0 {
		m.awsLocalPick += len(m.awsLocalProfiles)
	}
	m.awsLocalPick %= len(m.awsLocalProfiles)
	m.awsSource.SetValue(m.awsLocalProfiles[m.awsLocalPick])
	return m
}

func (m RemotesModel) applyDialogFocus() (RemotesModel, tea.Cmd) {
	m.hostInput.Blur()
	m.userInput.Blur()
	m.rootInput.Blur()
	switch m.dialogFocus {
	case dialogFocusHost:
		return m, m.hostInput.Focus()
	case dialogFocusUser:
		return m, m.userInput.Focus()
	case dialogFocusRoot:
		return m, m.rootInput.Focus()
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Messages
// ---------------------------------------------------------------------------

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
		if err := mgr.ValidateDir(ctx, root); err != nil {
			return testConnMsg{host: host, user: user, root: root, err: err}
		}
		return testConnMsg{host: host, user: user, root: root}
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
		return scanResults{sessions: sessions, repos: repos}
	}
}

func pushAWSDelegation(host, user, root string, d *config.AWSDelegation) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()

		mgr := ssh.NewManager(ssh.Config{Host: host, User: user, Root: root})
		if err := mgr.Connect(ctx); err != nil {
			return awsPushMsg{err: err}
		}
		var p, roleARN, src string
		if d != nil {
			p = strings.TrimSpace(d.Profile)
			src = strings.TrimSpace(d.SourceProfile)
			if p != "" && strings.TrimSpace(d.AccountID) != "" {
				var err error
				roleARN, err = awsdelegation.RoleARNFromParts(d.AccountID, d.RoleName)
				if err != nil {
					return awsPushMsg{err: err, profile: p}
				}
			}
		}

		var syncedCreds bool
		if d != nil && d.SyncCredentials {
			// Build an aws:RequestedRegion inline policy from d.Regions when no
			// custom session policy is provided.
			sessionPolicy := d.SessionPolicy
			if sessionPolicy == "" && len(d.Regions) > 0 {
				sessionPolicy = awsdelegation.BuildRegionPolicy(d.Regions)
			}
			// assume-role is only required when a session policy is set (to further
			// restrict permissions via an inline policy — get-session-token does not
			// support inline policies). duration_seconds alone uses get-session-token
			// directly and does NOT require sts:AssumeRole on any role.
			scopedRoleARN := ""
			if sessionPolicy != "" {
				scopedRoleARN = roleARN
			}
			opts := awsdelegation.CredentialOptions{
				SessionPolicy:   sessionPolicy,
				DurationSeconds: d.DurationSeconds,
				RoleARN:         scopedRoleARN,
				SessionName:     "aiman",
			}
			creds, err := awsdelegation.GetTemporaryCredentials(ctx, src, opts)
			if err != nil {
				return awsPushMsg{err: fmt.Errorf("local credentials: %w", err), profile: p}
			}
			if err := awsdelegation.ApplyDelegatedCredentials(ctx, mgr, p, creds); err != nil {
				return awsPushMsg{err: fmt.Errorf("push credentials: %w", err), profile: p}
			}
			syncedCreds = true

			// If we synced credentials, the remote doesn't need to know about the source_profile or role_arn.
			roleARN = ""
			src = ""
		}

		region := ""
		if d != nil {
			region = d.Region
		}
		if err := awsdelegation.ApplyDelegatedProfile(ctx, mgr, p, roleARN, src, region); err != nil {
			return awsPushMsg{err: err, profile: p}
		}

		return awsPushMsg{profile: p, syncedCreds: syncedCreds}
	}
}

// ---------------------------------------------------------------------------
// Init / Update
// ---------------------------------------------------------------------------

func (m RemotesModel) Init() tea.Cmd { return nil }

func (m RemotesModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.WindowSizeMsg); ok {
		m.width = msg.Width
		m.height = msg.Height
		m.list.SetSize(msg.Width-4, msg.Height-6)
		return m, nil
	}
	if msg, ok := msg.(spinner.TickMsg); ok {
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if m.state == remotesStateTesting || m.state == remotesStateScanning || m.state == remotesStateAWSPushing {
			return m, cmd
		}
		return m, nil
	}

	switch m.state {
	case remotesStateList:
		return m.updateList(msg)
	case remotesStateAdd, remotesStateEdit:
		return m.updateDialog(msg)
	case remotesStateDelete:
		return m.updateDelete(msg)
	case remotesStateTesting:
		return m.updateTesting(msg)
	case remotesStateScanning:
		return m.updateScanning(msg)
	case remotesStateResult:
		return m.updateResult(msg)
	case remotesStateAWS:
		return m.updateAWS(msg)
	case remotesStateAWSPushing:
		return m.updateAWSPushing(msg)
	}
	return m, nil
}

// --- List state ---

func (m RemotesModel) updateList(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "a":
			m.initDialog("", "", "")
			m.editingIndex = -1
			m.state = remotesStateAdd
			return m.applyDialogFocus()

		case "e", "enter":
			if i, ok := m.list.SelectedItem().(remoteItem); ok {
				m.initDialog(i.host, i.user, i.root)
				if i.isConfig {
					for idx, r := range m.cfg.Remotes {
						if r.Host == i.host {
							m.editingIndex = idx
							break
						}
					}
					m.state = remotesStateEdit
				} else {
					// suggestion → treat as add, pre-filled
					m.editingIndex = -1
					m.state = remotesStateAdd
				}
				return m.applyDialogFocus()
			}

		case "d":
			if i, ok := m.list.SelectedItem().(remoteItem); ok && i.isConfig {
				m.state = remotesStateDelete
				return m, nil
			}

		case " ":
			// Scan the selected remote to verify connectivity and discover sessions
			if i, ok := m.list.SelectedItem().(remoteItem); ok && i.isConfig {
				m.testingHost = i.host
				m.testingUser = i.user
				m.testingRoot = i.root
				m.state = remotesStateScanning
				return m, tea.Batch(scanRemote(i.host, i.user, i.root), m.spinner.Tick)
			}

		case "w":
			if i, ok := m.list.SelectedItem().(remoteItem); ok && i.isConfig {
				m.awsRemoteIdx = -1
				for idx, r := range m.cfg.Remotes {
					if r.Host == i.host {
						m.awsRemoteIdx = idx
						break
					}
				}
				if m.awsRemoteIdx < 0 {
					return m, nil
				}
				m.initAWSDialog()
				m.state = remotesStateAWS
				m2, cmd := m.applyAWSFocus()
				src := strings.TrimSpace(m2.awsSource.Value())
				m2.awsAccountResolving = true
				m2.awsAccountLookupErr = ""
				return m2, tea.Batch(cmd, lookupAWSAccountIDCmd(src))
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// --- Add / Edit dialog ---

func (m RemotesModel) updateDialog(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "esc":
			m.state = remotesStateList
			return m, nil
		case "tab":
			m.dialogFocus = (m.dialogFocus + 1) % dialogFocusCount
			return m.applyDialogFocus()
		case "shift+tab":
			m.dialogFocus = (m.dialogFocus - 1 + dialogFocusCount) % dialogFocusCount
			return m.applyDialogFocus()
		case "enter":
			host := strings.TrimSpace(m.hostInput.Value())
			user := strings.TrimSpace(m.userInput.Value())
			root := strings.TrimSpace(m.rootInput.Value())
			if host == "" || user == "" {
				return m, nil
			}
			if root == "" {
				root = "/home/" + user
			}
			m.testingHost = host
			m.testingUser = user
			m.testingRoot = root
			m.state = remotesStateTesting
			return m, tea.Batch(testConnection(host, user, root), m.spinner.Tick)
		}
	}

	// Auto-update root when user changes
	var cmd tea.Cmd
	switch m.dialogFocus {
	case dialogFocusHost:
		m.hostInput, cmd = m.hostInput.Update(msg)
	case dialogFocusUser:
		oldUser := m.userInput.Value()
		m.userInput, cmd = m.userInput.Update(msg)
		if m.userInput.Value() != oldUser && m.rootInput.Value() == "/home/"+oldUser {
			m.rootInput.SetValue("/home/" + m.userInput.Value())
		}
	case dialogFocusRoot:
		m.rootInput, cmd = m.rootInput.Update(msg)
	}
	return m, cmd
}

// --- Delete confirmation ---

func (m RemotesModel) updateDelete(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "y":
			if i, ok := m.list.SelectedItem().(remoteItem); ok {
				newRemotes := []config.Remote{}
				for _, r := range m.cfg.Remotes {
					if r.Host != i.host {
						newRemotes = append(newRemotes, r)
					}
				}
				m.cfg.Remotes = newRemotes
				_ = m.cfg.Save()
				m.refreshList()
			}
			m.state = remotesStateList
		case "n", "esc":
			m.state = remotesStateList
		}
	}
	return m, nil
}

// --- Connection test ---

func (m RemotesModel) updateTesting(msg tea.Msg) (tea.Model, tea.Cmd) {
	res, ok := msg.(testConnMsg)
	if !ok {
		return m, nil
	}

	if res.err != nil {
		m.testResult = failStyle.Render(fmt.Sprintf("Connection failed: %v", res.err))
		m.state = remotesStateResult
		return m, nil
	}

	// Save remote
	if m.editingIndex >= 0 && m.editingIndex < len(m.cfg.Remotes) {
		m.cfg.Remotes[m.editingIndex].Host = res.host
		m.cfg.Remotes[m.editingIndex].User = res.user
		m.cfg.Remotes[m.editingIndex].Root = res.root
		m.cfg.Remotes[m.editingIndex].Name = res.host
	} else {
		m.cfg.Remotes = append(m.cfg.Remotes, config.Remote{
			Name: res.host, Host: res.host, User: res.user, Root: res.root,
		})
	}
	_ = m.cfg.Save()

	m.state = remotesStateScanning
	return m, tea.Batch(scanRemote(res.host, res.user, res.root), m.spinner.Tick)
}

// --- Scanning ---

func (m RemotesModel) updateScanning(msg tea.Msg) (tea.Model, tea.Cmd) {
	if res, ok := msg.(scanResults); ok {
		m.scanResults = &res
		m.DiscoveredSessions = res.sessions
		m.state = remotesStateResult
	}
	return m, nil
}

// --- Result ---

func (m RemotesModel) updateResult(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "enter", "esc":
			if m.scanResults != nil && m.scanResults.err == nil {
				m.done = true
			} else {
				m.refreshList()
				m.state = remotesStateList
			}
		}
	}
	return m, nil
}

func (m RemotesModel) updateAWS(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case awsAccountLookupMsg:
		m.awsAccountResolving = false
		if msg.err != nil {
			m.awsAccountLookupErr = msg.err.Error()
			m.awsDerivedAccountID = ""
		} else {
			m.awsAccountLookupErr = ""
			m.awsDerivedAccountID = msg.accountID
		}
		return m, nil
	}

	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "esc":
			m.state = remotesStateList
			return m, nil
		case "tab":
			m.awsFocus = (m.awsFocus + 1) % awsFocusCount
			return m.applyAWSFocus()
		case "shift+tab":
			m.awsFocus = (m.awsFocus - 1 + awsFocusCount) % awsFocusCount
			return m.applyAWSFocus()
		case "enter":
			return m.saveAWSAndPush()
		case " ":
			if m.awsFocus == awsFocusSync {
				m.awsSyncCreds = !m.awsSyncCreds
				return m, nil
			}
		}
		if m.awsFocus == awsFocusSource {
			switch km.String() {
			case "left", "h":
				m = m.cycleLocalProfile(-1)
				src := strings.TrimSpace(m.awsSource.Value())
				m.awsAccountResolving = true
				m.awsAccountLookupErr = ""
				m.awsDerivedAccountID = ""
				return m, lookupAWSAccountIDCmd(src)
			case "right", "l":
				m = m.cycleLocalProfile(1)
				src := strings.TrimSpace(m.awsSource.Value())
				m.awsAccountResolving = true
				m.awsAccountLookupErr = ""
				m.awsDerivedAccountID = ""
				return m, lookupAWSAccountIDCmd(src)
			}
		}
	}
	var cmd tea.Cmd
	switch m.awsFocus {
	case awsFocusProfile:
		m.awsProfile, cmd = m.awsProfile.Update(msg)
	case awsFocusRoleName:
		m.awsRoleName, cmd = m.awsRoleName.Update(msg)
	case awsFocusSource:
		m.awsSource, cmd = m.awsSource.Update(msg)
	case awsFocusRegions:
		m.awsRegions, cmd = m.awsRegions.Update(msg)
	case awsFocusRegion:
		m.awsRegion, cmd = m.awsRegion.Update(msg)
	case awsFocusSessionPolicy:
		m.awsSessionPolicy, cmd = m.awsSessionPolicy.Update(msg)
	case awsFocusDuration:
		m.awsDuration, cmd = m.awsDuration.Update(msg)
	}
	return m, cmd
}

func (m RemotesModel) saveAWSAndPush() (tea.Model, tea.Cmd) {
	rawProf := strings.TrimSpace(m.awsProfile.Value())
	rn := strings.TrimSpace(m.awsRoleName.Value())
	src := strings.TrimSpace(m.awsSource.Value())

	if m.awsRemoteIdx < 0 || m.awsRemoteIdx >= len(m.cfg.Remotes) {
		return m, nil
	}

	if rawProf == "" && rn == "" && src == "" {
		m.cfg.Remotes[m.awsRemoteIdx].AWSDelegation = nil
		_ = m.cfg.Save()
		m.testResult = successStyle.Render("Cleared saved AWS delegation (remote ~/.aws/config not changed).")
		m.scanResults = nil
		m.state = remotesStateResult
		return m, nil
	}

	prof := rawProf
	if prof == "" {
		prof = awsdelegation.DefaultDelegatedProfileName
	}

	var d *config.AWSDelegation
	if rn == "" && src == "" && rawProf == "" {
		d = &config.AWSDelegation{Profile: prof}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		acct, err := awsdelegation.AccountIDFromLocalProfile(ctx, src)
		cancel()
		if err != nil {
			msg := fmt.Sprintf("Could not derive account ID from local AWS CLI (profile %q): %v", src, err)
			if src == "" {
				msg = fmt.Sprintf("Could not derive account ID from local AWS CLI (using default credentials): %v", err)
			}
			m.testResult = failStyle.Render(msg)
			m.scanResults = nil
			m.state = remotesStateResult
			return m, nil
		}
		if _, err := awsdelegation.RoleARNFromParts(acct, rn); err != nil {
			m.testResult = failStyle.Render(err.Error())
			m.scanResults = nil
			m.state = remotesStateResult
			return m, nil
		}
		d = &config.AWSDelegation{
			Profile: prof, AccountID: acct, RoleName: rn, SourceProfile: src,
			SyncCredentials: m.awsSyncCreds,
			Region:          strings.TrimSpace(m.awsRegion.Value()),
			SessionPolicy:   strings.TrimSpace(m.awsSessionPolicy.Value()),
		}
		// Parse comma-separated region restriction list.
		if raw := strings.TrimSpace(m.awsRegions.Value()); raw != "" {
			parts := strings.Split(raw, ",")
			regions := make([]string, 0, len(parts))
			for _, p := range parts {
				if r := strings.TrimSpace(p); r != "" {
					regions = append(regions, r)
				}
			}
			d.Regions = regions
		}
		if durStr := strings.TrimSpace(m.awsDuration.Value()); durStr != "" {
			dur := 0
			if _, err := fmt.Sscanf(durStr, "%d", &dur); err == nil && dur >= 900 && dur <= 43200 {
				d.DurationSeconds = dur
			} else {
				m.testResult = failStyle.Render("duration_seconds must be an integer between 900 and 43200")
				m.scanResults = nil
				m.state = remotesStateResult
				return m, nil
			}
		}
	}
	m.cfg.Remotes[m.awsRemoteIdx].AWSDelegation = d
	_ = m.cfg.Save()

	r := m.cfg.Remotes[m.awsRemoteIdx]
	m.testingHost = r.Host
	m.testingUser = r.User
	m.testingRoot = r.Root
	m.state = remotesStateAWSPushing
	return m, tea.Batch(pushAWSDelegation(r.Host, r.User, r.Root, d), m.spinner.Tick)
}

func (m RemotesModel) updateAWSPushing(msg tea.Msg) (tea.Model, tea.Cmd) {
	if res, ok := msg.(awsPushMsg); ok {
		if res.err != nil {
			m.testResult = failStyle.Render(fmt.Sprintf("AWS push failed: %v", res.err))
		} else {
			msg := "Updated remote ~/.aws/config"
			if res.profile != "" {
				msg = fmt.Sprintf("Updated remote ~/.aws/config (profile %q)", res.profile)
			}
			if res.syncedCreds {
				msg += " and ~/.aws/credentials"
			}
			m.testResult = successStyle.Render(msg + ".")
		}
		m.scanResults = nil
		m.state = remotesStateResult
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

func (m RemotesModel) View() string {
	switch m.state {
	case remotesStateTesting:
		return m.viewOverlay(fmt.Sprintf("Testing connection to %s@%s...", m.testingUser, m.testingHost))
	case remotesStateScanning:
		return m.viewOverlay(fmt.Sprintf("Scanning %s@%s:%s...", m.testingUser, m.testingHost, m.testingRoot))
	case remotesStateAWSPushing:
		return m.viewOverlay(fmt.Sprintf("Writing ~/.aws/config on %s@%s...", m.testingUser, m.testingHost))
	case remotesStateResult:
		return m.viewResult()
	case remotesStateAdd, remotesStateEdit:
		return m.viewDialog()
	case remotesStateAWS:
		return m.viewAWS()
	case remotesStateDelete:
		return m.viewDeleteConfirm()
	default:
		return m.viewList()
	}
}

func (m RemotesModel) viewList() string {
	var b strings.Builder
	b.WriteString(m.list.View())
	b.WriteString("\n  ")
	b.WriteString(activeStyle.Render("[a]") + " add  ")
	b.WriteString(activeStyle.Render("[e]") + " edit  ")
	b.WriteString(activeStyle.Render("[d]") + " delete  ")
	b.WriteString(activeStyle.Render("[space]") + " scan  ")
	b.WriteString(activeStyle.Render("[w]") + " AWS profile  ")
	b.WriteString(activeStyle.Render("[esc]") + " back")
	return b.String()
}

func (m RemotesModel) viewDialog() string {
	title := "Add Remote"
	if m.state == remotesStateEdit {
		title = "Edit Remote"
	}

	focusLabel := func(label string, f dialogFocus) string {
		if m.dialogFocus == f {
			return activeStyle.Render(label)
		}
		return label
	}

	var b strings.Builder
	b.WriteString(activeStyle.Render(title) + "\n\n")
	b.WriteString(fmt.Sprintf("  %s %s\n\n", focusLabel("Host:", dialogFocusHost), m.hostInput.View()))
	b.WriteString(fmt.Sprintf("  %s %s\n\n", focusLabel("User:", dialogFocusUser), m.userInput.View()))
	b.WriteString(fmt.Sprintf("  %s %s\n\n", focusLabel("Root:", dialogFocusRoot), m.rootInput.View()))
	b.WriteString("  " + activeStyle.Render("[tab]") + " next field  ")
	b.WriteString(activeStyle.Render("[enter]") + " save & test  ")
	b.WriteString(activeStyle.Render("[esc]") + " cancel")

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("205")).
		Padding(1, 2).
		Width(60)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(b.String()))
}

func (m RemotesModel) viewAWS() string {
	label := func(title string, f awsDialogFocus) string {
		if m.awsFocus == f {
			return activeStyle.Render(title)
		}
		return title
	}
	var b strings.Builder
	b.WriteString(activeStyle.Render("Delegated AWS profile (remote ~/.aws/config)") + "\n\n")
	b.WriteString(statusStyle.Render("  source_profile is written to the remote and must match a profile there with long-lived credentials.") + "\n")
	b.WriteString(statusStyle.Render("  Account ID is derived locally via: aws sts get-caller-identity --profile <source_profile>.") + "\n")
	b.WriteString(statusStyle.Render("  ←/→ on source_profile cycles names from this Mac’s ~/.aws.") + "\n\n")

	b.WriteString(fmt.Sprintf("  %s %s\n\n", label("Delegated profile name (remote, default "+awsdelegation.DefaultDelegatedProfileName+"):", awsFocusProfile), m.awsProfile.View()))
	b.WriteString(fmt.Sprintf("  %s %s\n\n", label("IAM role name (optional):", awsFocusRoleName), m.awsRoleName.View()))

	pickHint := ""
	if len(m.awsLocalProfiles) == 0 {
		pickHint = statusStyle.Render("  (no profiles found locally — type source_profile by hand)") + "\n"
	} else {
		pickLine := "—"
		showIdx := 0
		if m.awsLocalPick >= 0 && m.awsLocalPick < len(m.awsLocalProfiles) {
			pickLine = fmt.Sprintf("◀ %s ▶", m.awsLocalProfiles[m.awsLocalPick])
			showIdx = m.awsLocalPick + 1
		}
		pickHint = fmt.Sprintf("  %s  %s  (%d/%d)\n",
			statusStyle.Render("Local name picker:"),
			pickLine,
			showIdx,
			len(m.awsLocalProfiles),
		)
	}
	b.WriteString(pickHint)
	b.WriteString(fmt.Sprintf("  %s %s\n\n", label("source_profile (local CLI profile for account lookup):", awsFocusSource), m.awsSource.View()))

	acctLine := "—"
	if m.awsAccountResolving {
		acctLine = "… (running aws sts get-caller-identity)"
	} else if m.awsDerivedAccountID != "" {
		acctLine = m.awsDerivedAccountID
	}
	b.WriteString("  " + statusStyle.Render("AWS account ID (from local CLI):") + " ")
	b.WriteString(activeStyle.Render(acctLine) + "\n")
	if m.awsAccountLookupErr != "" {
		b.WriteString("  " + failStyle.Render(m.awsAccountLookupErr) + "\n")
	}
	b.WriteString("\n")

	if arn, err := awsdelegation.RoleARNFromParts(m.awsDerivedAccountID, m.awsRoleName.Value()); err == nil {
		b.WriteString("  " + statusStyle.Render("role_arn (generated by aiman): "+arn) + "\n\n")
	}

	syncCheck := "[ ]"
	if m.awsSyncCreds {
		syncCheck = "[x]"
	}
	b.WriteString(fmt.Sprintf("  %s %s\n\n", label(syncCheck+" Sync temporary credentials to remote ~/.aws/credentials", awsFocusSync), statusStyle.Render("(recommended if remote lacks credentials)")))

	b.WriteString(statusStyle.Render("  — Optional restrictions (applied when syncing credentials) —") + "\n\n")
	b.WriteString(fmt.Sprintf("  %s %s\n", label("Restrict to regions (comma-sep, empty = no restriction):", awsFocusRegions), m.awsRegions.View()))
	b.WriteString(statusStyle.Render("    Generates an aws:RequestedRegion condition policy. Requires a role ARN (above) to take effect.") + "\n\n")
	b.WriteString(fmt.Sprintf("  %s %s\n\n", label("Region (written to profile):", awsFocusRegion), m.awsRegion.View()))
	b.WriteString(fmt.Sprintf("  %s %s\n\n", label("Session policy (inline JSON — narrows resource/action access):", awsFocusSessionPolicy), m.awsSessionPolicy.View()))
	b.WriteString(fmt.Sprintf("  %s %s\n\n", label("Duration seconds (900–43200):", awsFocusDuration), m.awsDuration.View()))

	b.WriteString(statusStyle.Render("  Clear profile + source + role to drop saved delegation. Empty source+role removes the remote profile block only.") + "\n\n")
	b.WriteString("  " + activeStyle.Render("[tab]") + " next field  ")
	b.WriteString(activeStyle.Render("[space/enter]") + " toggle sync  ")
	b.WriteString(activeStyle.Render("[enter]") + " save & push  ")
	b.WriteString(activeStyle.Render("[esc]") + " cancel")

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("214")).
		Padding(1, 2).
		Width(76)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(b.String()))
}

func (m RemotesModel) viewDeleteConfirm() string {
	i, _ := m.list.SelectedItem().(remoteItem)

	var b strings.Builder
	b.WriteString(failStyle.Render("Delete Remote?") + "\n\n")
	b.WriteString(fmt.Sprintf("  %s@%s:%s\n\n", i.user, i.host, i.root))
	b.WriteString("  " + activeStyle.Render("[y]") + " confirm  ")
	b.WriteString(activeStyle.Render("[n]") + " cancel")

	dialog := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(lipgloss.Color("196")).
		Padding(1, 2).
		Width(50)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(b.String()))
}

func (m RemotesModel) viewOverlay(msg string) string {
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(1, 2).
		Width(60)
	body := fmt.Sprintf("%s %s", m.spinner.View(), msg)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, style.Render(body))
}

func (m RemotesModel) viewResult() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Result for %s@%s\n\n", m.testingUser, m.testingHost))

	if m.scanResults != nil {
		if m.scanResults.err != nil {
			b.WriteString(failStyle.Render(fmt.Sprintf("  Error: %v\n", m.scanResults.err)))
		} else {
			b.WriteString(successStyle.Render("  Connection & scan successful!") + "\n\n")
			b.WriteString(fmt.Sprintf("  Found %d tmux sessions\n", len(m.scanResults.sessions)))
			b.WriteString(fmt.Sprintf("  Found %d git repositories in %s\n", len(m.scanResults.repos), m.testingRoot))
		}
	} else {
		b.WriteString(m.testResult + "\n")
	}
	b.WriteString("\n  Press " + activeStyle.Render("Enter") + " to continue")

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240")).
		Padding(1, 2).
		Width(60)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(b.String()))
}
