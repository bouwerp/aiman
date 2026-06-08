package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	tea "github.com/charmbracelet/bubbletea"
)

func TestSummaryModelPromptFocusedByDefault(t *testing.T) {
	m := NewSummaryModel("ABC-1", "feature/x", domain.Repo{Name: "repo"}, "")
	if m.focusIndex != 0 {
		t.Fatalf("expected focusIndex 0 (prompt), got %d", m.focusIndex)
	}
	if !m.promptInput.Focused() {
		t.Fatal("expected prompt input to be focused by default")
	}
}

func TestSummaryModelAdHocPromptFocusedByDefault(t *testing.T) {
	m := NewAdHocSummaryModel("my-label")
	if m.focusIndex != 0 || !m.promptInput.Focused() {
		t.Fatal("expected ad-hoc prompt input focused by default")
	}
}

func TestSummaryModelTypingPopulatesPrompt(t *testing.T) {
	m := NewSummaryModel("ABC-1", "feature/x", domain.Repo{Name: "repo"}, "")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hi there")})
	m = updated.(SummaryModel)
	if got := m.promptInput.Value(); got != "hi there" {
		t.Fatalf("expected prompt %q, got %q", "hi there", got)
	}
}

func TestSummaryModelGetSessionConfigReturnsPromptNoSecrets(t *testing.T) {
	m := NewSummaryModel("ABC-1", "feature/x", domain.Repo{Name: "repo"}, "")
	m.SetAgent(&domain.Agent{Name: "Claude"})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("do the thing")})
	m = updated.(SummaryModel)
	cfg := m.GetSessionConfig()
	if cfg.InitialPrompt != "do the thing" {
		t.Fatalf("expected InitialPrompt %q, got %q", "do the thing", cfg.InitialPrompt)
	}
	if len(cfg.EnvSecrets) != 0 {
		t.Fatalf("expected no EnvSecrets, got %d", len(cfg.EnvSecrets))
	}
}

func TestSummaryModelEnterConfirmsWithAgent(t *testing.T) {
	m := NewSummaryModel("ABC-1", "feature/x", domain.Repo{Name: "repo"}, "")
	m.SetAgent(&domain.Agent{Name: "Claude"})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(SummaryModel)
	if !m.IsConfirmed() {
		t.Fatal("expected confirmed after Enter with agent set")
	}
}

func TestSummaryModelAdHocGetSessionConfigReturnsPrompt(t *testing.T) {
	m := NewAdHocSummaryModel("my-label")
	m.SetAgent(&domain.Agent{Name: "Claude"})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("adhoc prompt")})
	m = updated.(SummaryModel)
	cfg := m.GetSessionConfig()
	if !cfg.AdHoc {
		t.Fatal("expected ad-hoc config")
	}
	if cfg.InitialPrompt != "adhoc prompt" {
		t.Fatalf("expected InitialPrompt %q, got %q", "adhoc prompt", cfg.InitialPrompt)
	}
}

// With AWS inputs present, the prompt occupies focus index 0 and the AWS inputs
// shift to 1..N. Tabbing off the prompt must route typing to the AWS input, not
// the prompt — this guards the focus-index offset arithmetic.
func TestSummaryModelTabRoutesTypingToAWSInput(t *testing.T) {
	m := NewSummaryModel("ABC-1", "feature/x", domain.Repo{Name: "repo"}, "")
	m.SetAWSDefaults(&domain.AWSConfig{SourceProfile: "p", Region: "us-east-2"})
	if m.focusIndex != 0 {
		t.Fatalf("expected prompt focused (0) after SetAWSDefaults, got %d", m.focusIndex)
	}

	// Tab moves focus to the first AWS input (focus index 1).
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(SummaryModel)
	if m.focusIndex != 1 {
		t.Fatalf("expected focus index 1 (AWS profile) after tab, got %d", m.focusIndex)
	}

	// Typing now goes to the AWS profile input (m.inputs[0]), not the prompt.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("X")})
	m = updated.(SummaryModel)
	if m.promptInput.Value() != "" {
		t.Fatalf("prompt should stay empty; got %q", m.promptInput.Value())
	}
	if !strings.Contains(m.inputs[0].Value(), "X") {
		t.Fatalf("expected AWS profile input to receive the keystroke, got %q", m.inputs[0].Value())
	}
}
