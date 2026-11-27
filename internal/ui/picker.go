package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

type RepoItem struct {
	repo   domain.Repo
	marked bool
}

func (i RepoItem) Title() string {
	if i.marked {
		return "[x] " + i.repo.Name
	}
	return i.repo.Name
}
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
	// Single turns marking off: existing-branch, review and autonomous
	// creates pick one repository.
	Single bool
	// marked holds the repos marked with space, in mark order.
	marked []domain.Repo
	// workspace is set by enter with 2+ marks.
	workspace []domain.Repo
	markNote  string
}

// MaxWorkspaceRepos is the most repositories one workspace session holds.
const MaxWorkspaceRepos = 8

// Marked returns the marked repositories in mark order.
func (m RepoPickerModel) Marked() []domain.Repo { return m.marked }

// Workspace returns the marked repositories once enter finished a multi-repo
// pick (2+ marks), else nil.
func (m RepoPickerModel) Workspace() []domain.Repo { return m.workspace }

func (m RepoPickerModel) markIndex(name string) int {
	for i, r := range m.marked {
		if r.Name == name {
			return i
		}
	}
	return -1
}

func (m *RepoPickerModel) toggleMark() {
	i, ok := m.list.SelectedItem().(RepoItem)
	if !ok {
		return
	}
	m.markNote = ""
	if at := m.markIndex(i.repo.Name); at >= 0 {
		m.marked = append(m.marked[:at:at], m.marked[at+1:]...)
		m.refreshMarks()
		return
	}
	if len(m.marked) >= MaxWorkspaceRepos {
		m.markNote = fmt.Sprintf("at most %d repositories", MaxWorkspaceRepos)
		return
	}
	m.marked = append(m.marked, i.repo)
	m.refreshMarks()
}

// refreshMarks redraws the items so marked rows carry "[x] ".
func (m *RepoPickerModel) refreshMarks() {
	items := m.list.Items()
	for idx, it := range items {
		if ri, ok := it.(RepoItem); ok {
			ri.marked = m.markIndex(ri.repo.Name) >= 0
			items[idx] = ri
		}
	}
	m.list.SetItems(items)
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
	ti.SetWidth(50)

	return RepoPickerModel{
		list:   l,
		config: cfg,
		input:  ti,
		state:  repoStateList,
	}
}

func (m *RepoPickerModel) initOwnersList() {
	var items []list.Item
	if config.PersonalReposEnabled(m.config) {
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
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		switch m.state {
		case repoStateList:
			switch msg.String() {
			case "space":
				if !m.Single && m.list.FilterState() != list.Filtering {
					m.toggleMark()
					return m, nil
				}
			case "enter":
				switch {
				case len(m.marked) >= 2:
					m.workspace = append([]domain.Repo(nil), m.marked...)
					return m, nil
				case len(m.marked) == 1:
					r := m.marked[0]
					m.selected = &r
					return m, nil
				}
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

func (m RepoPickerModel) viewString() string {
	switch m.state {
	case repoStateList:
		dim := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
		help := "ctrl+n: create new repository • ctrl+a: skip (no repository)"
		if !m.Single {
			help = "space: mark (multi-repo) • " + help
		}
		out := docStyle.Render(m.list.View()) + "\n  " + dim.Render(help)
		if n := len(m.marked); n > 0 {
			out += "\n  " + dim.Render(fmt.Sprintf("%d marked: enter creates one session over them", n))
		}
		if m.markNote != "" {
			out += "\n  " + lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Render(m.markNote)
		}
		return out

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

func (m RepoPickerModel) View() tea.View {
	v := tea.NewView(m.viewString())
	return v
}
