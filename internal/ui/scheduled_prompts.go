package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
	"github.com/robfig/cron/v3"
)

type spMode int

const (
	spModeList spMode = iota
	spModeEdit
	spModeSelectSessions
	spModeConfirmDelete
)

type spLoadedMsg struct{ prompts []domain.ScheduledPrompt }
type spSavedMsg struct{ prompts []domain.ScheduledPrompt }
type spDeletedMsg struct{ prompts []domain.ScheduledPrompt }

type ScheduledPromptsModel struct {
	repo             domain.SessionRepository
	mode             spMode
	prompts          []domain.ScheduledPrompt
	allSessions      []domain.Session
	cursor           int
	editingID        string
	inputs           []textinput.Model
	inputFocus       int
	sessionCursor    int
	selectedSessions map[string]bool
	err              error
}

func NewScheduledPromptsModel(repo domain.SessionRepository) ScheduledPromptsModel {
	return ScheduledPromptsModel{repo: repo}
}

func (m ScheduledPromptsModel) Init() tea.Cmd {
	return m.loadPrompts()
}

func (m ScheduledPromptsModel) loadPrompts() tea.Cmd {
	return func() tea.Msg {
		prompts, err := m.repo.ListScheduledPrompts(context.Background())
		if err != nil {
			return spLoadedMsg{prompts: nil}
		}
		return spLoadedMsg{prompts: prompts}
	}
}

func (m ScheduledPromptsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spLoadedMsg:
		m.prompts = msg.prompts
		if m.cursor >= len(m.prompts) && len(m.prompts) > 0 {
			m.cursor = len(m.prompts) - 1
		}
		return m, nil
	case spSavedMsg:
		m.prompts = msg.prompts
		m.mode = spModeList
		m.inputs = nil
		m.err = nil
		return m, nil
	case spDeletedMsg:
		m.prompts = msg.prompts
		m.mode = spModeList
		if m.cursor >= len(m.prompts) && len(m.prompts) > 0 {
			m.cursor = len(m.prompts) - 1
		}
		return m, nil
	case tea.KeyMsg:
		switch m.mode {
		case spModeList:
			return m.updateList(msg)
		case spModeEdit:
			return m.updateEdit(msg)
		case spModeSelectSessions:
			return m.updateSelectSessions(msg)
		case spModeConfirmDelete:
			return m.updateConfirmDelete(msg)
		}
	}
	if m.mode == spModeEdit && len(m.inputs) > 0 && m.inputFocus < len(m.inputs) {
		var cmd tea.Cmd
		m.inputs[m.inputFocus], cmd = m.inputs[m.inputFocus].Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m ScheduledPromptsModel) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		return m, nil // handled by parent
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.prompts)-1 {
			m.cursor++
		}
	case "a":
		m.mode = spModeEdit
		m.editingID = ""
		m.inputs = makeSPInputs("", "")
		m.inputFocus = 0
		m.inputs[0].Focus()
		m.selectedSessions = make(map[string]bool)
	case "e":
		if len(m.prompts) > 0 {
			p := m.prompts[m.cursor]
			m.mode = spModeEdit
			m.editingID = p.ID
			m.inputs = makeSPInputs(p.CronExpr, p.Prompt)
			m.inputFocus = 0
			m.inputs[0].Focus()
			m.selectedSessions = make(map[string]bool)
			for _, sid := range p.SessionIDs {
				m.selectedSessions[sid] = true
			}
		}
	case "d", "delete":
		if len(m.prompts) > 0 {
			m.mode = spModeConfirmDelete
		}
	}
	return m, nil
}

func (m ScheduledPromptsModel) updateEdit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.mode = spModeList
		m.inputs = nil
		m.err = nil
		return m, nil
	case "tab", "down":
		m.inputFocus = (m.inputFocus + 1) % (len(m.inputs) + 2) // +1 for sessions, +1 for save
		m.refocusInputs()
		return m, nil
	case "shift+tab", "up":
		m.inputFocus--
		if m.inputFocus < 0 {
			m.inputFocus = len(m.inputs) + 1
		}
		m.refocusInputs()
		return m, nil
	case "enter":
		if m.inputFocus == len(m.inputs) {
			// select sessions
			m.mode = spModeSelectSessions
			sessions, _ := m.repo.List(context.Background())
			m.allSessions = sessions
			m.sessionCursor = 0
			return m, nil
		}
		if m.inputFocus == len(m.inputs)+1 {
			return m.savePrompt()
		}
		m.inputFocus = (m.inputFocus + 1) % (len(m.inputs) + 2)
		m.refocusInputs()
		return m, nil
	}
	var cmd tea.Cmd
	if m.inputFocus < len(m.inputs) {
		m.inputs[m.inputFocus], cmd = m.inputs[m.inputFocus].Update(msg)
	}
	return m, cmd
}

func (m ScheduledPromptsModel) updateSelectSessions(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "enter":
		m.mode = spModeEdit
		return m, nil
	case "up", "k":
		if m.sessionCursor > 0 {
			m.sessionCursor--
		}
	case "down", "j":
		if m.sessionCursor < len(m.allSessions)-1 {
			m.sessionCursor++
		}
	case " ":
		if len(m.allSessions) > 0 {
			sid := m.allSessions[m.sessionCursor].ID
			m.selectedSessions[sid] = !m.selectedSessions[sid]
		}
	}
	return m, nil
}

func (m ScheduledPromptsModel) updateConfirmDelete(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "n", "N":
		m.mode = spModeList
	case "enter", "y", "Y":
		return m, m.deletePrompt(m.prompts[m.cursor].ID)
	}
	return m, nil
}

func (m *ScheduledPromptsModel) refocusInputs() {
	for i := range m.inputs {
		if i == m.inputFocus {
			m.inputs[i].Focus()
		} else {
			m.inputs[i].Blur()
		}
	}
}

func (m ScheduledPromptsModel) savePrompt() (tea.Model, tea.Cmd) {
	cronExpr := strings.TrimSpace(m.inputs[0].Value())
	promptTxt := strings.TrimSpace(m.inputs[1].Value())

	if cronExpr == "" || promptTxt == "" {
		m.err = fmt.Errorf("cron expr and prompt are required")
		return m, nil
	}

	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	if _, err := parser.Parse(cronExpr); err != nil {
		m.err = fmt.Errorf("invalid cron expression: %v", err)
		return m, nil
	}

	var sessionIDs []string
	for sid, selected := range m.selectedSessions {
		if selected {
			sessionIDs = append(sessionIDs, sid)
		}
	}
	if len(sessionIDs) == 0 {
		m.err = fmt.Errorf("at least one session must be selected")
		return m, nil
	}

	id := m.editingID
	if id == "" {
		id = uuid.NewString()
	}

	sp := domain.ScheduledPrompt{
		ID:         id,
		CronExpr:   cronExpr,
		Prompt:     promptTxt,
		SessionIDs: sessionIDs,
		CreatedAt:  time.Now(),
	}

	repo := m.repo
	return m, func() tea.Msg {
		if err := repo.SaveScheduledPrompt(context.Background(), &sp); err != nil {
			return spLoadedMsg{}
		}
		prompts, _ := repo.ListScheduledPrompts(context.Background())
		return spSavedMsg{prompts: prompts}
	}
}

func (m ScheduledPromptsModel) deletePrompt(id string) tea.Cmd {
	repo := m.repo
	return func() tea.Msg {
		_ = repo.DeleteScheduledPrompt(context.Background(), id)
		prompts, _ := repo.ListScheduledPrompts(context.Background())
		return spDeletedMsg{prompts: prompts}
	}
}

func (m ScheduledPromptsModel) View() string {
	switch m.mode {
	case spModeEdit:
		return m.viewEdit()
	case spModeSelectSessions:
		return m.viewSelectSessions()
	case spModeConfirmDelete:
		return m.viewConfirmDelete()
	default:
		return m.viewList()
	}
}

func (m ScheduledPromptsModel) viewList() string {
	var b strings.Builder
	b.WriteString(activeStyle.Render("Scheduled Prompts") + "\n")
	b.WriteString("Cron-scheduled prompts injected into sessions\n\n")

	if len(m.prompts) == 0 {
		b.WriteString("  (no scheduled prompts — press a to add one)\n")
	} else {
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
		for i, p := range m.prompts {
			prefix := "  "

			schedule, err := parser.Parse(p.CronExpr)
			nextStr := "Invalid"
			if err == nil {
				last := p.LastRunAt
				if last.IsZero() {
					last = p.CreatedAt
				}
				nextRun := schedule.Next(last)
				nextStr = nextRun.Format(time.RFC3339)
			}

			line := fmt.Sprintf("[%s] %q (%d sessions) Next: %s", p.CronExpr, p.Prompt, len(p.SessionIDs), nextStr)
			if i == m.cursor {
				prefix = "> "
				line = activeStyle.Render(line)
			}
			b.WriteString(prefix + line + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString("(a to add, e to edit, d to delete, esc to go back, ↑/↓ to navigate)\n")

	return docStyle.Render(b.String())
}

func (m ScheduledPromptsModel) viewEdit() string {
	var b strings.Builder
	b.WriteString(activeStyle.Render("Edit Scheduled Prompt") + "\n\n")

	labels := []string{"Cron Expression:", "Prompt Text:    "}
	for i, inp := range m.inputs {
		label := labels[i]
		if i == m.inputFocus {
			label = activeStyle.Render(label)
		}
		b.WriteString(fmt.Sprintf("%s %s\n", label, inp.View()))
	}

	b.WriteString("\n")
	sessionsLabel := fmt.Sprintf("[ Select Sessions (%d selected) ]", len(m.selectedSessions))
	if m.inputFocus == len(m.inputs) {
		sessionsLabel = activeStyle.Render(sessionsLabel)
	}
	b.WriteString(sessionsLabel + "\n")

	saveLabel := "[ Save ]"
	if m.inputFocus == len(m.inputs)+1 {
		saveLabel = activeStyle.Render(saveLabel)
	}
	b.WriteString(saveLabel + "\n")

	if m.err != nil {
		b.WriteString("\n" + failStyle.Render(fmt.Sprintf("Error: %v", m.err)) + "\n")
	}

	b.WriteString("\n(tab to navigate, enter to select/save, esc to cancel)\n")
	return docStyle.Render(b.String())
}

func (m ScheduledPromptsModel) viewSelectSessions() string {
	var b strings.Builder
	b.WriteString(activeStyle.Render("Select Sessions") + "\n\n")

	if len(m.allSessions) == 0 {
		b.WriteString("  (no active sessions available)\n")
	} else {
		for i, s := range m.allSessions {
			prefix := "  "
			if i == m.sessionCursor {
				prefix = "> "
			}
			check := "[ ]"
			if m.selectedSessions[s.ID] {
				check = "[x]"
			}
			line := fmt.Sprintf("%s %s (%s)", check, s.Branch, s.RepoName)
			if i == m.sessionCursor {
				line = activeStyle.Render(line)
			}
			b.WriteString(prefix + line + "\n")
		}
	}

	b.WriteString("\n(space to toggle, enter/esc to confirm)\n")
	return docStyle.Render(b.String())
}

func (m ScheduledPromptsModel) viewConfirmDelete() string {
	var b strings.Builder
	b.WriteString(failStyle.Render("Delete Scheduled Prompt") + "\n\n")
	b.WriteString("Delete this scheduled prompt? This cannot be undone.\n\n")
	b.WriteString("(y/enter to confirm, n/esc to cancel)\n")
	return docStyle.Render(b.String())
}

func makeSPInputs(cron, prompt string) []textinput.Model {
	inputs := make([]textinput.Model, 2)
	for i := range inputs {
		t := textinput.New()
		t.Cursor.Style = cursorStyle
		t.CharLimit = 256
		t.Width = 40
		if i == 0 {
			t.Placeholder = "*/5 * * * *"
			t.SetValue(cron)
		} else {
			t.Placeholder = "What is the status?"
			t.SetValue(prompt)
		}
		inputs[i] = t
	}
	return inputs
}
