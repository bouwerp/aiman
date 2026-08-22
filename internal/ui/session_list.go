package ui

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/remotesvc"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

func groupRollup(items []item) string {
	hasWait, hasErr, hasBusy := false, false, false
	for _, it := range items {
		if it.needsInput || it.activity == "stale" {
			hasWait = true
		}
		if it.activity == "create-failed" {
			hasErr = true
		}
		if it.activity == "busy" || it.activity == "creating" {
			hasBusy = true
		}
	}
	switch {
	case hasWait:
		return "waiting"
	case hasErr:
		return "error"
	case hasBusy:
		return "working"
	default:
		return "idle"
	}
}

func groupedSessionItems(flat []item, collapsed map[string]bool) []list.Item {
	if len(flat) == 0 {
		return nil
	}
	if collapsed == nil {
		collapsed = map[string]bool{}
	}
	type bucket struct {
		key   string
		items []item
	}
	order := []string{}
	by := map[string]*bucket{}
	for _, it := range flat {
		g := domain.GroupLabel(it.session.Group)
		if it.remoteName != "" {
			g = g + "\x00" + it.remoteName
		}
		b, ok := by[g]
		if !ok {
			b = &bucket{key: g}
			by[g] = b
			order = append(order, g)
		}
		b.items = append(b.items, it)
	}
	sort.Strings(order)
	var out []list.Item
	for _, key := range order {
		b := by[key]
		sort.Slice(b.items, func(i, j int) bool {
			return strings.ToLower(b.items[i].session.Name) < strings.ToLower(b.items[j].session.Name)
		})
		groupName := domain.GroupLabel(b.items[0].session.Group)
		header := item{
			header:     true,
			session:    domain.Session{Group: groupName, RemoteHost: b.items[0].session.RemoteHost},
			remoteName: b.items[0].remoteName,
			activity:   groupRollup(b.items),
			groupN:     len(b.items),
			collapsed:  collapsed[key],
		}
		out = append(out, header)
		if header.collapsed {
			continue
		}
		for i, it := range b.items {
			it.treeLast = i == len(b.items)-1
			out = append(out, it)
		}
	}
	return out
}

func (m *Model) defaultRemote() (config.Remote, bool) {
	if m.cfg == nil {
		return config.Remote{}, false
	}
	if m.remoteFilter != "" {
		for _, r := range m.cfg.Remotes {
			if r.Host == m.remoteFilter {
				return r, true
			}
		}
	}
	return resolveRemote(m.cfg, domain.Session{RemoteHost: m.cfg.ActiveRemote})
}

func (m *Model) selectedSessionItem() (item, bool) {
	sel := m.list.SelectedItem()
	it, ok := sel.(item)
	if !ok || it.header {
		return item{}, false
	}
	return it, true
}

func (m *Model) selectFirstSessionRow() {
	for i, it := range m.list.Items() {
		if si, ok := it.(item); ok && !si.header {
			m.list.Select(i)
			return
		}
	}
}

// snapOffGroupHeader moves the cursor off a group header onto a session.
// dir > 0 prefers the first session under the header; dir < 0 prefers the
// session above it. Headers are labels, not selectable sessions.
func (m *Model) snapOffGroupHeader(dir int) {
	it, ok := m.list.SelectedItem().(item)
	if !ok || !it.header {
		return
	}
	items := m.list.Items()
	idx := m.list.Index()
	pick := func(start, step int) bool {
		for i := start; i >= 0 && i < len(items); i += step {
			if si, ok := items[i].(item); ok && !si.header {
				m.list.Select(i)
				return true
			}
		}
		return false
	}
	if dir < 0 {
		if pick(idx-1, -1) {
			return
		}
		_ = pick(idx+1, 1)
		return
	}
	if pick(idx+1, 1) {
		return
	}
	_ = pick(idx-1, -1)
}

type sessionListDelegate struct {
	list.DefaultDelegate
}

func newSessionListDelegate() sessionListDelegate {
	return sessionListDelegate{DefaultDelegate: list.NewDefaultDelegate()}
}

var (
	stateColorWorking = lipgloss.Color("#00FF00")
	stateColorWaiting = lipgloss.Color("#FFA500")
	stateColorError   = lipgloss.Color("#FF0000")
	stateColorIdle    = lipgloss.Color("#7D7D7D")
	stateColorEnded   = lipgloss.Color("#6B6B6B")
	stateColorName    = lipgloss.Color("#DDDDDD")
	headerLabelColor  = lipgloss.Color("62")
	headerSelFg       = lipgloss.Color("229")
	headerSelBg       = lipgloss.Color("57")
)

func sessionStateColor(i item) lipgloss.Color {
	if i.header {
		switch i.activity {
		case "waiting":
			return stateColorWaiting
		case "error":
			return stateColorError
		case "working":
			return stateColorWorking
		default:
			return stateColorIdle
		}
	}
	if i.session.AgentEnded || i.activity == "terminating" {
		return stateColorEnded
	}
	if i.activity == "create-failed" || i.activity == "stale" {
		return stateColorError
	}
	if i.needsInput {
		return stateColorWaiting
	}
	if i.syncStale {
		return stateColorError
	}
	switch i.activity {
	case "busy", "creating":
		return stateColorWorking
	case "idle":
		return stateColorIdle
	default:
		return stateColorName
	}
}

func (i item) chrome() (prefix, activity string) {
	switch i.activity {
	case "creating":
		prefix, activity = "~ ", " • creating…"
	case "create-failed":
		prefix, activity = "! ", " ⚠ create failed"
	case "terminating":
		prefix, activity = "x ", " • terminating…"
	default:
		if i.needsInput {
			prefix = "! "
			activity = " ⚠ input"
			if msg := strings.TrimSpace(i.session.HookStateMessage); msg != "" {
				activity = " ⚠ " + truncateRunes(msg, 24)
			}
			break
		}
		switch i.activity {
		case "idle":
			prefix, activity = "o ", " • idle"
		case "busy":
			prefix, activity = "> ", " • busy"
		case "stale":
			prefix, activity = "! ", " ⚠ thinking (stuck?)"
		}
	}
	if i.syncStale {
		activity += " ⚠ sync"
	}
	if i.session.AgentEnded {
		prefix = "x "
		activity = " • exited"
	}
	if i.session.Mode == domain.SessionModeAutonomous {
		prefix = "🤖 " + prefix
	}
	return prefix, activity
}

func (i item) displayName() string {
	label := i.session.Name
	if label == "" {
		if i.session.IssueKey != "" {
			label = fmt.Sprintf("%s (%s)", i.session.IssueKey, i.session.TmuxSession)
		} else {
			label = i.session.TmuxSession
		}
	}
	if title := strings.TrimSpace(i.session.AgentTitle); title != "" {
		label += " · " + truncateRunes(title, 32)
	}
	return label
}

func (i item) treeBranch() string {
	if i.treeLast {
		return "  └─ "
	}
	return "  ├─ "
}

func (i item) headerPlainTitle() string {
	label := domain.GroupLabel(i.session.Group)
	count := ""
	if i.groupN > 0 {
		count = fmt.Sprintf(" · %d", i.groupN)
	}
	activity := ""
	if i.activity != "" {
		activity = " • " + i.activity
	}
	remoteTag := ""
	if i.remoteName != "" {
		remoteTag = " [" + i.remoteName + "]"
	}
	glyph := "▾"
	if i.collapsed {
		glyph = "▸"
	}
	return fmt.Sprintf("%s %s%s%s%s", glyph, label, count, activity, remoteTag)
}

func (i item) plainTitle() string {
	if i.header {
		return i.headerPlainTitle()
	}
	prefix, activity := i.chrome()
	return i.treeBranch() + prefix + i.displayName() + activity
}

func (d sessionListDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	it, ok := listItem.(item)
	if !ok {
		d.DefaultDelegate.Render(w, m, index, listItem)
		return
	}
	if m.Width() <= 0 {
		return
	}
	selected := index == m.Index()
	if it.header {
		_, _ = fmt.Fprint(w, renderSessionHeader(it, selected)+"\n")
		return
	}
	title, desc := renderSessionRow(d, it, selected, m.Width())
	if d.ShowDescription {
		_, _ = fmt.Fprintf(w, "%s\n%s", title, desc)
		return
	}
	_, _ = fmt.Fprint(w, title)
}

func renderSessionHeader(it item, selected bool) string {
	label := domain.GroupLabel(it.session.Group)
	count := ""
	if it.groupN > 0 {
		count = fmt.Sprintf(" · %d", it.groupN)
	}
	glyph := "▾"
	if it.collapsed {
		glyph = "▸"
	}
	headFg := headerLabelColor
	if selected {
		headFg = headerSelFg
	}
	head := lipgloss.NewStyle().Bold(true).Foreground(headFg).Render(glyph + " " + label + count)
	act := ""
	if it.activity != "" {
		act = lipgloss.NewStyle().Foreground(sessionStateColor(it)).Render(" • " + it.activity)
	}
	remote := ""
	if it.remoteName != "" {
		remote = lipgloss.NewStyle().Foreground(stateColorIdle).Render(" [" + it.remoteName + "]")
	}
	line := head + act + remote
	row := lipgloss.NewStyle().Padding(0, 0, 0, 2)
	if selected {
		row = row.Background(headerSelBg)
	}
	return row.Render(line)
}

func renderSessionRow(d sessionListDelegate, it item, selected bool, width int) (string, string) {
	prefix, activity := it.chrome()
	state := lipgloss.NewStyle().Foreground(sessionStateColor(it))
	if selected {
		state = state.Bold(true)
	}
	tree := lipgloss.NewStyle().Foreground(stateColorIdle).Render(it.treeBranch())
	nameStyle := d.Styles.NormalTitle.UnsetPadding().Foreground(stateColorName)
	if selected {
		nameStyle = d.Styles.SelectedTitle.UnsetPadding().UnsetBorderStyle().Foreground(lipgloss.Color("#EE6FF8"))
	}
	line := tree + state.Render(prefix) + nameStyle.Render(it.displayName()) + state.Render(activity)
	pad := 2
	if selected {
		pad = 1
	}
	textwidth := width - pad
	if textwidth < 8 {
		textwidth = 8
	}
	line = ansi.Truncate(line, textwidth, "…")
	titleStyle := d.Styles.NormalTitle.UnsetForeground()
	descStyle := d.Styles.NormalDesc
	if selected {
		titleStyle = d.Styles.SelectedTitle.UnsetForeground()
		descStyle = d.Styles.SelectedDesc
	}
	desc := it.Description()
	desc = ansi.Truncate(desc, textwidth, "…")
	return titleStyle.Render(line), descStyle.Render(desc)
}

func (m *Model) startQuickSession() (tea.Model, tea.Cmd) {
	name, err := domain.AssignSessionName(m.allSessions, "", true)
	if err != nil {
		m.log("quick session name: %v", err)
		m.state = viewStateMain
		return m, nil
	}
	m.sessionCfg.Name = name
	m.sessionCfg.Group = domain.GroupQuick
	m.sessionCfg.AdHoc = true
	m.sessionCfg.PromptFree = true
	m.sessionCfg.Quick = true
	if d := firstSyncingDelegation(m.selectedRemote); d != nil {
		_, region := m.cfg.ResolveAWSSessionDefaults(m.selectedRemote, d)
		m.sessionCfg.AWSConfig = &domain.AWSConfig{
			RoleName:        d.RoleName,
			AccountID:       d.AccountID,
			Region:          region,
			Regions:         d.Regions,
			SessionPolicy:   d.SessionPolicy,
			DurationSeconds: d.DurationSeconds,
		}
	}
	return m, m.startBackgroundCreate()
}

func (m *Model) handleRenameSessionUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
		m.renamingSessionID = ""
		m.state = viewStateMain
		return m, nil
	}
	var cmd tea.Cmd
	m.genericInput, cmd = m.genericInput.Update(msg)
	if !m.genericInput.Confirmed {
		return m, cmd
	}
	name := strings.Join(strings.Fields(strings.TrimSpace(m.genericInput.Value())), "-")
	sess, ok := m.sessionForRename()
	if !ok {
		m.renamingSessionID = ""
		m.state = viewStateMain
		return m, nil
	}
	if err := domain.ValidateSessionName(name); err != nil {
		m.genericInput.Confirmed = false
		m.genericInput.Error = "Start with a letter; use letters, digits, _ or - (max 48)."
		return m, nil
	}
	others := make([]domain.Session, 0, len(m.allSessions))
	for _, s := range m.allSessions {
		if s.ID != sess.ID && s.RemoteHost == sess.RemoteHost {
			others = append(others, s)
		}
	}
	if domain.NameTaken(others, name) {
		m.genericInput.Confirmed = false
		m.genericInput.Error = "That name is already in use on this remote."
		return m, nil
	}
	sess.Name = name
	if m.db != nil {
		if err := m.db.Save(context.Background(), &sess); err != nil {
			m.genericInput.Confirmed = false
			m.genericInput.Error = "Could not save: " + err.Error()
			return m, nil
		}
	}
	for i, s := range m.allSessions {
		if s.ID == sess.ID {
			m.allSessions[i].Name = name
			break
		}
	}
	m.renamingSessionID = ""
	m.applyRemoteFilter()
	m.state = viewStateMain
	return m, nil
}

func (m *Model) sessionForRename() (domain.Session, bool) {
	if m.renamingSessionID != "" {
		for _, s := range m.allSessions {
			if s.ID == m.renamingSessionID {
				return s, true
			}
		}
	}
	it, ok := m.selectedSessionItem()
	if !ok {
		return domain.Session{}, false
	}
	return it.session, true
}

func (m *Model) ensureRemoteServer(ctx context.Context, sshMgr *ssh.Manager) error {
	if sshMgr == nil {
		return nil
	}
	if _, err := sshMgr.Execute(ctx, "test -S ~/.aiman/aiman.sock"); err == nil {
		return nil
	}
	script := remotesvc.InstallEnableScript(remotesvc.KindServe)
	if _, err := execRemoteScript(sshMgr, ctx, "/tmp/aiman-install-serve.sh", script); err != nil {
		return fmt.Errorf("installing aiman serve: %w", err)
	}
	return nil
}
