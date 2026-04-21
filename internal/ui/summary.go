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

func (m SummaryModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			return m, nil
		case "tab", "shift+tab", "up", "down":
			s := msg.String()
			if s == "up" || s == "shift+tab" {
				m.focusIndex--
			} else {
				m.focusIndex++
			}

			if m.focusIndex > 0 {
				m.focusIndex = 0
			} else if m.focusIndex < 0 {
				m.focusIndex = 0
			}

			return m, nil
		case "p":
			m.promptFree = !m.promptFree
			return m, nil
		case "enter":
			if m.agent != nil {
				m.confirmed = true
				return m, nil
			}
		}
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

	b.WriteString("\n")

	// Create button
	buttonLabel := "[ Create Session ]"
	switch {
	case m.agent == nil:
		buttonLabel = "[ Select Agent First ]"
		b.WriteString(failStyle.Render(buttonLabel) + "\n")
	case m.focusIndex == 0:
		b.WriteString(activeStyle.Render(buttonLabel) + "\n")
	default:
		b.WriteString(buttonLabel + "\n")
	}

	hint := "(enter to create, esc to go back"
	if !m.adHoc {
		hint += ", p to toggle prompt-free"
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
	return domain.SessionConfig{
		IssueKey:   m.issueKey,
		Branch:     m.branch,
		Repo:       m.repo,
		Directory:  m.directory,
		Agent:      m.agent,
		PromptFree: m.promptFree,
		AdHoc:      m.adHoc,
	}
}
