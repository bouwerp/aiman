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
	issueKey   string
	branch     string
	repo       domain.Repo
	directory  string
	agent      *domain.Agent
	promptFree bool
	adHoc      bool
	confirmed  bool
	// dirField says the summary has a focusable Directory stop. Ad-hoc,
	// review, autonomous and new-repo summaries have none.
	dirField bool
	// wantsDir is set by enter on the Directory field; the dashboard opens
	// the directory picker and calls SetDirectory with the choice.
	wantsDir      bool
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
	// missingTools are the repo's required tools the remote lacks. They are
	// a warning only; enter still creates.
	missingTools []string
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
		dirField:    !repo.IsNew,
		promptFree:  true,
		promptInput: newPromptInput(),
	}

	return m
}

// DisableDirectory removes the Directory stop, for a summary whose working
// directory is fixed (a review session or an autonomous trigger).
func (m *SummaryModel) DisableDirectory() {
	m.dirField = false
	if m.focusIndex > m.buttonFocusIndex() {
		m.focusIndex = m.buttonFocusIndex()
	}
}

// WantsDirectory reports that enter on the Directory field asked for the
// directory picker.
func (m SummaryModel) WantsDirectory() bool {
	return m.wantsDir
}

// SetDirectory records the directory chosen in the picker (empty is the
// repository root) and clears the picker request.
func (m *SummaryModel) SetDirectory(dir string) {
	m.directory = dir
	m.wantsDir = false
}

// ClearDirectoryRequest drops a picker request the user abandoned.
func (m *SummaryModel) ClearDirectoryRequest() {
	m.wantsDir = false
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

// SetMissingTools records the repo's required tools the remote lacks.
func (m *SummaryModel) SetMissingTools(missing []string) {
	m.missingTools = missing
}

// dirFocusIndex is the Directory field's stop, right after the prompt (0).
const dirFocusIndex = 1

// buttonFocusIndex is the Create button's stop: 2 with a Directory field,
// else 1.
func (m SummaryModel) buttonFocusIndex() int {
	if m.dirField {
		return 2
	}
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
			if m.dirField && m.focusIndex == dirFocusIndex {
				m.wantsDir = true
				return m, nil
			}
			// Anywhere else, confirm immediately when an agent is selected.
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

		// Directory: a focusable field when the user may choose one.
		dir := m.directory
		if m.dirField {
			if dir == "" || dir == "." {
				root := "repo root"
				if m.repo.Name == "" || m.repo.Name == "No Repository" {
					root = "remote root"
				}
				dir = muted.Render(root)
			}
			label := fmt.Sprintf("%-15s", "Directory:")
			if m.focusIndex == dirFocusIndex {
				label = activeStyle.Render(label)
			}
			dir += muted.Render("  (tab here, enter to change)")
			b.WriteString(label + " " + dir + "\n")
		} else {
			if dir == "" {
				dir = "."
			}
			b.WriteString(fmt.Sprintf("%-15s %s\n", "Directory:", dir))
		}

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
		agentLabel := m.agent.Name
		if m.agent.LaunchModel != "" {
			agentLabel += " (" + m.agent.LaunchModel + ")"
		}
		b.WriteString(fmt.Sprintf("%-15s %s\n", "Agent:", successStyle.Render(agentLabel)))
	} else {
		b.WriteString(fmt.Sprintf("%-15s %s\n", "Agent:", failStyle.Render("None selected")))
	}

	if len(m.missingTools) > 0 {
		warn := lipgloss.NewStyle().Foreground(a1Wait)
		b.WriteString(warn.Render("⚠ Missing on remote: "+strings.Join(m.missingTools, ", ")+" (create anyway with enter)") + "\n")
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
