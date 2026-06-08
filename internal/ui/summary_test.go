package ui

import (
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
