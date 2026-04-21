package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/usecase"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// snapshotItem wraps a SessionSnapshot for use in a bubbletea list.
type snapshotItem struct {
	snap domain.SessionSnapshot
}

func (s snapshotItem) Title() string {
	title := s.snap.IssueKey
	if title == "" {
		title = s.snap.Branch
	}
	if title == "" {
		title = s.snap.SessionID[:8]
	}
	if s.snap.AgentName != "" {
		title += " · " + s.snap.AgentName
	}
	return title
}

func (s snapshotItem) Description() string {
	age := formatSnapshotAge(s.snap.CreatedAt)
	summary := s.snap.Summary
	if len(summary) > 80 {
		summary = summary[:77] + "…"
	}
	if summary == "" {
		summary = "(no summary)"
	}
	return fmt.Sprintf("%s — %s", age, summary)
}

func (s snapshotItem) FilterValue() string {
	return s.snap.IssueKey + " " + s.snap.Branch + " " + s.snap.RepoName + " " + s.snap.Summary
}

func formatSnapshotAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("Jan 2")
	}
}

// SnapshotBrowserModel is the TUI screen for browsing saved session snapshots.
type SnapshotBrowserModel struct {
	list          list.Model
	detail        viewport.Model
	snapshots     []domain.SessionSnapshot
	snapMgr       *usecase.SnapshotManager
	width         int
	height        int
	showDetail    bool
	confirmDelete *domain.SessionSnapshot // non-nil when delete confirm dialog is open
}

func NewSnapshotBrowserModel(width, height int, snapMgr *usecase.SnapshotManager) SnapshotBrowserModel {
	l := list.New(nil, list.NewDefaultDelegate(), width, height-4)
	l.Title = "Session Snapshots"
	l.SetShowHelp(true)
	return SnapshotBrowserModel{
		list:    l,
		detail:  viewport.New(width, height-6),
		width:   width,
		height:  height,
		snapMgr: snapMgr,
	}
}

type snapshotBrowserLoadedMsg struct {
	snapshots []domain.SessionSnapshot
	err       error
}

type snapshotDeletedMsg struct {
	err error
}

func loadAllSnapshotsCmd(snapMgr *usecase.SnapshotManager) tea.Cmd {
	return func() tea.Msg {
		if snapMgr == nil {
			return snapshotBrowserLoadedMsg{err: fmt.Errorf("snapshot manager unavailable")}
		}
		snaps, err := snapMgr.ListAllSnapshots(context.Background())
		return snapshotBrowserLoadedMsg{snapshots: snaps, err: err}
	}
}

func deleteSnapshotCmd(snapMgr *usecase.SnapshotManager, id string) tea.Cmd {
	return func() tea.Msg {
		if snapMgr == nil {
			return snapshotDeletedMsg{err: fmt.Errorf("snapshot manager unavailable")}
		}
		err := snapMgr.DeleteSnapshot(context.Background(), id)
		return snapshotDeletedMsg{err: err}
	}
}

func (m SnapshotBrowserModel) Init() tea.Cmd { return nil }

func (m SnapshotBrowserModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case snapshotBrowserLoadedMsg:
		items := make([]list.Item, len(msg.snapshots))
		for i, s := range msg.snapshots {
			items[i] = snapshotItem{snap: s}
		}
		m.snapshots = msg.snapshots
		m.list.SetItems(items)
		return m, nil

	case snapshotDeletedMsg:
		// Reload the list regardless of error (error will surface via empty list).
		return m, loadAllSnapshotsCmd(m.snapMgr)

	case tea.KeyMsg:
		// Delete confirmation dialog intercepts all keys first.
		if m.confirmDelete != nil {
			switch msg.String() {
			case "y", "Y":
				id := m.confirmDelete.ID
				m.confirmDelete = nil
				return m, deleteSnapshotCmd(m.snapMgr, id)
			default:
				m.confirmDelete = nil
				return m, nil
			}
		}

		switch msg.String() {
		case "enter":
			if sel := m.list.SelectedItem(); sel != nil && !m.showDetail {
				m.showDetail = true
				snap := sel.(snapshotItem).snap
				m.detail.SetContent(renderSnapshotDetail(snap, m.width-4))
				m.detail.GotoTop()
				return m, nil
			}
		case "d", "delete":
			if !m.showDetail {
				if sel := m.list.SelectedItem(); sel != nil {
					snap := sel.(snapshotItem).snap
					m.confirmDelete = &snap
					return m, nil
				}
			}
		case "esc", "backspace":
			if m.showDetail {
				m.showDetail = false
				return m, nil
			}
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.list.SetSize(msg.Width, msg.Height-4)
		m.detail = viewport.New(msg.Width, msg.Height-6)
	}

	if m.confirmDelete != nil {
		return m, nil
	}

	if m.showDetail {
		var cmd tea.Cmd
		m.detail, cmd = m.detail.Update(msg)
		return m, cmd
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m SnapshotBrowserModel) View() string {
	if m.confirmDelete != nil {
		return m.renderDeleteConfirm()
	}
	if m.showDetail {
		header := activeStyle.Render("Snapshot Detail") + "  " +
			lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("esc: back • ↑↓: scroll")
		return header + "\n" + m.detail.View()
	}
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("enter: detail • d: delete • esc: close")
	return m.list.View() + "\n" + hint
}

func (m SnapshotBrowserModel) renderDeleteConfirm() string {
	snap := m.confirmDelete
	title := snap.IssueKey
	if title == "" {
		title = snap.Branch
	}
	if title == "" {
		title = snap.SessionID[:8]
	}

	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	warn := lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)

	var b strings.Builder
	b.WriteString(warn.Render("Delete Snapshot") + "\n\n")
	b.WriteString(fmt.Sprintf("%s\n", title))
	b.WriteString(muted.Render(fmt.Sprintf("%s  ·  %s", snap.RepoName, snap.CreatedAt.Format("2006-01-02 15:04"))) + "\n\n")
	b.WriteString("This cannot be undone.\n\n")
	b.WriteString(activeStyle.Render("[y]") + " Delete  " + activeStyle.Render("[any]") + " Cancel")

	dialog := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(1, 3).
		Width(50)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog.Render(b.String()))
}

func renderSnapshotDetail(snap domain.SessionSnapshot, width int) string {
	wrap := lipgloss.NewStyle().Width(width)
	var b strings.Builder

	// Header
	title := snap.IssueKey
	if title == "" {
		title = snap.Branch
	}
	if title == "" {
		title = snap.SessionID
	}
	b.WriteString(activeStyle.Render(title) + "\n")
	meta := []string{}
	if snap.AgentName != "" {
		meta = append(meta, snap.AgentName)
	}
	if snap.RepoName != "" {
		meta = append(meta, snap.RepoName)
	}
	if snap.Branch != "" && snap.Branch != title {
		meta = append(meta, snap.Branch)
	}
	meta = append(meta, snap.CreatedAt.Format("2006-01-02 15:04"))
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render(strings.Join(meta, " · ")) + "\n")

	injected := "never"
	if snap.InjectedAt != nil {
		injected = snap.InjectedAt.Format("2006-01-02 15:04")
	}
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("Injected: "+injected) + "\n\n")

	// Summary
	b.WriteString(activeStyle.Render("Summary") + "\n")
	if snap.Summary != "" {
		b.WriteString(wrap.Render(snap.Summary) + "\n\n")
	} else {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("(no summary)") + "\n\n")
	}

	// Next steps
	if len(snap.NextSteps) > 0 {
		b.WriteString(activeStyle.Render("Next Steps") + "\n")
		for _, s := range snap.NextSteps {
			lines := strings.Split(wrap.Width(width-4).Render(s), "\n")
			b.WriteString("  • " + lines[0] + "\n")
			for _, l := range lines[1:] {
				b.WriteString("    " + l + "\n")
			}
		}
		b.WriteString("\n")
	}

	// Agent state badge
	stateColor := "2"
	switch snap.AgentState {
	case domain.AgentStateErrored:
		stateColor = "1"
	case domain.AgentStateWaitingInput:
		stateColor = "3"
	case domain.AgentStateIdle:
		stateColor = "241"
	}
	b.WriteString(activeStyle.Render("Agent State") + " " +
		lipgloss.NewStyle().Foreground(lipgloss.Color(stateColor)).Render(string(snap.AgentState)) + "\n\n")

	return b.String()
}

