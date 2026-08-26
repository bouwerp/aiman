package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/jira"
)

type SetupModel struct {
	cfg        *config.Config
	inputs     []textinput.Model
	focusIndex int
	err        error
	saved      bool
}

// parseIssueStatuses splits the comma-separated statuses field into a clean list. Blank
// entries are dropped; an all-blank value yields nil so the provider falls back to
// jira.DefaultIssueStatuses.
func parseIssueStatuses(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

func NewSetupModel(cfg *config.Config) SetupModel {
	m := SetupModel{
		cfg:    cfg,
		inputs: make([]textinput.Model, 4),
	}

	var t textinput.Model
	for i := range m.inputs {
		t = textinput.New()
		applyCursorStyle(&t)
		t.CharLimit = 128

		switch i {
		case 0:
			t.Placeholder = "JIRA URL (e.g., https://company.atlassian.net)"
			t.SetValue(cfg.Integrations.Jira.URL)
			t.Focus()
		case 1:
			t.Placeholder = "JIRA Email"
			t.SetValue(cfg.Integrations.Jira.Email)
		case 2:
			t.Placeholder = "JIRA API Token"
			// Never cap a credential near its real length. Atlassian's current
			// tokens (ATATT…) run about 192 characters, so the shared 128 limit
			// truncated them silently — the field is masked, so there was nothing
			// to see — and the saved token then failed every request with 401.
			// The symptom was "no JIRA issues in the picker", which looks like a
			// JQL or status-filter problem and sends you looking in the wrong place.
			t.CharLimit = 4096
			t.SetValue(cfg.Integrations.Jira.APIToken)
			t.EchoMode = textinput.EchoPassword
			t.EchoCharacter = '•'
		case 3:
			t.Placeholder = "e.g. Dev Ready, In Development, Dev Review"
			t.CharLimit = 512
			t.SetWidth(56)
			t.SetValue(strings.Join(cfg.Integrations.Jira.IssueStatuses, ", "))
		}

		m.inputs[i] = t
	}

	return m
}

func (m SetupModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m SetupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			return m, nil

		case "tab", "shift+tab", "enter", "up", "down":
			s := msg.String()

			if s == "enter" && m.focusIndex == len(m.inputs) {
				return m.save()
			}

			if s == "up" || s == "shift+tab" {
				m.focusIndex--
			} else {
				m.focusIndex++
			}

			if m.focusIndex > len(m.inputs) {
				m.focusIndex = 0
			} else if m.focusIndex < 0 {
				m.focusIndex = len(m.inputs)
			}

			cmds := make([]tea.Cmd, len(m.inputs))
			for i := 0; i <= len(m.inputs)-1; i++ {
				if i == m.focusIndex {
					cmds[i] = m.inputs[i].Focus()
					continue
				}
				m.inputs[i].Blur()
			}

			return m, tea.Batch(cmds...)
		}
	}

	cmd := m.updateInputs(msg)
	return m, cmd
}

func (m *SetupModel) updateInputs(msg tea.Msg) tea.Cmd {
	cmds := make([]tea.Cmd, len(m.inputs))

	for i := range m.inputs {
		m.inputs[i], cmds[i] = m.inputs[i].Update(msg)
	}

	return tea.Batch(cmds...)
}

func (m SetupModel) save() (tea.Model, tea.Cmd) {
	m.cfg.Integrations.Jira.URL = m.inputs[0].Value()
	m.cfg.Integrations.Jira.Email = m.inputs[1].Value()
	m.cfg.Integrations.Jira.APIToken = m.inputs[2].Value()
	m.cfg.Integrations.Jira.IssueStatuses = parseIssueStatuses(m.inputs[3].Value())

	if err := m.cfg.Save(); err != nil {
		m.err = err
		return m, nil
	}

	m.saved = true
	return m, nil
}

func (m SetupModel) viewString() string {
	if m.saved {
		return "Configuration saved! Please restart Aiman.\n"
	}

	var b strings.Builder
	b.WriteString("Aiman Setup - JIRA Configuration\n\n")

	labels := []string{"URL", "Email", "API Token", "Issue statuses"}
	hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	for i := range m.inputs {
		b.WriteString(hintStyle.Render(labels[i]) + "\n")
		b.WriteString(m.inputs[i].View())
		b.WriteString("\n")
	}
	b.WriteString(hintStyle.Render("  Only issues assigned to you in these statuses are offered when starting a\n"+
		"  session. Comma-separated; leave empty to use the defaults:\n  "+
		strings.Join(jira.DefaultIssueStatuses, ", ")) + "\n")

	button := &strings.Builder{}
	fmt.Fprintf(button, "[ Save ]")
	if m.focusIndex == len(m.inputs) {
		b.WriteString("\n" + activeStyle.Render(button.String()) + "\n")
	} else {
		b.WriteString("\n" + button.String() + "\n")
	}

	b.WriteString("\n(esc to quit, tab to navigate)\n")

	return docStyle.Render(b.String())
}

func (m SetupModel) View() tea.View {
	v := tea.NewView(m.viewString())
	return v
}
