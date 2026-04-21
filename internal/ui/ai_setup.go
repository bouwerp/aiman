package ui

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// Message types for async AI setup operations.
type aiCheckResultMsg struct {
	ollamaInstalled bool
	modelPresent    bool
}
type aiInstallDoneMsg struct{ err error }
type aiPullStartedMsg struct{ scanner *bufio.Scanner }
type aiPullProgressMsg string
type aiPullDoneMsg struct{ err error }

const (
	aiSetupFocusEnabled = 0
	aiSetupFocusHost    = 1
	aiSetupFocusModel   = 2
	aiSetupFocusCheck   = 3
	aiSetupFocusInstall = 4
	aiSetupFocusPull    = 5
	aiSetupFocusSave    = 6
	aiSetupFocusMax     = 6
)

// AISetupModel is the settings screen for configuring the local AI backend.
type AISetupModel struct {
	cfg             *config.Config
	enabled         bool
	hostInput       textinput.Model
	modelInput      textinput.Model
	focusIndex      int
	saved           bool
	err             error
	checkDone       bool
	ollamaInstalled bool
	modelPresent    bool
	installing      bool
	pulling         bool
	progressLines   []string
	pullScanner     *bufio.Scanner
}

func NewAISetupModel(cfg *config.Config) AISetupModel {
	host := textinput.New()
	host.Cursor.Style = cursorStyle
	host.CharLimit = 256
	host.Placeholder = "http://localhost:11434"
	host.SetValue(cfg.AI.OllamaHost)

	model := textinput.New()
	model.Cursor.Style = cursorStyle
	model.CharLimit = 128
	model.Placeholder = "qwen3:4b"
	model.SetValue(cfg.AI.Model)

	return AISetupModel{
		cfg:        cfg,
		enabled:    cfg.AI.Enabled,
		hostInput:  host,
		modelInput: model,
	}
}

func (m AISetupModel) Init() tea.Cmd {
	return aiCheckCmd(m.modelInput.Value())
}

func (m AISetupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case aiCheckResultMsg:
		m.checkDone = true
		m.ollamaInstalled = msg.ollamaInstalled
		m.modelPresent = msg.modelPresent
		return m, nil

	case aiInstallDoneMsg:
		m.installing = false
		if msg.err != nil {
			m.progressLines = append(m.progressLines, failStyle.Render("✗ "+msg.err.Error()))
		} else {
			m.progressLines = append(m.progressLines, successStyle.Render("✓ Ollama installed successfully"))
			m.ollamaInstalled = true
		}
		return m, nil

	case aiPullStartedMsg:
		m.pullScanner = msg.scanner
		return m, waitForPullLine(msg.scanner)

	case aiPullProgressMsg:
		line := string(msg)
		if line != "" {
			if len(m.progressLines) > 0 &&
				strings.Contains(m.progressLines[len(m.progressLines)-1], "pulling") &&
				strings.Contains(line, "pulling") {
				// Overwrite rolling progress line for the same chunk
				m.progressLines[len(m.progressLines)-1] = line
			} else {
				m.progressLines = append(m.progressLines, line)
			}
			if len(m.progressLines) > 15 {
				m.progressLines = m.progressLines[len(m.progressLines)-15:]
			}
		}
		return m, waitForPullLine(m.pullScanner)

	case aiPullDoneMsg:
		m.pulling = false
		m.pullScanner = nil
		if msg.err != nil {
			m.progressLines = append(m.progressLines, failStyle.Render("✗ "+msg.err.Error()))
		} else {
			m.progressLines = append(m.progressLines, successStyle.Render("✓ Model ready"))
			m.modelPresent = true
		}
		return m, nil
	}

	if km, ok := msg.(tea.KeyMsg); ok {
		switch km.String() {
		case "ctrl+c":
			return m, tea.Quit
		case "esc":
			return m, nil
		case "up", "shift+tab":
			m.focusIndex--
			if m.focusIndex < 0 {
				m.focusIndex = aiSetupFocusMax
			}
			return m, m.syncFocus()
		case "down", "tab":
			m.focusIndex++
			if m.focusIndex > aiSetupFocusMax {
				m.focusIndex = 0
			}
			return m, m.syncFocus()
		case "enter", " ":
			switch m.focusIndex {
			case aiSetupFocusEnabled:
				m.enabled = !m.enabled
				return m, nil
			case aiSetupFocusCheck:
				m.progressLines = nil
				m.checkDone = false
				return m, aiCheckCmd(m.modelInput.Value())
			case aiSetupFocusInstall:
				if !m.ollamaInstalled && !m.installing {
					m.installing = true
					m.progressLines = []string{statusStyle.Render("Running: brew install ollama…")}
					return m, installOllamaCmd()
				}
			case aiSetupFocusPull:
				if !m.pulling {
					modelName := m.modelInput.Value()
					if modelName == "" {
						modelName = "qwen3:4b"
					}
					m.pulling = true
					m.progressLines = []string{statusStyle.Render("Starting: ollama pull " + modelName + "…")}
					return m, startPullCmd(modelName)
				}
			case aiSetupFocusSave:
				return m.save()
			}
		}
	}

	var cmd tea.Cmd
	switch m.focusIndex {
	case aiSetupFocusHost:
		m.hostInput, cmd = m.hostInput.Update(msg)
	case aiSetupFocusModel:
		m.modelInput, cmd = m.modelInput.Update(msg)
	}
	return m, cmd
}

func (m *AISetupModel) syncFocus() tea.Cmd {
	m.hostInput.Blur()
	m.modelInput.Blur()
	switch m.focusIndex {
	case aiSetupFocusHost:
		return m.hostInput.Focus()
	case aiSetupFocusModel:
		return m.modelInput.Focus()
	}
	return nil
}

func (m AISetupModel) save() (tea.Model, tea.Cmd) {
	m.cfg.AI.Enabled = m.enabled
	m.cfg.AI.OllamaHost = m.hostInput.Value()
	m.cfg.AI.Model = m.modelInput.Value()
	if err := m.cfg.Save(); err != nil {
		m.err = err
		return m, nil
	}
	m.saved = true
	return m, nil
}

func (m AISetupModel) View() string {
	if m.saved {
		return "AI settings saved! Press 'i' on any session to try it now.\n"
	}

	var b strings.Builder
	b.WriteString("AI Settings\n")
	b.WriteString("Configure the local AI backend (Ollama)\n\n")

	// Enabled toggle (focus 0)
	check := "[ ]"
	if m.enabled {
		check = "[✓]"
	}
	label := fmt.Sprintf("%s Enable local AI (requires Ollama running)", check)
	if m.focusIndex == aiSetupFocusEnabled {
		label = activeStyle.Render(label)
	}
	b.WriteString(label + "\n\n")

	// Host input (focus 1)
	hostLabel := "Ollama host"
	if m.focusIndex == aiSetupFocusHost {
		hostLabel = activeStyle.Render(hostLabel)
	}
	b.WriteString(hostLabel + "\n" + m.hostInput.View() + "\n\n")

	// Model input (focus 2)
	modelLabel := "Model name"
	if m.focusIndex == aiSetupFocusModel {
		modelLabel = activeStyle.Render(modelLabel)
	}
	b.WriteString(modelLabel + "\n" + m.modelInput.View() + "\n\n")

	// Status section
	b.WriteString(statusStyle.Render("─── Status ───") + "\n")
	if !m.checkDone {
		b.WriteString(statusStyle.Render("  Checking…") + "\n\n")
	} else {
		ollamaStatus := failStyle.Render("✗ not installed")
		if m.ollamaInstalled {
			ollamaStatus = successStyle.Render("✓ installed")
		}
		modelName := m.modelInput.Value()
		if modelName == "" {
			modelName = "qwen3:4b"
		}
		modelStatus := failStyle.Render("✗ not pulled")
		if m.modelPresent {
			modelStatus = successStyle.Render("✓ ready")
		}
		b.WriteString(fmt.Sprintf("  Ollama: %s    Model (%s): %s\n\n", ollamaStatus, modelName, modelStatus))
	}

	// Action buttons (focus 3, 4, 5)
	checkBtn := "[ ↻ Check Status ]"
	if m.focusIndex == aiSetupFocusCheck {
		checkBtn = activeStyle.Render(checkBtn)
	} else {
		checkBtn = statusStyle.Render(checkBtn)
	}

	var installBtn string
	switch {
	case m.installing:
		installBtn = statusStyle.Render("[ Installing… ]")
	case m.ollamaInstalled:
		installBtn = statusStyle.Render("[ ✓ Ollama Installed ]")
	case m.focusIndex == aiSetupFocusInstall:
		installBtn = activeStyle.Render("[ ⬇ Install Ollama ]")
	default:
		installBtn = statusStyle.Render("[ ⬇ Install Ollama ]")
	}

	var pullBtn string
	switch {
	case m.pulling:
		pullBtn = statusStyle.Render("[ Pulling… ]")
	case m.modelPresent:
		pullBtn = statusStyle.Render("[ ✓ Model Ready ]")
	case m.focusIndex == aiSetupFocusPull:
		pullBtn = activeStyle.Render("[ ⬇ Pull Model ]")
	default:
		pullBtn = statusStyle.Render("[ ⬇ Pull Model ]")
	}

	b.WriteString(checkBtn + "  " + installBtn + "  " + pullBtn + "\n\n")

	// Progress log
	if len(m.progressLines) > 0 {
		b.WriteString(statusStyle.Render("─── Progress ───") + "\n")
		for _, line := range m.progressLines {
			b.WriteString("  " + line + "\n")
		}
		b.WriteString("\n")
	}

	// Save button (focus 6)
	saveLabel := "[ Save ]"
	if m.focusIndex == aiSetupFocusSave {
		saveLabel = activeStyle.Render(saveLabel)
	}
	b.WriteString(saveLabel + "\n")

	if m.err != nil {
		b.WriteString("\n" + failStyle.Render(fmt.Sprintf("Error: %v", m.err)) + "\n")
	}

	b.WriteString("\n(tab/shift+tab to navigate, enter/space to toggle/activate, esc to cancel)\n")

	return docStyle.Render(b.String())
}

// aiCheckCmd checks whether ollama is installed and the configured model is present.
func aiCheckCmd(modelName string) tea.Cmd {
	return func() tea.Msg {
		_, err := exec.LookPath("ollama")
		installed := err == nil

		modelPresent := false
		if installed && modelName != "" {
			out, err := exec.Command("ollama", "list").Output()
			if err == nil {
				prefix := strings.SplitN(modelName, ":", 2)[0]
				for _, line := range strings.Split(string(out), "\n") {
					if strings.HasPrefix(strings.TrimSpace(line), prefix) {
						modelPresent = true
						break
					}
				}
			}
		}
		return aiCheckResultMsg{ollamaInstalled: installed, modelPresent: modelPresent}
	}
}

// installOllamaCmd installs Ollama via Homebrew.
func installOllamaCmd() tea.Cmd {
	return func() tea.Msg {
		brewPath, err := exec.LookPath("brew")
		if err != nil {
			return aiInstallDoneMsg{err: fmt.Errorf("Homebrew not found — download Ollama from https://ollama.com/download/mac")}
		}
		out, err := exec.Command(brewPath, "install", "ollama").CombinedOutput()
		if err != nil {
			return aiInstallDoneMsg{err: fmt.Errorf("%s", strings.TrimSpace(string(out)))}
		}
		return aiInstallDoneMsg{}
	}
}

// startPullCmd begins streaming `ollama pull <model>` output.
// It returns aiPullStartedMsg with a scanner; the Update loop then
// calls waitForPullLine to read one line at a time.
func startPullCmd(modelName string) tea.Cmd {
	return func() tea.Msg {
		cmd := exec.Command("ollama", "pull", modelName)
		pr, pw := io.Pipe()
		cmd.Stdout = pw
		cmd.Stderr = pw
		if err := cmd.Start(); err != nil {
			_ = pw.Close()
			return aiPullDoneMsg{err: err}
		}
		go func() {
			_ = cmd.Wait()
			_ = pw.Close()
		}()
		scanner := bufio.NewScanner(pr)
		scanner.Split(scanCR)
		return aiPullStartedMsg{scanner: scanner}
	}
}

// waitForPullLine reads the next line from the scanner in a goroutine,
// returning aiPullProgressMsg or aiPullDoneMsg when the stream ends.
func waitForPullLine(scanner *bufio.Scanner) tea.Cmd {
	return func() tea.Msg {
		if scanner == nil {
			return aiPullDoneMsg{}
		}
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				return aiPullProgressMsg(line)
			}
		}
		return aiPullDoneMsg{err: scanner.Err()}
	}
}

// scanCR splits on \r or \n so ollama's carriage-return progress lines
// are each captured as individual tokens.
func scanCR(data []byte, atEOF bool) (advance int, token []byte, err error) {
	for i, b := range data {
		if b == '\r' || b == '\n' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}
