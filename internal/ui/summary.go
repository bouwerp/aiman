package ui

import (
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	awsProfileInputIdx = 0
	awsRegionInputIdx  = 1
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
	awsEnabled    bool
	awsDefaults   *domain.AWSConfig // original remote defaults (non-editable fields)
}

func NewSummaryModel(issueKey, branch string, repo domain.Repo, directory string) SummaryModel {
	m := SummaryModel{
		issueKey:   issueKey,
		branch:     branch,
		repo:       repo,
		directory:  directory,
		promptFree: true,
		inputs:     make([]textinput.Model, 0),
	}

	return m
}

func NewAdHocSummaryModel(label string) SummaryModel {
	return SummaryModel{
		branch:     label,
		adHoc:      true,
		promptFree: true,
		inputs:     make([]textinput.Model, 0),
	}
}

// SetAWSDefaults enables the AWS override section, pre-filling inputs with
// the remote's configured defaults. The user can edit profile and region
// before confirming.
func (m *SummaryModel) SetAWSDefaults(cfg *domain.AWSConfig) {
	if cfg == nil {
		return
	}
	m.awsEnabled = true
	m.awsDefaults = cfg

	profileInput := textinput.New()
	profileInput.Placeholder = "local AWS profile (e.g. default)"
	profileInput.SetValue(cfg.SourceProfile)
	profileInput.Width = 40
	profileInput.Focus()

	regionInput := textinput.New()
	regionInput.Placeholder = "AWS region (e.g. us-east-1)"
	regionInput.SetValue(cfg.Region)
	regionInput.Width = 40

	m.inputs = []textinput.Model{profileInput, regionInput}
	m.focusIndex = awsProfileInputIdx
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

// buttonFocusIndex returns the focusIndex value that corresponds to the Create button.
func (m SummaryModel) buttonFocusIndex() int {
	if m.awsEnabled {
		return len(m.inputs) // after all text inputs
	}
	return 0
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
			// Update input focus states
			for i := range m.inputs {
				if i == m.focusIndex {
					m.inputs[i].Focus()
				} else {
					m.inputs[i].Blur()
				}
			}
			return m, nil
		case "p":
			if !m.awsEnabled || m.focusIndex == m.buttonFocusIndex() {
				m.promptFree = !m.promptFree
			}
			return m, nil
		case "enter":
			if m.focusIndex == m.buttonFocusIndex() && m.agent != nil {
				m.confirmed = true
				return m, nil
			}
			// Move focus forward on enter in text inputs
			if m.awsEnabled && m.focusIndex < m.buttonFocusIndex() {
				m.focusIndex++
				for i := range m.inputs {
					if i == m.focusIndex {
						m.inputs[i].Focus()
					} else {
						m.inputs[i].Blur()
					}
				}
				return m, nil
			}
		}
	}

	// Delegate key events to the focused text input
	if m.awsEnabled && m.focusIndex < len(m.inputs) {
		var cmd tea.Cmd
		m.inputs[m.focusIndex], cmd = m.inputs[m.focusIndex].Update(msg)
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

	// AWS credential overrides
	if m.awsEnabled {
		b.WriteString("\n" + activeStyle.Render("AWS Credentials") + "\n")
		profileLabel := "  Profile:"
		regionLabel := "  Region: "
		if m.focusIndex == awsProfileInputIdx {
			profileLabel = activeStyle.Render("> Profile:")
		}
		if m.focusIndex == awsRegionInputIdx {
			regionLabel = activeStyle.Render("> Region: ")
		}
		b.WriteString(fmt.Sprintf("%-15s %s\n", profileLabel, m.inputs[awsProfileInputIdx].View()))
		b.WriteString(fmt.Sprintf("%-15s %s\n", regionLabel, m.inputs[awsRegionInputIdx].View()))
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

	hint := "(enter to create, esc to go back"
	if !m.adHoc {
		hint += ", p to toggle prompt-free"
	}
	if m.awsEnabled {
		hint += ", tab to cycle fields"
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

	// Merge per-session AWS overrides on top of the remote defaults.
	if m.awsEnabled && m.awsDefaults != nil {
		overrideProfile := strings.TrimSpace(m.inputs[awsProfileInputIdx].Value())
		overrideRegion := strings.TrimSpace(m.inputs[awsRegionInputIdx].Value())

		aws := *m.awsDefaults // copy remote defaults
		if overrideProfile != "" {
			aws.SourceProfile = overrideProfile
		}
		if overrideRegion != "" {
			aws.Region = overrideRegion
		}
		cfg.AWSConfig = &aws
	}

	return cfg
}
