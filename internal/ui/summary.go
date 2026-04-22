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
	// Global secrets available for injection
	allSecrets      []domain.Secret
	selectedSecrets map[string]bool
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
	profileInput.Placeholder = "blank = AWS default credential chain"
	profileInput.SetValue(cfg.SourceProfile)
	profileInput.Width = 40

	regionInput := textinput.New()
	regionInput.Placeholder = "AWS region (e.g. us-east-2)"
	region := cfg.Region
	if region == "" {
		region = "us-east-2"
	}
	regionInput.SetValue(region)
	regionInput.Width = 40

	// Preserve any existing OpenRouter input that was added before AWS defaults.
	newInputs := []textinput.Model{profileInput, regionInput}
	for _, in := range m.inputs {
		if in.EchoMode == textinput.EchoPassword {
			newInputs = append(newInputs, in)
		}
	}
	m.inputs = newInputs
	// Default focus to the Create button so the user can just press Enter.
	m.focusIndex = m.buttonFocusIndex()
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
	m.inputs = append(filtered, orInput)
	// Default focus to the Create button so the user can just press Enter.
	m.focusIndex = m.buttonFocusIndex()
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

// awsProfileIdx returns the index of the AWS profile input in m.inputs.
func (m SummaryModel) awsProfileIdx() int { return 0 }

// awsRegionIdx returns the index of the AWS region input in m.inputs.
func (m SummaryModel) awsRegionIdx() int { return 1 }

// openRouterIdx returns the index of the OpenRouter key input in m.inputs.
func (m SummaryModel) openRouterIdx() int {
	if m.awsEnabled {
		return 2
	}
	return 0
}

// buttonFocusIndex returns the focusIndex value that corresponds to the Create button.
// It always equals len(m.inputs) since the button lives after all text inputs.
func (m SummaryModel) buttonFocusIndex() int {
	return len(m.inputs) + len(m.allSecrets)
}

// secretFocusStart returns the focusIndex of the first secret toggle row.
func (m SummaryModel) secretFocusStart() int {
	return len(m.inputs)
}

// SetSecrets loads the globally available secrets and pre-selects all of them.
func (m *SummaryModel) SetSecrets(secrets []domain.Secret) {
	m.allSecrets = secrets
	if m.selectedSecrets == nil {
		m.selectedSecrets = make(map[string]bool)
	}
	for _, s := range secrets {
		// Default: none selected; user opts in per session.
		if _, exists := m.selectedSecrets[s.Key]; !exists {
			m.selectedSecrets[s.Key] = false
		}
	}
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
		case "space", " ":
			// Toggle secret selection when focused on a secret row.
			if m.focusIndex >= m.secretFocusStart() && m.focusIndex < m.buttonFocusIndex() {
				idx := m.focusIndex - m.secretFocusStart()
				if idx < len(m.allSecrets) {
					key := m.allSecrets[idx].Key
					if m.selectedSecrets == nil {
						m.selectedSecrets = make(map[string]bool)
					}
					m.selectedSecrets[key] = !m.selectedSecrets[key]
				}
			}
			return m, nil
		case "enter":
			if m.focusIndex == m.buttonFocusIndex() && m.agent != nil {
				m.confirmed = true
				return m, nil
			}
			// Toggle secret on enter (same as space) when on a secret row.
			if m.focusIndex >= m.secretFocusStart() && m.focusIndex < m.buttonFocusIndex() {
				idx := m.focusIndex - m.secretFocusStart()
				if idx < len(m.allSecrets) {
					key := m.allSecrets[idx].Key
					if m.selectedSecrets == nil {
						m.selectedSecrets = make(map[string]bool)
					}
					m.selectedSecrets[key] = !m.selectedSecrets[key]
				}
				return m, nil
			}
			// Move focus forward on enter in text inputs
			if (m.awsEnabled || m.openRouterEnabled) && m.focusIndex < len(m.inputs) {
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
	if (m.awsEnabled || m.openRouterEnabled) && m.focusIndex < len(m.inputs) {
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
		if m.focusIndex == m.awsProfileIdx() {
			profileLabel = activeStyle.Render("> Profile:")
		}
		if m.focusIndex == m.awsRegionIdx() {
			regionLabel = activeStyle.Render("> Region: ")
		}
		b.WriteString(fmt.Sprintf("%-15s %s\n", profileLabel, m.inputs[m.awsProfileIdx()].View()))
		b.WriteString(fmt.Sprintf("%-15s %s\n", regionLabel, m.inputs[m.awsRegionIdx()].View()))
	}

	// OpenRouter API key
	if m.openRouterEnabled {
		b.WriteString("\n" + activeStyle.Render("OpenRouter") + "\n")
		keyLabel := "  API Key: "
		if m.focusIndex == m.openRouterIdx() {
			keyLabel = activeStyle.Render("> API Key: ")
		}
		b.WriteString(fmt.Sprintf("%-15s %s\n", keyLabel, m.inputs[m.openRouterIdx()].View()))
	}

	// Secrets multi-select
	if len(m.allSecrets) > 0 {
		b.WriteString("\n" + activeStyle.Render("Inject Secrets") + "\n")
		for i, s := range m.allSecrets {
			focusIdx := m.secretFocusStart() + i
			checked := "[ ]"
			if m.selectedSecrets[s.Key] {
				checked = "[✓]"
			}
			label := s.Key
			if s.Description != "" {
				label += " — " + s.Description
			}
			line := fmt.Sprintf("  %s %s", checked, label)
			if m.focusIndex == focusIdx {
				line = activeStyle.Render(fmt.Sprintf("  %s %s", checked, label))
			}
			b.WriteString(line + "\n")
		}
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
	if m.awsEnabled || m.openRouterEnabled {
		hint += ", tab to cycle fields"
	}
	if len(m.allSecrets) > 0 {
		hint += ", space to toggle secret"
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
		overrideProfile := strings.TrimSpace(m.inputs[m.awsProfileIdx()].Value())
		overrideRegion := strings.TrimSpace(m.inputs[m.awsRegionIdx()].Value())

		aws := *m.awsDefaults // copy remote defaults
		// Always apply the field values — an empty profile means "use the AWS default
		// credential chain" (omits --profile from CLI calls), allowing the user to clear
		// a remote-configured profile to fall back to the local default.
		aws.SourceProfile = overrideProfile
		if overrideRegion != "" {
			aws.Region = overrideRegion
		}
		cfg.AWSConfig = &aws
	}

	// OpenRouter API key
	if m.openRouterEnabled {
		cfg.OpenRouterAPIKey = strings.TrimSpace(m.inputs[m.openRouterIdx()].Value())
	}

	// Selected secrets
	for _, s := range m.allSecrets {
		if m.selectedSecrets[s.Key] {
			cfg.EnvSecrets = append(cfg.EnvSecrets, s)
		}
	}

	return cfg
}
