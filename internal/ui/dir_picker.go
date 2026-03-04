package ui

import (
	"fmt"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

type dirItem struct {
	path string
	name string
}

func (i dirItem) Title() string       { return i.name }
func (i dirItem) Description() string { return i.path }
func (i dirItem) FilterValue() string { return i.name }

type DirPickerModel struct {
	list     list.Model
	selected string
	repo     domain.Repo
}

func NewDirPickerModel(dirs []string, repo domain.Repo) DirPickerModel {
	items := make([]list.Item, len(dirs))
	for i, dir := range dirs {
		items[i] = dirItem{
			path: dir,
			name: extractDirName(dir),
		}
	}

	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = fmt.Sprintf("Select Working Directory for %s", repo.Name)

	// Enable VSCode-style search/filter
	l.SetShowFilter(true)
	l.FilterInput.Placeholder = "Type to filter directories..."
	l.FilterInput.Focus()

	// Disable the default "n/p" bindings so they don't interfere with typing
	l.KeyMap.NextPage = key.Binding{}
	l.KeyMap.PrevPage = key.Binding{}

	return DirPickerModel{
		list: l,
		repo: repo,
	}
}

func (m DirPickerModel) Init() tea.Cmd {
	return nil
}

func (m *DirPickerModel) SetSize(width, height int) {
	h, v := docStyle.GetFrameSize()
	m.list.SetSize(width-h, height-v)
}

func (m DirPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "enter" {
			if i, ok := m.list.SelectedItem().(dirItem); ok {
				m.selected = i.path
				return m, nil
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m DirPickerModel) View() string {
	if len(m.list.Items()) == 0 {
		return "\n  No directories found in repository.\n  Press ESC to go back."
	}
	return m.list.View()
}

func extractDirName(path string) string {
	// Extract the last component of the path
	parts := splitPath(path)
	if len(parts) == 0 {
		return path
	}
	return parts[len(parts)-1]
}

func splitPath(path string) []string {
	// Simple path splitting by /
	var parts []string
	current := ""
	for _, char := range path {
		if char == '/' {
			if current != "" {
				parts = append(parts, current)
				current = ""
			}
		} else {
			current += string(char)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}
