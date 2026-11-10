package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/usecase"
)

// New-session wizard: picking a repository goes straight to the agent step;
// the working directory is an optional field on the summary, the repository
// root unless the user picks one there.

func wizardModel(t *testing.T) *Model {
	t.Helper()
	cfg := &config.Config{Remotes: []config.Remote{{Host: "devbox", User: "code"}}}
	m := NewModel(cfg, []usecase.CheckResult{}, []domain.Session{}, &mockSessionRepo{}, nil, nil, nil)
	m.selectedRemote = cfg.Remotes[0]
	m.sessionCfg = domain.SessionConfig{IssueKey: "PB-1", Branch: "feature/pb-1"}
	return m
}

func TestWizardRepoPickSkipsDirectoryStep(t *testing.T) {
	m := wizardModel(t)
	m.state = viewStateRepoPicker
	m.picker = NewRepoPickerModel([]domain.Repo{{Name: "org/repo"}}, &m.cfg.Git)
	got, _ := m.handleRepoPickerUpdate(pressKey("enter"))
	m = got.(*Model)
	if m.sessionCfg.Repo.Name != "org/repo" {
		t.Fatalf("repo = %q, want org/repo picked", m.sessionCfg.Repo.Name)
	}
	if m.loadingNext == viewStateDirPicker || m.state == viewStateDirPicker {
		t.Fatal("picking a repo still opens the directory step")
	}
	if m.loadingNext != viewStateAgentPicker {
		t.Fatalf("loadingNext = %v, want the agent picker", m.loadingNext)
	}
	if m.sessionCfg.Directory != "" && m.sessionCfg.Directory != "." {
		t.Fatalf("directory = %q, want the repo root by default", m.sessionCfg.Directory)
	}
}

func TestWizardAgentEscReturnsToRepoPicker(t *testing.T) {
	m := wizardModel(t)
	m.sessionCfg.Repo = domain.Repo{Name: "org/repo"}
	m.state = viewStateAgentPicker
	m.agentPicker = NewAgentPickerModel([]domain.Agent{{Name: "Claude", Command: "claude"}})
	got, _ := m.handleAgentPickerUpdate(pressKey("esc"))
	m = got.(*Model)
	if m.state != viewStateRepoPicker {
		t.Fatalf("esc from the agent list = %v, want the repo picker", m.state)
	}
}

func TestSummaryDirectoryFieldRequestsPicker(t *testing.T) {
	s := NewSummaryModel("PB-1", "feature/pb-1", domain.Repo{Name: "org/repo"}, "")
	s.SetAgent(&domain.Agent{Name: "Claude"})
	got, _ := s.Update(pressKey("tab")) // prompt -> directory
	s = got.(SummaryModel)
	got, _ = s.Update(pressKey("enter"))
	s = got.(SummaryModel)
	if s.IsConfirmed() {
		t.Fatal("enter on the directory field created the session")
	}
	if !s.WantsDirectory() {
		t.Fatal("enter on the directory field did not ask for the directory picker")
	}
}

func TestSummaryDefaultCreateKeepsRepoRoot(t *testing.T) {
	s := NewSummaryModel("PB-1", "feature/pb-1", domain.Repo{Name: "org/repo"}, "")
	s.SetAgent(&domain.Agent{Name: "Claude"})
	got, _ := s.Update(pressKey("enter")) // enter in the prompt creates
	s = got.(SummaryModel)
	if !s.IsConfirmed() {
		t.Fatal("enter in the prompt no longer creates the session")
	}
	if d := s.GetSessionConfig().Directory; d != "" && d != "." {
		t.Fatalf("directory = %q, want the repo root", d)
	}
}

func TestSummaryDirectoryChosenInPicker(t *testing.T) {
	m := wizardModel(t)
	m.sessionCfg.Repo = domain.Repo{Name: "org/repo"}
	m.sessionCfg.Agent = &domain.Agent{Name: "Claude"}
	m.summary = NewSummaryModel("PB-1", "feature/pb-1", m.sessionCfg.Repo, "")
	m.summary.SetAgent(m.sessionCfg.Agent)
	m.state = viewStateSummary

	got, _ := m.handleSummaryUpdate(pressKey("tab"))
	m = got.(*Model)
	got, _ = m.handleSummaryUpdate(pressKey("enter"))
	m = got.(*Model)
	if m.state != viewStateLoading || m.loadingNext != viewStateDirPicker {
		t.Fatalf("state=%v next=%v, want loading the directory picker", m.state, m.loadingNext)
	}

	m.state = viewStateDirPicker
	m.dirPicker = NewDirPickerModel([]string{"svc/api"}, m.sessionCfg.Repo)
	got, _ = m.handleDirPickerUpdate(pressKey("enter"))
	m = got.(*Model)
	if m.state != viewStateSummary {
		t.Fatalf("state after picking = %v, want back on the summary", m.state)
	}
	if d := m.summary.GetSessionConfig().Directory; d != "svc/api" {
		t.Fatalf("summary directory = %q, want svc/api", d)
	}

	// esc from the picker returns to the summary and keeps the choice.
	m.state = viewStateDirPicker
	m.dirPicker = NewDirPickerModel([]string{"other"}, m.sessionCfg.Repo)
	got, _ = m.handleDirPickerUpdate(pressKey("esc"))
	m = got.(*Model)
	if m.state != viewStateSummary {
		t.Fatalf("esc from the picker = %v, want the summary", m.state)
	}
	if d := m.summary.GetSessionConfig().Directory; d != "svc/api" {
		t.Fatalf("esc changed the directory to %q", d)
	}
}
