package ui

import (
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type SummaryModel struct {
	issueKey      string
	branch        string
	repo          domain.Repo
	directory     string
	agent         *domain.Agent
	promptFree    bool
	adHoc         bool
	confirmed     bool
	focusIndex    int
	inputs        []textinput.Model
	width, height int
	// AWS override fields — populated when the remote has SyncCredentials enabled
	awsEnabled  bool
	awsDefaults *domain.AWSConfig // original remote defaults (non-editable fields)
	// OpenRouter key field
	openRouterEnabled bool
	// promptInput is a free-text initial prompt sent to the agent. Focused by default.
	promptInput textinput.Model
	// workspace fields
	workspacePath   string
	workspaceExists bool
}

func newPromptInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "Initial prompt (optional)"
	ti.Width = 40
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
		inputs:      make([]textinput.Model, 0),
		promptInput: newPromptInput(),
	}

	return m
}

func NewAdHocSummaryModel(label string) SummaryModel {
	return SummaryModel{
		branch:      label,
		adHoc:       true,
		promptFree:  true,
		inputs:      make([]textinput.Model, 0),
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

// SetOpenRouterKey enables the OpenRouter API key section, pre-filling it with
// the provided key (typically from the local OPENROUTER_API_KEY env var).
// If key is empty the field is still shown so the user can enter one manually.
func (m *SummaryModel) SetOpenRouterKey(key string) {
	m.openRouterEnabled = true

	orInput := textinput.New()
	orInput.Placeholder = "sk-or-... (OPENROUTER_API_KEY)"
	orInput.EchoMode = textinput.EchoPassword
	orInput.SetValue(key)
	orInput.Width = 40

	// Remove any stale openRouter input, then append the fresh one.
	filtered := make([]textinput.Model, 0, len(m.inputs))
	for _, in := range m.inputs {
		if in.EchoMode != textinput.EchoPassword {
			filtered = append(filtered, in)
		}
	}
	m.inputs = append(filtered, orInput) //nolint:gocritic // filtered is a fresh slice built above, not an alias of m.inputs
}

func (m SummaryModel) Init() tea.Cmd {
	return nil
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

func (m SummaryModel) openRouterIdx() int { return 0 }

// Focus index 0 is always the initial-prompt input. The AWS/OpenRouter inputs in
// m.inputs occupy focus indices 1..len(m.inputs); the Create button is last.

// inputFocusIndex maps an index into m.inputs to its focusIndex value.
func (m SummaryModel) inputFocusIndex(i int) int { return i + 1 }

// buttonFocusIndex returns the focusIndex value that corresponds to the Create button.
func (m SummaryModel) buttonFocusIndex() int {
	return len(m.inputs) + 1
}

func (m SummaryModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
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
			// Update focus states (focus index 0 = prompt input; 1.. = m.inputs).
			if m.focusIndex == 0 {
				m.promptInput.Focus()
			} else {
				m.promptInput.Blur()
			}
			for i := range m.inputs {
				if m.inputFocusIndex(i) == m.focusIndex {
					m.inputs[i].Focus()
				} else {
					m.inputs[i].Blur()
				}
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
	if (m.awsEnabled || m.openRouterEnabled) && m.focusIndex >= 1 && m.focusIndex <= len(m.inputs) {
		idx := m.focusIndex - 1
		var cmd tea.Cmd
		m.inputs[idx], cmd = m.inputs[idx].Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m SummaryModel) View() string {
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

	// OpenRouter API key
	if m.openRouterEnabled {
		b.WriteString("\n" + activeStyle.Render("OpenRouter") + "\n")
		keyLabel := "  API Key: "
		if m.focusIndex == m.inputFocusIndex(m.openRouterIdx()) {
			keyLabel = activeStyle.Render("> API Key: ")
		}
		b.WriteString(fmt.Sprintf("%-15s %s\n", keyLabel, m.inputs[m.openRouterIdx()].View()))
	}

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

	// OpenRouter API key
	if m.openRouterEnabled {
		cfg.OpenRouterAPIKey = strings.TrimSpace(m.inputs[m.openRouterIdx()].Value())
	}

	// Initial prompt text entered by the user.
	cfg.InitialPrompt = strings.TrimSpace(m.promptInput.Value())

	return cfg
}
