package ui

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

func groupedSessionItems(flat []item) []list.Item {
	if len(flat) == 0 {
		return nil
	}
	type bucket struct {
		key   string
		items []item
	}
	order := []string{}
	by := map[string]*bucket{}
	for _, it := range flat {
		g := it.session.Group
		if g == "" {
			g = domain.GroupUngrouped
		}
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
		groupName := b.items[0].session.Group
		if groupName == "" {
			groupName = domain.GroupUngrouped
		}
		header := item{
			header:     true,
			session:    domain.Session{Group: groupName},
			remoteName: b.items[0].remoteName,
			activity:   groupRollup(b.items),
			groupN:     len(b.items),
		}
		out = append(out, header)
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

func (d sessionListDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	it, ok := listItem.(item)
	if !ok || !it.header {
		d.DefaultDelegate.Render(w, m, index, listItem)
		return
	}
	// Headers are section labels: never the selected-session left border.
	label := lipgloss.NewStyle().
		Foreground(lipgloss.Color("62")).
		Bold(true).
		Padding(0, 0, 0, 2).
		Render(it.Title())
	_, _ = fmt.Fprint(w, label+"\n")
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
		m.state = viewStateMain
		return m, nil
	}
	var cmd tea.Cmd
	m.genericInput, cmd = m.genericInput.Update(msg)
	if !m.genericInput.Confirmed {
		return m, cmd
	}
	name := strings.TrimSpace(m.genericInput.Value())
	it, ok := m.selectedSessionItem()
	if !ok {
		m.state = viewStateMain
		return m, nil
	}
	if err := domain.ValidateSessionName(name); err != nil {
		m.genericInput.Confirmed = false
		m.log("%v", err)
		return m, nil
	}
	others := make([]domain.Session, 0, len(m.allSessions))
	for _, s := range m.allSessions {
		if s.ID != it.session.ID && s.RemoteHost == it.session.RemoteHost {
			others = append(others, s)
		}
	}
	if domain.NameTaken(others, name) {
		m.genericInput.Confirmed = false
		m.log("name %q already in use", name)
		return m, nil
	}
	sess := it.session
	sess.Name = name
	if m.db != nil {
		if err := m.db.Save(context.Background(), &sess); err != nil {
			m.log("rename save: %v", err)
		}
	}
	for i, s := range m.allSessions {
		if s.ID == sess.ID {
			m.allSessions[i].Name = name
			break
		}
	}
	m.applyRemoteFilter()
	m.state = viewStateMain
	return m, nil
}

func (m *Model) ensureRemoteServer(ctx context.Context, sshMgr *ssh.Manager) error {
	if sshMgr == nil {
		return nil
	}
	if _, err := sshMgr.Execute(ctx, "test -S ~/.aiman/aiman.sock"); err == nil {
		return nil
	}
	if _, err := sshMgr.Execute(ctx, "test -x ~/.local/bin/aiman"); err != nil {
		installCmd := "curl -sSfL https://raw.githubusercontent.com/bouwerp/aiman/main/install.sh | sh"
		if _, err := sshMgr.Execute(ctx, installCmd); err != nil {
			return fmt.Errorf("install aiman on remote: %w", err)
		}
	}
	_, _ = sshMgr.Execute(ctx, "tmux has-session -t aiman-serve 2>/dev/null || tmux new-session -d -s aiman-serve 'aiman serve'")
	return nil
}
