package ui

import (
	"context"
	"fmt"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/review"
	"github.com/bouwerp/aiman/internal/usecase"
)

type prItem struct {
	pr review.PR
}

func (i prItem) Title() string {
	return fmt.Sprintf("#%d %s · %s · +%d -%d", i.pr.Number, i.pr.Title, i.pr.Author, i.pr.Additions, i.pr.Deletions)
}
func (i prItem) Description() string { return "" }
func (i prItem) FilterValue() string { return i.Title() }

type PRPickerModel struct {
	list     list.Model
	selected *review.PR
}

func NewPRPickerModel(prs []review.PR) PRPickerModel {
	items := make([]list.Item, len(prs))
	for i, p := range prs {
		items[i] = prItem{pr: p}
	}

	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Select a Pull Request"
	l.SetShowFilter(true)
	l.FilterInput.Placeholder = "Type to filter pull requests..."
	l.FilterInput.Focus()
	l.KeyMap.NextPage = key.Binding{}
	l.KeyMap.PrevPage = key.Binding{}

	return PRPickerModel{list: l}
}

func (m PRPickerModel) Init() tea.Cmd { return nil }

func (m *PRPickerModel) SetSize(width, height int) {
	h, v := docStyle.GetFrameSize()
	m.list.SetSize(width-h, height-v)
}

func (m PRPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyPressMsg); ok {
		if km.String() == "enter" {
			if i, ok := m.list.SelectedItem().(prItem); ok {
				pr := i.pr
				m.selected = &pr
				return m, nil
			}
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m PRPickerModel) viewString() string {
	if len(m.list.Items()) == 0 {
		return "\n  No open pull requests.\n  Press esc to go back."
	}
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("enter: select pull request")
	return m.list.View() + "\n  " + hint
}

func (m PRPickerModel) View() tea.View {
	return newView(m.viewString())
}

type prsMsg struct {
	prs []review.PR
	err error
}

func (m *Model) fetchPRs(slug string) tea.Cmd {
	remote := m.selectedRemote
	return func() tea.Msg {
		ctx := context.Background()
		if remote.Host == "" {
			return prsMsg{err: fmt.Errorf("no remote selected")}
		}
		mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
		if err := mgr.Connect(ctx); err != nil {
			return prsMsg{err: fmt.Errorf("failed to connect to remote: %w", err)}
		}
		defer mgr.Close()
		prs, err := usecase.ListOpenPRs(ctx, mgr, slug)
		return prsMsg{prs: prs, err: err}
	}
}

func (m *Model) handlePRPickerUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if km, ok := msg.(tea.KeyPressMsg); ok && km.String() == "esc" {
		if m.prPicker.list.FilterState() != list.Filtering {
			m.state = viewStateRepoPicker
			return m, nil
		}
	}
	var subModel tea.Model
	var cmd tea.Cmd
	subModel, cmd = m.prPicker.Update(msg)
	m.prPicker = subModel.(PRPickerModel)
	if m.prPicker.selected != nil {
		pr := m.prPicker.selected
		m.prPicker.selected = nil
		m.sessionCfg.ReviewPR = fmt.Sprintf("%s#%d", m.sessionCfg.Repo.Name, pr.Number)
		m.loadingMsg = "Scanning available agents..."
		m.loadingNext = viewStateAgentPicker
		m.state = viewStateLoading
		return m, m.fetchAgents()
	}
	return m, cmd
}
