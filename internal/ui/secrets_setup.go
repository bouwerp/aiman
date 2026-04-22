package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

type secretsSetupMode int

const (
	secretsModeList secretsSetupMode = iota
	secretsModeAdd
	secretsModeConfirmDelete
)

type secretsLoadedMsg struct{ secrets []domain.Secret }
type secretSavedMsg struct{ secrets []domain.Secret }
type secretDeletedMsg struct{ secrets []domain.Secret }

// SecretsSetupModel manages globally stored env-var secrets.
type SecretsSetupModel struct {
	repo       domain.SessionRepository
	mode       secretsSetupMode
	secrets    []domain.Secret
	cursor     int
	inputs     []textinput.Model // [key, value, description] for add mode
	inputFocus int
	err        error
}

func NewSecretsSetupModel(repo domain.SessionRepository) SecretsSetupModel {
	return SecretsSetupModel{repo: repo}
}

func (m SecretsSetupModel) Init() tea.Cmd {
	return m.loadSecrets()
}

func (m SecretsSetupModel) loadSecrets() tea.Cmd {
	return func() tea.Msg {
		secrets, err := m.repo.ListSecrets(context.Background())
		if err != nil {
			return secretsLoadedMsg{secrets: nil}
		}
		return secretsLoadedMsg{secrets: secrets}
	}
}

func (m SecretsSetupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case secretsLoadedMsg:
		m.secrets = msg.secrets
		if m.cursor >= len(m.secrets) && len(m.secrets) > 0 {
			m.cursor = len(m.secrets) - 1
		}
		return m, nil
	case secretSavedMsg:
		m.secrets = msg.secrets
		m.mode = secretsModeList
		m.inputs = nil
		m.err = nil
		return m, nil
	case secretDeletedMsg:
		m.secrets = msg.secrets
		m.mode = secretsModeList
		if m.cursor >= len(m.secrets) && len(m.secrets) > 0 {
			m.cursor = len(m.secrets) - 1
		}
		return m, nil
	case tea.KeyMsg:
		switch m.mode {
		case secretsModeList:
			return m.updateList(msg)
		case secretsModeAdd:
			return m.updateAdd(msg)
		case secretsModeConfirmDelete:
			return m.updateConfirmDelete(msg)
		}
	}
	if m.mode == secretsModeAdd && len(m.inputs) > 0 && m.inputFocus < len(m.inputs) {
		var cmd tea.Cmd
		m.inputs[m.inputFocus], cmd = m.inputs[m.inputFocus].Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m SecretsSetupModel) updateList(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		return m, nil
	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "j":
		if m.cursor < len(m.secrets)-1 {
			m.cursor++
		}
	case "a":
		m.mode = secretsModeAdd
		m.inputs = makeSecretInputs()
		m.inputFocus = 0
		m.inputs[0].Focus()
	case "d", "delete":
		if len(m.secrets) > 0 {
			m.mode = secretsModeConfirmDelete
		}
	}
	return m, nil
}

func (m SecretsSetupModel) updateAdd(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc":
		m.mode = secretsModeList
		m.inputs = nil
		m.err = nil
		return m, nil
	case "tab", "down":
		m.inputFocus = (m.inputFocus + 1) % (len(m.inputs) + 1) // +1 for save button
		m.refocusInputs()
		return m, nil
	case "shift+tab", "up":
		m.inputFocus--
		if m.inputFocus < 0 {
			m.inputFocus = len(m.inputs)
		}
		m.refocusInputs()
		return m, nil
	case "enter":
		if m.inputFocus == len(m.inputs) {
			return m.saveSecret()
		}
		m.inputFocus = (m.inputFocus + 1) % (len(m.inputs) + 1)
		m.refocusInputs()
		return m, nil
	}
	var cmd tea.Cmd
	if m.inputFocus < len(m.inputs) {
		m.inputs[m.inputFocus], cmd = m.inputs[m.inputFocus].Update(msg)
	}
	return m, cmd
}

func (m SecretsSetupModel) updateConfirmDelete(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "esc", "n", "N":
		m.mode = secretsModeList
	case "enter", "y", "Y":
		return m, m.deleteSecret(m.secrets[m.cursor].Key)
	}
	return m, nil
}

func (m *SecretsSetupModel) refocusInputs() {
	for i := range m.inputs {
		if i == m.inputFocus {
			m.inputs[i].Focus()
		} else {
			m.inputs[i].Blur()
		}
	}
}

func (m SecretsSetupModel) saveSecret() (tea.Model, tea.Cmd) {
	key := strings.TrimSpace(m.inputs[0].Value())
	value := strings.TrimSpace(m.inputs[1].Value())
	desc := strings.TrimSpace(m.inputs[2].Value())

	if key == "" {
		m.err = fmt.Errorf("key is required")
		return m, nil
	}
	if !isValidEnvKey(key) {
		m.err = fmt.Errorf("key must be a valid env var name (letters, digits, underscores)")
		return m, nil
	}
	if value == "" {
		m.err = fmt.Errorf("value is required")
		return m, nil
	}

	secret := domain.Secret{Key: key, Value: value, Description: desc}
	repo := m.repo
	return m, func() tea.Msg {
		if err := repo.SaveSecret(context.Background(), secret); err != nil {
			return secretsLoadedMsg{}
		}
		secrets, _ := repo.ListSecrets(context.Background())
		return secretSavedMsg{secrets: secrets}
	}
}

func (m SecretsSetupModel) deleteSecret(key string) tea.Cmd {
	repo := m.repo
	return func() tea.Msg {
		_ = repo.DeleteSecret(context.Background(), key)
		secrets, _ := repo.ListSecrets(context.Background())
		return secretDeletedMsg{secrets: secrets}
	}
}

func (m SecretsSetupModel) View() string {
	switch m.mode {
	case secretsModeAdd:
		return m.viewAdd()
	case secretsModeConfirmDelete:
		return m.viewConfirmDelete()
	default:
		return m.viewList()
	}
}

func (m SecretsSetupModel) viewList() string {
	var b strings.Builder
	b.WriteString(activeStyle.Render("Global Secrets") + "\n")
	b.WriteString("Env-var secrets injected into new sessions\n\n")

	if len(m.secrets) == 0 {
		b.WriteString("  (no secrets — press a to add one)\n")
	} else {
		for i, s := range m.secrets {
			prefix := "  "
			line := fmt.Sprintf("%-24s %s", s.Key, maskDescription(s.Description))
			if i == m.cursor {
				prefix = "> "
				line = activeStyle.Render(line)
			}
			b.WriteString(prefix + line + "\n")
		}
	}

	b.WriteString("\n")
	b.WriteString("(a to add, d to delete selected, esc to go back, ↑/↓ to navigate)\n")

	return docStyle.Render(b.String())
}

func (m SecretsSetupModel) viewAdd() string {
	var b strings.Builder
	b.WriteString(activeStyle.Render("Add Secret") + "\n\n")

	labels := []string{"Key (env var name):", "Value:             ", "Description:       "}
	for i, inp := range m.inputs {
		label := labels[i]
		if i == m.inputFocus {
			label = activeStyle.Render(label)
		}
		b.WriteString(fmt.Sprintf("%s %s\n", label, inp.View()))
	}

	b.WriteString("\n")
	saveLabel := "[ Save ]"
	if m.inputFocus == len(m.inputs) {
		saveLabel = activeStyle.Render(saveLabel)
	}
	b.WriteString(saveLabel + "\n")

	if m.err != nil {
		b.WriteString("\n" + failStyle.Render(fmt.Sprintf("Error: %v", m.err)) + "\n")
	}

	b.WriteString("\n(tab to navigate, enter to save, esc to cancel)\n")
	return docStyle.Render(b.String())
}

func (m SecretsSetupModel) viewConfirmDelete() string {
	key := ""
	if m.cursor < len(m.secrets) {
		key = m.secrets[m.cursor].Key
	}
	var b strings.Builder
	b.WriteString(failStyle.Render("Delete Secret") + "\n\n")
	b.WriteString(fmt.Sprintf("Delete secret %q? This cannot be undone.\n\n", key))
	b.WriteString("(y/enter to confirm, n/esc to cancel)\n")
	return docStyle.Render(b.String())
}

// GetSecrets returns the currently loaded secrets (used to seed the summary screen).
func (m SecretsSetupModel) GetSecrets() []domain.Secret {
	return m.secrets
}

func makeSecretInputs() []textinput.Model {
	inputs := make([]textinput.Model, 3)
	for i := range inputs {
		t := textinput.New()
		t.Cursor.Style = cursorStyle
		t.CharLimit = 256
		switch i {
		case 0:
			t.Placeholder = "MY_API_KEY"
			t.Width = 40
		case 1:
			t.Placeholder = "secret value"
			t.EchoMode = textinput.EchoPassword
			t.EchoCharacter = '•'
			t.Width = 40
		case 2:
			t.Placeholder = "optional description"
			t.Width = 40
		}
		inputs[i] = t
	}
	return inputs
}

// isValidEnvKey returns true if s is a valid POSIX env var name.
func isValidEnvKey(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i, c := range s {
		if c >= 'A' && c <= 'Z' {
			continue
		}
		if c >= 'a' && c <= 'z' {
			continue
		}
		if c == '_' {
			continue
		}
		if i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

func maskDescription(desc string) string {
	if desc == "" {
		return ""
	}
	return "— " + desc
}
