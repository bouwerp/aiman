package ui

import (
	"fmt"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
)

type jiraItem struct {
	issue domain.Issue
}

func (i jiraItem) Title() string       { return fmt.Sprintf("%s: %s", i.issue.Key, i.issue.Summary) }
func (i jiraItem) Description() string { return i.issue.Status.String() }
func (i jiraItem) FilterValue() string { return i.issue.Key + " " + i.issue.Summary }

type IssuePickerModel struct {
	list     list.Model
	selected *domain.Issue
	loading  bool
}

func NewIssuePickerModel(issues []domain.Issue) IssuePickerModel {
	items := make([]list.Item, len(issues))
	for i, iss := range issues {
		items[i] = jiraItem{issue: iss}
	}

	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Select JIRA Issue"

	// Enable VSCode-style search/filter
	l.SetShowFilter(true)
	l.FilterInput.Placeholder = "Type to filter issues..."
	l.FilterInput.Focus()

	// Disable the default "n/p" bindings so they don't interfere with typing
	l.KeyMap.NextPage = key.Binding{}
	l.KeyMap.PrevPage = key.Binding{}

	return IssuePickerModel{
		list:    l,
		loading: false,
	}
}

func (m IssuePickerModel) Init() tea.Cmd {
	return nil
}

func (m *IssuePickerModel) SetSize(width, height int) {
	h, v := docStyle.GetFrameSize()
	m.list.SetSize(width-h, height-v)
}

func (m IssuePickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "enter" {
			if i, ok := m.list.SelectedItem().(jiraItem); ok {
				m.selected = &i.issue
				return m, nil
			}
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m IssuePickerModel) View() string {
	if m.loading && len(m.list.Items()) == 0 {
		return "\n  Loading issues from JIRA..."
	}
	if !m.loading && len(m.list.Items()) == 0 {
		return "\n  No JIRA issues found.\n  Press ESC to go back."
	}
	return m.list.View()
}
