package ui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type assignChoice struct {
	label string
	group string
	isNew bool
}

func (m *Model) dispatchGroupEdit(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch m.state {
	case viewStateRenameGroup:
		model, cmd := m.handleRenameGroupUpdate(msg)
		return model, cmd, true
	case viewStateAssignGroup:
		model, cmd := m.handleAssignGroupUpdate(msg)
		return model, cmd, true
	case viewStateNewGroup:
		model, cmd := m.handleNewGroupUpdate(msg)
		return model, cmd, true
	default:
		return m, nil, false
	}
}

func groupCollapseKey(group, remoteName string) string {
	return domain.GroupLabel(group) + "\x00" + remoteName
}

func (m *Model) toggleSelectedGroupCollapsed() (tea.Model, tea.Cmd, bool) {
	it, ok := m.list.SelectedItem().(item)
	if !ok || !it.header {
		return m, nil, false
	}
	if m.collapsedGroups == nil {
		m.collapsedGroups = map[string]bool{}
	}
	key := groupCollapseKey(it.session.Group, it.remoteName)
	m.collapsedGroups[key] = !m.collapsedGroups[key]
	m.applyRemoteFilter()
	return m, nil, true
}

func (m *Model) startRenameGroup(it item) (tea.Model, tea.Cmd, bool) {
	m.renamingGroup = domain.GroupLabel(it.session.Group)
	m.renamingGroupHost = it.session.RemoteHost
	m.genericInput = NewTextInputModel("Rename group", "group", m.renamingGroup)
	m.state = viewStateRenameGroup
	return m, m.genericInput.Init(), true
}

func (m *Model) startAssignGroup(sess domain.Session) (tea.Model, tea.Cmd, bool) {
	m.assigningSessionID = sess.ID
	m.assignChoices = m.buildAssignChoices(sess)
	m.assignCursor = 0
	m.state = viewStateAssignGroup
	return m, nil, true
}

func (m *Model) buildAssignChoices(sess domain.Session) []assignChoice {
	seen := map[string]bool{domain.GroupUngrouped: true}
	choices := []assignChoice{{label: "ungrouped (remove from group)", group: domain.GroupUngrouped}}
	var names []string
	for _, s := range m.allSessions {
		if s.RemoteHost != sess.RemoteHost {
			continue
		}
		g := domain.GroupLabel(s.Group)
		if seen[g] {
			continue
		}
		seen[g] = true
		names = append(names, g)
	}
	sort.Strings(names)
	cur := domain.GroupLabel(sess.Group)
	for _, g := range names {
		label := g
		if g == cur {
			label = g + "  (current)"
		}
		choices = append(choices, assignChoice{label: label, group: g})
	}
	choices = append(choices, assignChoice{label: "+ new group…", isNew: true})
	return choices
}

func (m *Model) handleRenameGroupUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
		m.renamingGroup = ""
		m.renamingGroupHost = ""
		m.state = viewStateMain
		return m, nil
	}
	var cmd tea.Cmd
	m.genericInput, cmd = m.genericInput.Update(msg)
	if !m.genericInput.Confirmed {
		return m, cmd
	}
	name, err := domain.NormalizeGroupName(m.genericInput.Value())
	if err != nil {
		m.genericInput.Confirmed = false
		m.genericInput.Error = "Start with a letter; use letters, digits, _ or - (max 48)."
		return m, nil
	}
	old := domain.GroupLabel(m.renamingGroup)
	host := m.renamingGroupHost
	if err := m.renameGroupOnHost(host, old, name); err != nil {
		m.genericInput.Confirmed = false
		m.genericInput.Error = err.Error()
		return m, nil
	}
	if m.collapsedGroups != nil {
		oldKey := groupCollapseKey(old, remoteNameForHost(m.cfg, host))
		newKey := groupCollapseKey(name, remoteNameForHost(m.cfg, host))
		if m.collapsedGroups[oldKey] {
			delete(m.collapsedGroups, oldKey)
			m.collapsedGroups[newKey] = true
		}
	}
	m.renamingGroup = ""
	m.renamingGroupHost = ""
	m.applyRemoteFilter()
	m.state = viewStateMain
	return m, nil
}

func (m *Model) handleAssignGroupUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	n := len(m.assignChoices)
	switch {
	case km.String() == "esc" || km.String() == "q":
		m.state = viewStateMain
		return m, nil
	case km.String() == "up" || km.String() == "k":
		if m.assignCursor > 0 {
			m.assignCursor--
		}
		return m, nil
	case km.String() == "down" || km.String() == "j":
		if m.assignCursor < n-1 {
			m.assignCursor++
		}
		return m, nil
	case isConfirmKey(km):
		if n == 0 || m.assignCursor < 0 || m.assignCursor >= n {
			return m, nil
		}
		ch := m.assignChoices[m.assignCursor]
		if ch.isNew {
			m.genericInput = NewTextInputModel("New group", "group name", "")
			m.state = viewStateNewGroup
			return m, m.genericInput.Init()
		}
		if err := m.setSessionGroup(m.assigningSessionID, ch.group); err != nil {
			m.log("assign group: %v", err)
		}
		m.applyRemoteFilter()
		m.state = viewStateMain
		return m, nil
	}
	return m, nil
}

func (m *Model) handleNewGroupUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok && km.String() == "esc" {
		m.state = viewStateAssignGroup
		return m, nil
	}
	var cmd tea.Cmd
	m.genericInput, cmd = m.genericInput.Update(msg)
	if !m.genericInput.Confirmed {
		return m, cmd
	}
	name, err := domain.NormalizeGroupName(m.genericInput.Value())
	if err != nil {
		m.genericInput.Confirmed = false
		m.genericInput.Error = "Start with a letter; use letters, digits, _ or - (max 48)."
		return m, nil
	}
	if err := m.setSessionGroup(m.assigningSessionID, name); err != nil {
		m.genericInput.Confirmed = false
		m.genericInput.Error = err.Error()
		return m, nil
	}
	m.applyRemoteFilter()
	m.state = viewStateMain
	return m, nil
}

func (m *Model) renameGroupOnHost(host, oldLabel, newLabel string) error {
	for i, s := range m.allSessions {
		if s.RemoteHost != host || domain.GroupLabel(s.Group) != oldLabel {
			continue
		}
		s.Group = newLabel
		if m.db != nil && !domain.IsEphemeralSessionID(s.ID) {
			if err := m.db.Save(context.Background(), &s); err != nil {
				return fmt.Errorf("could not save: %w", err)
			}
		}
		m.allSessions[i].Group = newLabel
		if cs, ok := m.creatingSessions[s.ID]; ok {
			cs.placeholder.Group = newLabel
		}
	}
	return nil
}

func (m *Model) setSessionGroup(id, group string) error {
	for i, s := range m.allSessions {
		if s.ID != id {
			continue
		}
		s.Group = group
		if m.db != nil && !domain.IsEphemeralSessionID(id) {
			if err := m.db.Save(context.Background(), &s); err != nil {
				return err
			}
		}
		m.allSessions[i].Group = group
		if cs, ok := m.creatingSessions[id]; ok {
			cs.placeholder.Group = group
		}
		return nil
	}
	return fmt.Errorf("session not found")
}

func (m *Model) renderAssignGroupView() string {
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	selStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57"))
	var b strings.Builder
	b.WriteString("\n  " + titleStyle.Render("Assign group") + "\n\n")
	b.WriteString("  Move this session into a group, ungrouped, or a new group.\n\n")
	for i, ch := range m.assignChoices {
		line := "  " + ch.label
		if i == m.assignCursor {
			line = selStyle.Render(line)
		}
		b.WriteString(line + "\n")
	}
	b.WriteString("\n  " + dimStyle.Render("enter select  esc back") + "\n")
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, b.String())
}
