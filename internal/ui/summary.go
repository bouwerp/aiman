package ui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/bouwerp/aiman/internal/domain"
)

type SummaryModel struct {
	issueKey      string
	branch        string
	repo          domain.Repo
	directory     string
	agent         *domain.Agent
	backend       string // session terminal runtime; empty means the tmux default
	promptFree    bool
	adHoc         bool
	confirmed     bool
	focusIndex    int
	width, height int
	// AWS override fields — populated when the remote has SyncCredentials enabled
	awsEnabled  bool
	awsDefaults *domain.AWSConfig // original remote defaults (non-editable fields)
	// promptInput is a free-text initial prompt sent to the agent. Focused by default.
	promptInput textinput.Model
	// workspace fields
	workspacePath   string
	workspaceExists bool
}

func newPromptInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "Initial prompt (optional)"
	ti.SetWidth(40)
	ti.Focus()
	return ti
}

func NewSummaryModel(issueKey, branch string, repo domain.Repo, directory string) SummaryModel {
	m := SummaryModel{
		issueKey:    issueKey,
		branch:      branch,
		repo:        repo,
		directory:   directory,
		promptFree:  true,
		promptInput: newPromptInput(),
	}

	return m
}

func NewAdHocSummaryModel(label string) SummaryModel {
	return SummaryModel{
		branch:      label,
		adHoc:       true,
		promptFree:  true,
		promptInput: newPromptInput(),
	}
}

// SetAWSDefaults records remote AWS defaults for the session without showing
// per-session profile/region fields. Which local profiles are used is chosen
// in Menu → AWS Credentials.
func (m *SummaryModel) SetAWSDefaults(cfg *domain.AWSConfig) {
	if cfg == nil {
		return
	}
	m.awsDefaults = cfg
	m.awsEnabled = false
}

func (m SummaryModel) Init() tea.Cmd {
	return nil
}

// SetBackend records which terminal runtime will host the session.
func (m *SummaryModel) SetBackend(backend string) {
	m.backend = backend
}

func (m *SummaryModel) SetAgent(agent *domain.Agent) {
	m.agent = agent
}

func (m *SummaryModel) SetSize(width, height int) {
	m.width = width
	m.height = height
}

func (m *SummaryModel) SetWorkspaceStatus(path string, exists bool) {
	m.workspacePath = path
	m.workspaceExists = exists
}

// buttonFocusIndex is 1: focus 0 is the initial-prompt input, 1 is Create.
func (m SummaryModel) buttonFocusIndex() int {
	return 1
}

func (m SummaryModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			return m, nil
		case "tab", "shift+tab", "up", "down":
			s := msg.String()
			max := m.buttonFocusIndex()
			if s == "up" || s == "shift+tab" {
				m.focusIndex--
			} else {
				m.focusIndex++
			}
			if m.focusIndex > max {
				m.focusIndex = 0
			} else if m.focusIndex < 0 {
				m.focusIndex = max
			}
			if m.focusIndex == 0 {
				m.promptInput.Focus()
			} else {
				m.promptInput.Blur()
			}
			return m, nil
		case "p":
			if m.focusIndex == m.buttonFocusIndex() {
				m.promptFree = !m.promptFree
			} else {
				break // let the key fall through to the focused text input
			}
			return m, nil
		case "enter":
			// Always confirm immediately when an agent is selected.
			if m.agent != nil {
				m.confirmed = true
				return m, nil
			}
		}
	}

	// Delegate key events to the focused text input.
	if m.focusIndex == 0 {
		var cmd tea.Cmd
		m.promptInput, cmd = m.promptInput.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m SummaryModel) viewString() string {
	var b strings.Builder
	muted := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

	if m.adHoc {
		b.WriteString(activeStyle.Render("Ad-hoc Session") + "\n\n")
		label := m.branch
		if label == "" {
			label = muted.Render("(auto-generated)")
		}
		b.WriteString(fmt.Sprintf("%-15s %s\n", "Label:", label))
	} else {
		b.WriteString(activeStyle.Render("Session Summary") + "\n\n")

		// Issue
		if m.issueKey != "" {
			b.WriteString(fmt.Sprintf("%-15s %s\n", "Issue:", successStyle.Render(m.issueKey)))
		} else {
			b.WriteString(fmt.Sprintf("%-15s %s\n", "Issue:", failStyle.Render("None")))
		}

		// Branch
		if m.branch != "" {
			b.WriteString(fmt.Sprintf("%-15s %s\n", "Branch:", m.branch))
		} else {
			b.WriteString(fmt.Sprintf("%-15s %s\n", "Branch:", failStyle.Render("None")))
		}

		// Repository
		if m.repo.Name != "" {
			b.WriteString(fmt.Sprintf("%-15s %s\n", "Repository:", m.repo.Name))
		} else {
			b.WriteString(fmt.Sprintf("%-15s %s\n", "Repository:", failStyle.Render("None")))
		}

		// Directory
		dir := m.directory
		if dir == "" {
			dir = "."
		}
		b.WriteString(fmt.Sprintf("%-15s %s\n", "Directory:", dir))

		// Workspace Status
		if m.workspacePath != "" {
			status := "(Will be cloned)"
			if m.workspaceExists {
				status = successStyle.Render("(Already cloned)")
			}
			b.WriteString(fmt.Sprintf("%-15s %s %s\n", "Workspace:", m.workspacePath, status))
		}
	}

	// Agent
	if m.agent != nil {
		b.WriteString(fmt.Sprintf("%-15s %s\n", "Agent:", successStyle.Render(m.agent.Name)))
	} else {
		b.WriteString(fmt.Sprintf("%-15s %s\n", "Agent:", failStyle.Render("None selected")))
	}

	// Backend. Shown always, not just for pty: the run-target toggle silently
	// had no effect for a while, and nothing on the confirmation screen would
	// have revealed it.
	backend := m.backend
	if backend == "" {
		backend = domain.BackendTmux
	}
	b.WriteString(fmt.Sprintf("%-15s %s\n", "Backend:", backend))

	if !m.adHoc {
		// Prompt Free
		pfStatus := "Disabled"
		if m.promptFree {
			pfStatus = successStyle.Render("Enabled")
		}
		b.WriteString(fmt.Sprintf("%-15s %s\n", "Prompt Free:", pfStatus))
	}

	// Initial prompt
	b.WriteString("\n" + activeStyle.Render("Initial Prompt") + "\n")
	promptLabel := "  Prompt: "
	if m.focusIndex == 0 {
		promptLabel = activeStyle.Render("> Prompt: ")
	}
	b.WriteString(fmt.Sprintf("%-15s %s\n", promptLabel, m.promptInput.View()))

	b.WriteString("\n")

	// Create button
	buttonLabel := "[ Create Session ]"
	switch {
	case m.agent == nil:
		buttonLabel = "[ Select Agent First ]"
		b.WriteString(failStyle.Render(buttonLabel) + "\n")
	case m.focusIndex == m.buttonFocusIndex():
		b.WriteString(activeStyle.Render(buttonLabel) + "\n")
	default:
		b.WriteString(buttonLabel + "\n")
	}

	hint := "(enter to create, esc to go back, tab to cycle fields"
	if !m.adHoc {
		hint += ", p on Create to toggle prompt-free"
	}
	hint += ")"
	b.WriteString("\n" + hint + "\n")

	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		Padding(2, 4).
		Width(70)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
		docStyle.Render(style.Render(b.String())))
}

func (m SummaryModel) IsConfirmed() bool {
	return m.confirmed
}

func (m SummaryModel) GetAgent() *domain.Agent {
	return m.agent
}

func (m SummaryModel) GetSessionConfig() domain.SessionConfig {
	cfg := domain.SessionConfig{
		IssueKey:   m.issueKey,
		Branch:     m.branch,
		Repo:       m.repo,
		Directory:  m.directory,
		Agent:      m.agent,
		PromptFree: m.promptFree,
		AdHoc:      m.adHoc,
	}

	if m.awsDefaults != nil {
		aws := *m.awsDefaults
		cfg.AWSConfig = &aws
	}

	// Initial prompt text entered by the user.
	cfg.InitialPrompt = strings.TrimSpace(m.promptInput.Value())

	return cfg
}

func (m SummaryModel) View() tea.View {
	return newView(m.viewString())
}
