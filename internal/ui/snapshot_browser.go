package ui

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/usecase"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	snapshotListRatio    = 35   // % of total width for left list pane
	snapshotPreviewChars = 1500 // chars of head/tail to show in pane preview
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
	if title == "" && len(s.snap.SessionID) >= 8 {
		title = s.snap.SessionID[:8]
	}
	if s.snap.AgentName != "" {
		title += " · " + s.snap.AgentName
	}
	return title
}

func (s snapshotItem) Description() string {
	age := formatSnapshotAge(s.snap.CreatedAt)
	// Prefer first overview sentence for the short description
	summary := ""
	if len(s.snap.Overview) > 0 {
		summary = s.snap.Overview[0]
	} else {
		summary = s.snap.Summary
	}
	if utf8.RuneCountInString(summary) > 60 {
		runes := []rune(summary)
		summary = string(runes[:57]) + "…"
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
	focusLeft     bool // true = list focused, false = detail viewport focused
	confirmDelete *domain.SessionSnapshot
}

func NewSnapshotBrowserModel(width, height int, snapMgr *usecase.SnapshotManager) SnapshotBrowserModel {
	lw, dw, dh := snapshotPaneSizes(width, height)
	l := list.New(nil, list.NewDefaultDelegate(), lw, height-4)
	l.Title = "Session Snapshots"
	l.SetShowHelp(false)
	return SnapshotBrowserModel{
		list:      l,
		detail:    viewport.New(dw, dh),
		width:     width,
		height:    height,
		focusLeft: true,
		snapMgr:   snapMgr,
	}
}

// snapshotPaneSizes returns (listWidth, detailWidth, detailHeight).
func snapshotPaneSizes(w, h int) (int, int, int) {
	lw := w * snapshotListRatio / 100
	if lw < 24 {
		lw = 24
	}
	dw := w - lw - 1 // 1 for divider
	if dw < 20 {
		dw = 20
	}
	dh := h - 4
	if dh < 4 {
		dh = 4
	}
	return lw, dw, dh
}

// refreshDetail re-renders the detail viewport for the currently selected snapshot.
func (m *SnapshotBrowserModel) refreshDetail() {
	if sel := m.list.SelectedItem(); sel != nil {
		snap := sel.(snapshotItem).snap
		_, dw, _ := snapshotPaneSizes(m.width, m.height)
		m.detail.SetContent(renderSnapshotDetail(snap, dw-2))
	} else {
		m.detail.SetContent(lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Render("No snapshots saved."))
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
		m.refreshDetail()
		return m, nil

	case snapshotDeletedMsg:
		return m, loadAllSnapshotsCmd(m.snapMgr)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		lw, dw, dh := snapshotPaneSizes(m.width, m.height)
		m.list.SetSize(lw, m.height-4)
		m.detail = viewport.New(dw, dh)
		m.refreshDetail()
		return m, nil

	case tea.KeyMsg:
		// Delete confirmation dialog intercepts all keys.
		if m.confirmDelete != nil {
			switch msg.String() {
			case "y", "Y":
				id := m.confirmDelete.ID
				m.confirmDelete = nil
				return m, deleteSnapshotCmd(m.snapMgr, id)
			default:
				m.confirmDelete = nil
			}
			return m, nil
		}

		switch msg.String() {
		case "tab":
			m.focusLeft = !m.focusLeft
			return m, nil
		case "d", "delete":
			if m.focusLeft {
				if sel := m.list.SelectedItem(); sel != nil {
					snap := sel.(snapshotItem).snap
					m.confirmDelete = &snap
				}
			}
			return m, nil
		}
	}

	if m.confirmDelete != nil {
		return m, nil
	}

	if m.focusLeft {
		prevIdx := m.list.Index()
		var cmd tea.Cmd
		m.list, cmd = m.list.Update(msg)
		if m.list.Index() != prevIdx {
			m.detail.GotoTop()
			m.refreshDetail()
		}
		return m, cmd
	}

	// Detail pane focused.
	var cmd tea.Cmd
	m.detail, cmd = m.detail.Update(msg)
	return m, cmd
}

func (m SnapshotBrowserModel) View() string {
	if m.confirmDelete != nil {
		return m.renderDeleteConfirm()
	}

	lw, dw, dh := snapshotPaneSizes(m.width, m.height)

	// Left pane — list
	listStyle := lipgloss.NewStyle().Width(lw)
	leftContent := listStyle.Render(m.list.View())

	// Divider
	dividerChar := "│"
	dividerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("236"))
	dividerLines := make([]string, dh+4)
	for i := range dividerLines {
		dividerLines[i] = dividerStyle.Render(dividerChar)
	}
	divider := strings.Join(dividerLines, "\n")

	// Right pane — detail viewport
	detailBorder := lipgloss.NewStyle().Width(dw)
	if !m.focusLeft {
		detailBorder = detailBorder.BorderLeft(false)
	}
	detailHeader := m.renderDetailHeader(dw)
	rightContent := detailBorder.Render(detailHeader + "\n" + m.detail.View())

	body := lipgloss.JoinHorizontal(lipgloss.Top, leftContent, divider, rightContent)

	focusHint := "tab: focus detail"
	if !m.focusLeft {
		focusHint = "tab: focus list"
	}
	hint := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).
		Render(fmt.Sprintf("↑↓: navigate • d: delete • %s • esc: close", focusHint))

	return body + "\n" + hint
}

func (m SnapshotBrowserModel) renderDetailHeader(dw int) string {
	scrollPct := ""
	if !m.focusLeft {
		scrollPct = fmt.Sprintf(" %d%%", int(m.detail.ScrollPercent()*100))
	}
	focusIndicator := ""
	if !m.focusLeft {
		focusIndicator = activeStyle.Render(" [DETAIL]")
	}
	label := lipgloss.NewStyle().Foreground(lipgloss.Color("241")).
		Width(dw).
		Render("Session Detail" + focusIndicator + scrollPct)
	return label
}

func (m SnapshotBrowserModel) renderDeleteConfirm() string {
	snap := m.confirmDelete
	title := snap.IssueKey
	if title == "" {
		title = snap.Branch
	}
	if title == "" && len(snap.SessionID) >= 8 {
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
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	heading := func(s string) string {
		return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("33")).Render(s)
	}
	wrapStyle := lipgloss.NewStyle().Width(width)
	bullet := "  • "
	arrow := "  → "
	warn := "  ⚠ "

	var b strings.Builder

	// ── Header ─────────────────────────────────────────────────────────────
	title := snap.IssueKey
	if title == "" {
		title = snap.Branch
	}
	if title == "" {
		title = snap.SessionID
	}
	b.WriteString(activeStyle.Render(title) + "\n")

	meta := []string{}
	if snap.RepoName != "" {
		meta = append(meta, snap.RepoName)
	}
	if snap.Branch != "" && snap.Branch != title {
		meta = append(meta, "branch: "+snap.Branch)
	}
	if snap.WorktreePath != "" {
		meta = append(meta, "worktree: "+snap.WorktreePath)
	}
	if snap.AgentName != "" {
		meta = append(meta, snap.AgentName)
	}
	meta = append(meta, snap.CreatedAt.Format("2006-01-02 15:04"))
	b.WriteString(muted.Render(strings.Join(meta, " · ")) + "\n")

	injected := "never injected"
	if snap.InjectedAt != nil {
		injected = "injected " + snap.InjectedAt.Format("2006-01-02 15:04")
	}
	b.WriteString(muted.Render(injected) + "\n\n")

	// ── Agent state badge ──────────────────────────────────────────────────
	stateColor := "2"
	switch snap.AgentState {
	case domain.AgentStateErrored:
		stateColor = "1"
	case domain.AgentStateWaitingInput:
		stateColor = "3"
	case domain.AgentStateIdle:
		stateColor = "241"
	}
	b.WriteString(heading("Agent State") + " " +
		lipgloss.NewStyle().Foreground(lipgloss.Color(stateColor)).Render(string(snap.AgentState)) + "\n\n")

	// ── Overview ──────────────────────────────────────────────────────────
	if len(snap.Overview) > 0 {
		b.WriteString(heading("Overview") + "\n")
		for _, sentence := range snap.Overview {
			b.WriteString(wrapStyle.Render(sentence) + "\n")
		}
		b.WriteString("\n")
	} else if snap.Summary != "" {
		b.WriteString(heading("Overview") + "\n")
		b.WriteString(wrapStyle.Render(snap.Summary) + "\n\n")
	}

	// ── Details ───────────────────────────────────────────────────────────
	if len(snap.Details) > 0 {
		b.WriteString(heading("Details") + "\n")
		for _, item := range snap.Details {
			lines := strings.Split(wrapStyle.Width(width-len(bullet)).Render(item), "\n")
			b.WriteString(bullet + lines[0] + "\n")
			for _, l := range lines[1:] {
				b.WriteString("    " + l + "\n")
			}
		}
		b.WriteString("\n")
	}

	// ── Actions (needs immediate attention) ───────────────────────────────
	if len(snap.Actions) > 0 {
		b.WriteString(heading("Actions Needed") + "\n")
		for _, item := range snap.Actions {
			lines := strings.Split(wrapStyle.Width(width-len(warn)).Render(item), "\n")
			b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("3")).Render(warn+lines[0]) + "\n")
			for _, l := range lines[1:] {
				b.WriteString("    " + l + "\n")
			}
		}
		b.WriteString("\n")
	}

	// ── Next Steps ────────────────────────────────────────────────────────
	if len(snap.NextSteps) > 0 {
		b.WriteString(heading("Next Steps") + "\n")
		for _, item := range snap.NextSteps {
			lines := strings.Split(wrapStyle.Width(width-len(arrow)).Render(item), "\n")
			b.WriteString(arrow + lines[0] + "\n")
			for _, l := range lines[1:] {
				b.WriteString("    " + l + "\n")
			}
		}
		b.WriteString("\n")
	}

	// ── Pane Content Preview ───────────────────────────────────────────────
	if len(snap.PaneContent) > 0 {
		b.WriteString(heading("Session Content Preview") + "\n")
		text, err := usecase.DecompressPaneContent(snap.PaneContent)
		if err != nil {
			b.WriteString(muted.Render("  (preview unavailable)") + "\n\n")
		} else {
			runes := []rune(text)
			total := len(runes)

			if total <= snapshotPreviewChars*2 {
				b.WriteString(muted.Render("── full ──") + "\n")
				b.WriteString(text + "\n\n")
			} else {
				head := string(runes[:snapshotPreviewChars])
				tail := string(runes[total-snapshotPreviewChars:])
				b.WriteString(muted.Render("── start ──") + "\n")
				b.WriteString(head + "\n")
				b.WriteString(muted.Render(fmt.Sprintf("── … %d chars omitted … ──", total-snapshotPreviewChars*2)) + "\n")
				b.WriteString(muted.Render("── end ──") + "\n")
				b.WriteString(tail + "\n\n")
			}
		}
	}

	return b.String()
}
