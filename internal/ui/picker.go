package ui

import (
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type RepoItem struct {
	repo domain.Repo
}

func (i RepoItem) Title() string       { return i.repo.Name }
func (i RepoItem) Description() string { return i.repo.URL }
func (i RepoItem) FilterValue() string { return i.repo.Name }

type ownerItem struct {
	name string
}

func (i ownerItem) Title() string       { return i.name }
func (i ownerItem) Description() string { return "" }
func (i ownerItem) FilterValue() string { return i.name }

type repoPickerState int

const (
	repoStateList repoPickerState = iota
	repoStateOwnerPicker
	repoStateNameInput
)

type RepoPickerModel struct {
	list          list.Model
	selected      *domain.Repo
	state         repoPickerState
	ownersList    list.Model
	input         textinput.Model
	selectedOwner string
	config        *config.GitConfig
	Skip          bool
}

func NewRepoPickerModel(repos []domain.Repo, cfg *config.GitConfig) RepoPickerModel {
	items := make([]list.Item, len(repos))
	for i, r := range repos {
		items[i] = RepoItem{repo: r}
	}

	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Select a Repository"

	// Enable VSCode-style search/filter
	l.SetShowFilter(true)
	l.FilterInput.Placeholder = "Type to filter repositories..."
	l.FilterInput.Focus()

	// Disable the default "n/p" bindings so they don't interfere with typing
	l.KeyMap.NextPage = key.Binding{}
	l.KeyMap.PrevPage = key.Binding{}

	ti := textinput.New()
	ti.Placeholder = "Enter new repository name"
	ti.CharLimit = 100
	ti.Width = 50

	return RepoPickerModel{
		list:   l,
		config: cfg,
		input:  ti,
		state:  repoStateList,
	}
}

func (m *RepoPickerModel) initOwnersList() {
	var items []list.Item
	if m.config.IncludePersonal {
		items = append(items, ownerItem{name: "Personal (your account)"})
	}
	for _, org := range m.config.IncludeOrgs {
		items = append(items, ownerItem{name: org})
	}

	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Select Repository Owner"
	l.SetShowFilter(false)
	l.SetShowStatusBar(false)
	l.KeyMap.NextPage = key.Binding{}
	l.KeyMap.PrevPage = key.Binding{}

	// Set size based on current list size
	l.SetSize(m.list.Width(), m.list.Height())

	m.ownersList = l
}

func (m RepoPickerModel) Init() tea.Cmd {
	return nil
}

func (m *RepoPickerModel) SetSize(width, height int) {
	h, v := docStyle.GetFrameSize()
	m.list.SetSize(width-h, height-v)
	if m.state == repoStateOwnerPicker {
		m.ownersList.SetSize(width-h, height-v)
	}
}

func (m RepoPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch m.state {
		case repoStateList:
			switch msg.String() {
			case "enter":
				if i, ok := m.list.SelectedItem().(RepoItem); ok {
					m.selected = &i.repo
					return m, nil
				}
			case "ctrl+n":
				m.state = repoStateOwnerPicker
				m.initOwnersList()
				return m, nil
			case "ctrl+a":
				m.Skip = true
				return m, nil
			}

		case repoStateOwnerPicker:
			switch msg.String() {
			case "enter":
				if i, ok := m.ownersList.SelectedItem().(ownerItem); ok {
					m.selectedOwner = i.name
					if strings.HasPrefix(m.selectedOwner, "Personal") {
						m.selectedOwner = "" // Indicates personal
					}
					m.state = repoStateNameInput
					m.input.Focus()
					return m, nil
				}
			case "esc":
				m.state = repoStateList
				return m, nil
			}

			var cmd tea.Cmd
			m.ownersList, cmd = m.ownersList.Update(msg)
			return m, cmd

		case repoStateNameInput:
			switch msg.String() {
			case "enter":
				if m.input.Value() != "" {
					name := m.input.Value()
					fullName := name
					if m.selectedOwner != "" {
						fullName = m.selectedOwner + "/" + name
					}
					m.selected = &domain.Repo{
						Name:  fullName,
						IsNew: true,
					}
					return m, nil
				}
			case "esc":
				m.state = repoStateOwnerPicker
				m.input.Blur()
				return m, nil
			}

			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}
	}

	if m.state == repoStateList {
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m RepoPickerModel) View() string {
	switch m.state {
	case repoStateList:
		return docStyle.Render(m.list.View()) + "\n  " + lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("ctrl+n: create new repository • ctrl+a: skip (no repository)")

	case repoStateOwnerPicker:
		return docStyle.Render(m.ownersList.View())

	case repoStateNameInput:
		style := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(1, 2).
			BorderForeground(lipgloss.Color("62"))

		ownerDisplay := m.selectedOwner
		if ownerDisplay == "" {
			ownerDisplay = "Personal"
		}

		return lipgloss.Place(m.list.Width(), m.list.Height(),
			lipgloss.Center, lipgloss.Center,
			style.Render(fmt.Sprintf(
				"Create New Repository under %s\n\n%s\n\n(enter to confirm, esc to cancel)",
				ownerDisplay,
				m.input.View(),
			)))
	}

	return ""
}
