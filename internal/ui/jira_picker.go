package ui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/bouwerp/aiman/internal/domain"
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
	
	return IssuePickerModel{
		list:    l,
		loading: len(issues) == 0,
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
	return m.list.View()
}
