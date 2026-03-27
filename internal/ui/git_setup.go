package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/git"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type orgItem struct {
	name     string
	selected bool
}

func (i orgItem) Title() string {
	if i.selected {
		return "✓ " + i.name
	}
	return "  " + i.name
}

func (i orgItem) Description() string {
	if i.selected {
		return "Selected"
	}
	return "Not selected"
}

func (i orgItem) FilterValue() string {
	return i.name
}

type GitSetupModel struct {
	cfg            *config.Config
	personalToggle bool
	orgsList       list.Model
	includeInput   textinput.Model
	excludeInput   textinput.Model
	focusIndex     int
	err            error
	saved          bool
	loading        bool
	loadedOrgs     []string
}

func NewGitSetupModel(cfg *config.Config) GitSetupModel {
	m := GitSetupModel{
		cfg:            cfg,
		personalToggle: config.PersonalReposEnabled(&cfg.Git),
		loading:        true,
	}

	// Initialize orgs list (empty, will be populated)
	l := list.New([]list.Item{}, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Select Organizations (Space to toggle, Enter to confirm)"
	l.SetShowFilter(true)
	l.FilterInput.Placeholder = "Filter organizations..."
	l.KeyMap.NextPage = key.Binding{}
	l.KeyMap.PrevPage = key.Binding{}
	m.orgsList = l

	// Include patterns input
	m.includeInput = textinput.New()
	m.includeInput.Placeholder = "Include patterns (comma-separated regex)"
	m.includeInput.SetValue(strings.Join(cfg.Git.IncludePatterns, ", "))
	m.includeInput.Cursor.Style = cursorStyle

	// Exclude patterns input
	m.excludeInput = textinput.New()
	m.excludeInput.Placeholder = "Exclude patterns (comma-separated regex)"
	m.excludeInput.SetValue(strings.Join(cfg.Git.ExcludePatterns, ", "))
	m.excludeInput.Cursor.Style = cursorStyle

	return m
}

func (m GitSetupModel) Init() tea.Cmd {
	return tea.Batch(
		textinput.Blink,
		fetchOrgs(),
	)
}

type orgsMsg struct {
	orgs []string
	err  error
}

func fetchOrgs() tea.Cmd {
	return func() tea.Msg {
		ctx := context.Background()
		orgs, err := git.FetchOrganizations(ctx)
		return orgsMsg{orgs: orgs, err: err}
	}
}

func (m GitSetupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			return m, nil

		case "up", "down":
			// Navigate through focusable items
			if msg.String() == "up" {
				m.focusIndex--
				if m.focusIndex < 0 {
					m.focusIndex = 4
				}
			} else {
				m.focusIndex++
				if m.focusIndex > 4 {
					m.focusIndex = 0
				}
			}
			return m, m.updateFocus()

		case "tab":
			m.focusIndex++
			if m.focusIndex > 4 {
				m.focusIndex = 0
			}
			return m, m.updateFocus()

		case "shift+tab":
			m.focusIndex--
			if m.focusIndex < 0 {
				m.focusIndex = 4
			}
			return m, m.updateFocus()

		case "enter":
			if m.focusIndex == 0 {
				// Toggle personal repos
				m.personalToggle = !m.personalToggle
				return m, nil
			}
			if m.focusIndex == 4 {
				return m.save()
			}
			// Move to next field
			m.focusIndex++
			if m.focusIndex > 4 {
				m.focusIndex = 0
			}
			return m, m.updateFocus()

		case " ":
			// Toggle org selection when in orgs list
			if m.focusIndex == 1 {
				if sel := m.orgsList.SelectedItem(); sel != nil {
					org := sel.(orgItem)
					org.selected = !org.selected
					// Update the item in the list
					idx := m.orgsList.Index()
					m.orgsList.RemoveItem(idx)
					m.orgsList.InsertItem(idx, org)
				}
				return m, nil
			}
		}

	case orgsMsg:
		m.loading = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.loadedOrgs = msg.orgs
			// Create list items, marking selected ones
			selectedMap := make(map[string]bool)
			for _, org := range m.cfg.Git.IncludeOrgs {
				selectedMap[org] = true
			}

			items := make([]list.Item, len(msg.orgs))
			for i, org := range msg.orgs {
				items[i] = orgItem{
					name:     org,
					selected: selectedMap[org],
				}
			}
			m.orgsList.SetItems(items)
		}
		return m, nil
	}

	// Update focused component
	var cmd tea.Cmd
	switch m.focusIndex {
	case 1:
		m.orgsList, cmd = m.orgsList.Update(msg)
	case 2:
		m.includeInput, cmd = m.includeInput.Update(msg)
	case 3:
		m.excludeInput, cmd = m.excludeInput.Update(msg)
	}

	return m, cmd
}

func (m *GitSetupModel) updateFocus() tea.Cmd {
	// Blur all inputs
	m.includeInput.Blur()
	m.excludeInput.Blur()

	switch m.focusIndex {
	case 2:
		return m.includeInput.Focus()
	case 3:
		return m.excludeInput.Focus()
	}
	return nil
}

func (m GitSetupModel) save() (tea.Model, tea.Cmd) {
	// Collect selected orgs from the list
	var selectedOrgs []string
	for _, item := range m.orgsList.Items() {
		if org, ok := item.(orgItem); ok && org.selected {
			selectedOrgs = append(selectedOrgs, org.name)
		}
	}

	p := m.personalToggle
	m.cfg.Git.IncludePersonal = &p
	m.cfg.Git.IncludeOrgs = selectedOrgs
	m.cfg.Git.IncludePatterns = parseCommaList(m.includeInput.Value())
	m.cfg.Git.ExcludePatterns = parseCommaList(m.excludeInput.Value())

	if err := m.cfg.Save(); err != nil {
		m.err = err
		return m, nil
	}

	m.saved = true
	return m, nil
}

func (m *GitSetupModel) SetSize(width, height int) {
	h, v := docStyle.GetFrameSize()
	// Adjust height for the orgs list (leave room for other elements)
	listHeight := height - v - 20
	if listHeight < 10 {
		listHeight = 10
	}
	m.orgsList.SetSize(width-h, listHeight)
}

func parseCommaList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func (m GitSetupModel) View() string {
	if m.saved {
		return "Git configuration saved!\n"
	}

	var b strings.Builder
	b.WriteString("Git Repository Configuration\n")
	b.WriteString("Configure which repositories appear in the picker\n\n")

	// Personal repos toggle
	personalStatus := "[ ]"
	if m.personalToggle {
		personalStatus = "[✓]"
	}
	label := fmt.Sprintf("%s Include personal repositories", personalStatus)
	if m.focusIndex == 0 {
		b.WriteString(activeStyle.Render(label) + "\n")
	} else {
		b.WriteString(label + "\n")
	}

	b.WriteString("\n")

	// Orgs list
	label = "Organizations:"
	if m.focusIndex == 1 {
		label = activeStyle.Render(label)
	}
	b.WriteString(label + "\n")
	switch {
	case m.loading:
		b.WriteString("  Loading organizations from GitHub...\n")
	case m.err != nil:
		b.WriteString(fmt.Sprintf("  Error: %v\n", m.err))
	case len(m.loadedOrgs) == 0:
		b.WriteString("  No organizations found\n")
	default:
		b.WriteString(m.orgsList.View())
	}
	b.WriteString("\n")

	// Include patterns
	label = "Include patterns (regex, comma-separated):"
	if m.focusIndex == 2 {
		label = activeStyle.Render(label)
	}
	b.WriteString(label + "\n")
	b.WriteString(m.includeInput.View())
	b.WriteString("\n\n")

	// Exclude patterns
	label = "Exclude patterns (regex, comma-separated):"
	if m.focusIndex == 3 {
		label = activeStyle.Render(label)
	}
	b.WriteString(label + "\n")
	b.WriteString(m.excludeInput.View())
	b.WriteString("\n\n")

	// Save button
	saveLabel := "[ Save ]"
	if m.focusIndex == 4 {
		saveLabel = activeStyle.Render(saveLabel)
	}
	b.WriteString(saveLabel + "\n")

	if m.err != nil {
		b.WriteString("\n" + failStyle.Render(fmt.Sprintf("Error: %v", m.err)) + "\n")
	}

	b.WriteString("\n(↑/↓ or tab to navigate, space to toggle org, enter to select/save, esc to cancel)\n")

	return docStyle.Render(b.String())
}
