package ui

import (
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type branchItem struct {
	name string
}

func (i branchItem) Title() string       { return i.name }
func (i branchItem) Description() string { return "" }
func (i branchItem) FilterValue() string { return i.name }

type BranchPickerModel struct {
	list     list.Model
	selected string
}

func NewBranchPickerModel(branches []string) BranchPickerModel {
	items := make([]list.Item, len(branches))
	for i, b := range branches {
		items[i] = branchItem{name: b}
	}

	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Select Existing Branch"
	l.SetShowFilter(true)
	l.FilterInput.Placeholder = "Type to filter branches..."
	l.FilterInput.Focus()
	l.KeyMap.NextPage = key.Binding{}
	l.KeyMap.PrevPage = key.Binding{}

	return BranchPickerModel{list: l}
}

func (m BranchPickerModel) Init() tea.Cmd { return nil }

func (m *BranchPickerModel) SetSize(width, height int) {
	h, v := docStyle.GetFrameSize()
	m.list.SetSize(width-h, height-v)
}

func (m BranchPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyMsg); ok {
		if km.String() == "enter" {
			if i, ok := m.list.SelectedItem().(branchItem); ok {
				m.selected = i.name
				return m, nil
			}
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m BranchPickerModel) View() string {
	if len(m.list.Items()) == 0 {
		return "\n  No remote branches found.\n  Press esc to go back."
	}
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("enter: select branch")
	return m.list.View() + "\n  " + hint
}
