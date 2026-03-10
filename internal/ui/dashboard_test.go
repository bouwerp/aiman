package ui

import (
	"context"
	"errors"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/usecase"
	tea "github.com/charmbracelet/bubbletea"
)

// mockSessionRepo implements domain.SessionRepository for testing
type mockSessionRepo struct {
	sessions  []domain.Session
	saveErr   error
	getErr    error
	listErr   error
	deleteErr error
}

func (m *mockSessionRepo) Save(ctx context.Context, s *domain.Session) error {
	return m.saveErr
}

func (m *mockSessionRepo) Get(ctx context.Context, id string) (*domain.Session, error) {
	return nil, m.getErr
}

func (m *mockSessionRepo) List(ctx context.Context) ([]domain.Session, error) {
	return m.sessions, m.listErr
}

func (m *mockSessionRepo) Delete(ctx context.Context, id string) error {
	return m.deleteErr
}

func (m *mockSessionRepo) Close() error {
	return nil
}

// TestWorktreeExistsErrorHandling tests that WORKTREE_EXISTS error transitions to the correct state
func TestWorktreeExistsErrorHandling(t *testing.T) {
	cfg := &config.Config{}
	db := &mockSessionRepo{}

	model := NewModel(cfg, []usecase.CheckResult{}, []domain.Session{}, db, nil)
	model.state = viewStateLoading // Set initial state

	// Simulate receiving a sessionCreateMsg with WORKTREE_EXISTS error
	msg := sessionCreateMsg{
		err: errors.New("WORKTREE_EXISTS"),
	}

	updatedModel, _ := model.Update(msg)
	m := updatedModel.(*Model)

	// Verify state transition
	if m.state != viewStateWorktreeExists {
		t.Errorf("Expected state to be viewStateWorktreeExists, got %v", m.state)
	}
}

// TestWorktreeExistsStateKeyHandling tests keyboard input in viewStateWorktreeExists
func TestWorktreeExistsStateKeyHandling(t *testing.T) {
	tests := []struct {
		name          string
		key           string
		expectedState viewState
		setupBranch   string
	}{
		{
			name:          "Cancel with 'c' returns to main",
			key:           "c",
			expectedState: viewStateMain,
			setupBranch:   "feature/test-branch",
		},
		{
			name:          "Cancel with 'esc' returns to main",
			key:           "esc",
			expectedState: viewStateMain,
			setupBranch:   "feature/test-branch",
		},
		{
			name:          "Change branch with 'b' goes to branch input",
			key:           "b",
			expectedState: viewStateBranchInput,
			setupBranch:   "feature/test-branch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{}
			db := &mockSessionRepo{}

			model := NewModel(cfg, []usecase.CheckResult{}, []domain.Session{}, db, nil)
			model.state = viewStateWorktreeExists
			model.sessionCfg.Branch = tt.setupBranch

			// Simulate key press
			keyMsg := tea.KeyMsg{Type: tea.KeyRunes}
			// Manually set the key string (in real code this comes from bubbletea)
			switch tt.key {
			case "c":
				keyMsg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}}
			case "b":
				keyMsg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}}
			case "esc":
				keyMsg = tea.KeyMsg{Type: tea.KeyEsc}
			}

			updatedModel, _ := model.Update(keyMsg)
			m := updatedModel.(*Model)

			if m.state != tt.expectedState {
				t.Errorf("Expected state to be %v, got %v", tt.expectedState, m.state)
			}

			// If changing branch, verify branchInput is initialized
			if tt.key == "b" && m.branchInput.textInput.Value() == "" {
				t.Error("Expected branchInput to be initialized when changing branch")
			}
		})
	}
}

// TestWorktreeExistsOtherKeysIgnored tests that unhandled keys don't change state
func TestWorktreeExistsOtherKeysIgnored(t *testing.T) {
	cfg := &config.Config{}
	db := &mockSessionRepo{}

	model := NewModel(cfg, []usecase.CheckResult{}, []domain.Session{}, db, nil)
	model.state = viewStateWorktreeExists
	model.sessionCfg.Branch = "feature/test"

	// Simulate pressing an unhandled key
	keyMsg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}

	updatedModel, _ := model.Update(keyMsg)
	m := updatedModel.(*Model)

	// State should remain unchanged
	if m.state != viewStateWorktreeExists {
		t.Errorf("Expected state to remain viewStateWorktreeExists, got %v", m.state)
	}
}

// TestNonWorktreeExistsErrorHandling tests that other errors don't transition to viewStateWorktreeExists
func TestNonWorktreeExistsErrorHandling(t *testing.T) {
	cfg := &config.Config{}
	db := &mockSessionRepo{}

	model := NewModel(cfg, []usecase.CheckResult{}, []domain.Session{}, db, nil)
	model.state = viewStateLoading

	// Simulate receiving a sessionCreateMsg with a different error
	msg := sessionCreateMsg{
		err: errors.New("some other error"),
	}

	updatedModel, _ := model.Update(msg)
	m := updatedModel.(*Model)

	// Should transition to error state, not worktree exists state
	if m.state == viewStateWorktreeExists {
		t.Error("Non-WORKTREE_EXISTS error should not transition to viewStateWorktreeExists")
	}

	// Verify error message is set
	if m.lastError == "" {
		t.Error("Expected lastError to be set for non-WORKTREE_EXISTS error")
	}
}
