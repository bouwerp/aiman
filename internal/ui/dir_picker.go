package ui

import (
	"fmt"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type dirItem struct {
	path string
	name string
}

func (i dirItem) Title() string       { return i.name }
func (i dirItem) Description() string { return i.path }
func (i dirItem) FilterValue() string { return i.name }

type DirPickerModel struct {
	list       list.Model
	selected   string
	repo       domain.Repo
	createMode bool
	input      textinput.Model
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

	ti := textinput.New()
	ti.Placeholder = "Enter new directory path (e.g. src/new-feature)"
	ti.CharLimit = 100
	ti.Width = 50

	return DirPickerModel{
		list:  l,
		repo:  repo,
		input: ti,
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
	if msg, ok := msg.(tea.KeyMsg); ok {
		if m.createMode {
			switch msg.String() {
			case "enter":
				if m.input.Value() != "" {
					m.selected = m.input.Value()
					return m, nil
				}
			case "esc":
				m.createMode = false
				m.input.Blur()
				m.list.FilterInput.Focus()
				return m, nil
			}

			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd
		}

		switch msg.String() {
		case "enter":
			if i, ok := m.list.SelectedItem().(dirItem); ok {
				m.selected = i.path
				return m, nil
			}
		case "ctrl+n":
			m.createMode = true
			m.input.Focus()
			m.list.FilterInput.Blur()
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m DirPickerModel) View() string {
	if m.createMode {
		style := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(1, 2).
			BorderForeground(lipgloss.Color("62"))

		return lipgloss.Place(m.list.Width(), m.list.Height(),
			lipgloss.Center, lipgloss.Center,
			style.Render(fmt.Sprintf(
				"Define New Working Directory\n\n%s\n\n(enter to confirm, esc to cancel)",
				m.input.View(),
			)))
	}

	if len(m.list.Items()) == 0 {
		return "\n  No directories found in repository.\n  Press ctrl+n to create a new one, or ESC to go back."
	}

	return m.list.View() + "\n  " + lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("ctrl+n: define new folder")
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
