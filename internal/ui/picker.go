package ui

import (
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

type RepoItem struct {
	repo domain.Repo
}

func (i RepoItem) Title() string       { return i.repo.Name }
func (i RepoItem) Description() string { return i.repo.URL }
func (i RepoItem) FilterValue() string { return i.repo.Name }

type RepoPickerModel struct {
	list     list.Model
	selected *domain.Repo
}

func NewRepoPickerModel(repos []domain.Repo) RepoPickerModel {
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

	return RepoPickerModel{
		list: l,
	}
}

func (m RepoPickerModel) Init() tea.Cmd {
	return nil
}

func (m RepoPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "enter" {
			if i, ok := m.list.SelectedItem().(RepoItem); ok {
				m.selected = &i.repo
				return m, tea.Quit
			}
		}
		if msg.String() == "ctrl+c" || msg.String() == "esc" {
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		h, v := docStyle.GetFrameSize()
		m.list.SetSize(msg.Width-h, msg.Height-v)
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m RepoPickerModel) View() string {
	return docStyle.Render(m.list.View())
}
