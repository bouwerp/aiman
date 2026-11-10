package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/bouwerp/aiman/internal/domain"
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
	updated, _ := m.Update(pressKey("hi there"))
	m = updated.(SummaryModel)
	if got := m.promptInput.Value(); got != "hi there" {
		t.Fatalf("expected prompt %q, got %q", "hi there", got)
	}
}

func TestSummaryModelGetSessionConfigReturnsPromptNoSecrets(t *testing.T) {
	m := NewSummaryModel("ABC-1", "feature/x", domain.Repo{Name: "repo"}, "")
	m.SetAgent(&domain.Agent{Name: "Claude"})
	updated, _ := m.Update(pressKey("do the thing"))
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
	updated, _ := m.Update(pressKey("enter"))
	m = updated.(SummaryModel)
	if !m.IsConfirmed() {
		t.Fatal("expected confirmed after Enter with agent set")
	}
}

func TestSummaryShowsLaunchModel(t *testing.T) {
	m := NewSummaryModel("ABC-1", "feature/x", domain.Repo{Name: "repo"}, "")
	m.SetAgent(&domain.Agent{Name: "Pi", LaunchModel: "openrouter/qwen/qwen3-coder"})
	out := ansi.Strip(m.viewString())
	if !strings.Contains(out, "openrouter/qwen/qwen3-coder") {
		t.Fatalf("summary missing launch model:\n%s", out)
	}
}

func TestSummaryModelAdHocGetSessionConfigReturnsPrompt(t *testing.T) {
	m := NewAdHocSummaryModel("my-label")
	m.SetAgent(&domain.Agent{Name: "Claude"})
	updated, _ := m.Update(pressKey("adhoc prompt"))
	m = updated.(SummaryModel)
	cfg := m.GetSessionConfig()
	if !cfg.AdHoc {
		t.Fatal("expected ad-hoc config")
	}
	if cfg.InitialPrompt != "adhoc prompt" {
		t.Fatalf("expected InitialPrompt %q, got %q", "adhoc prompt", cfg.InitialPrompt)
	}
}

func TestSummaryModelHasNoAWSProfileFields(t *testing.T) {
	m := NewSummaryModel("ABC-1", "feature/x", domain.Repo{Name: "repo"}, "")
	m.SetAWSDefaults(&domain.AWSConfig{SourceProfile: "prod", Region: "eu-west-1"})
	if m.awsEnabled {
		t.Fatal("session summary must not show AWS profile fields")
	}
	m.SetAgent(&domain.Agent{Name: "Claude"})
	cfg := m.GetSessionConfig()
	if cfg.AWSConfig == nil || cfg.AWSConfig.Region != "eu-west-1" {
		t.Fatalf("expected remote AWS defaults, got %+v", cfg.AWSConfig)
	}
}

func TestSummaryModelHasNoOpenRouterField(t *testing.T) {
	m := NewSummaryModel("ABC-1", "feature/x", domain.Repo{Name: "repo"}, "")
	view := m.viewString()
	if strings.Contains(view, "OpenRouter") {
		t.Fatal("session summary must not show an OpenRouter API key field")
	}
	if strings.Contains(view, "API Key") {
		t.Fatal("session summary must not show an API key field")
	}
	updated, _ := m.Update(pressKey("tab"))
	m = updated.(SummaryModel)
	if m.focusIndex != dirFocusIndex {
		t.Fatalf("tab from prompt should land on Directory, got %d", m.focusIndex)
	}
	updated, _ = m.Update(pressKey("tab"))
	m = updated.(SummaryModel)
	if m.focusIndex != m.buttonFocusIndex() {
		t.Fatalf("tab from Directory should land on Create, got %d", m.focusIndex)
	}
}

func TestSummaryNoDirectoryStop(t *testing.T) {
	cases := map[string]SummaryModel{
		"ad-hoc":   NewAdHocSummaryModel("my-label"),
		"new repo": NewSummaryModel("ABC-1", "feature/x", domain.Repo{Name: "o/r", IsNew: true}, "."),
	}
	review := NewSummaryModel("", "", domain.Repo{Name: "o/r"}, "")
	review.DisableDirectory()
	cases["review"] = review
	for name, m := range cases {
		m.SetAgent(&domain.Agent{Name: "Claude"})
		updated, _ := m.Update(pressKey("tab"))
		m = updated.(SummaryModel)
		if m.focusIndex != m.buttonFocusIndex() || m.buttonFocusIndex() != 1 {
			t.Errorf("%s: tab from prompt = %d, want Create (1)", name, m.focusIndex)
		}
		updated, _ = m.Update(pressKey("enter"))
		m = updated.(SummaryModel)
		if m.WantsDirectory() || !m.IsConfirmed() {
			t.Errorf("%s: enter on Create wants=%v confirmed=%v", name, m.WantsDirectory(), m.IsConfirmed())
		}
	}
}

func TestSummaryDirectoryShowsChoiceOrRoot(t *testing.T) {
	m := NewSummaryModel("ABC-1", "feature/x", domain.Repo{Name: "o/r"}, "")
	if got := ansi.Strip(m.viewString()); !strings.Contains(got, "repo root") {
		t.Fatalf("no repo-root default in:\n%s", got)
	}
	m.SetDirectory("svc/api")
	if got := ansi.Strip(m.viewString()); !strings.Contains(got, "svc/api") {
		t.Fatalf("chosen directory missing in:\n%s", got)
	}
	if d := m.GetSessionConfig().Directory; d != "svc/api" {
		t.Fatalf("directory = %q", d)
	}
}

func TestSummaryShowsMissingTools(t *testing.T) {
	m := NewSummaryModel("ABC-1", "feature/x", domain.Repo{Name: "o/r"}, "")
	m.SetAgent(&domain.Agent{Name: "Claude"})
	m.SetMissingTools([]string{"gh", "mcp:atlassian"})
	// The summary box is 70 columns wide, so the warning wraps.
	got := ansi.Strip(m.viewString())
	if !strings.Contains(got, "⚠ Missing on remote: gh, mcp:atlassian") || !strings.Contains(got, "(create anyway with") {
		t.Fatalf("no missing-tools warning in:\n%s", got)
	}
	updated, _ := m.Update(pressKey("enter"))
	if !updated.(SummaryModel).IsConfirmed() {
		t.Fatal("enter must still create when tools are missing")
	}
}

func TestSummaryNoWarningWhenNothingMissing(t *testing.T) {
	m := NewSummaryModel("ABC-1", "feature/x", domain.Repo{Name: "o/r"}, "")
	m.SetAgent(&domain.Agent{Name: "Claude"})
	m.SetMissingTools(nil)
	if got := m.viewString(); strings.Contains(got, "Missing on remote") {
		t.Fatalf("unexpected warning in:\n%s", got)
	}
}

func TestWizardBranchPickSkipsDirectoryStep(t *testing.T) {
	m := wizardModel(t)
	m.sessionCfg.ExistingBranch = true
	m.sessionCfg.Repo = domain.Repo{Name: "org/repo"}
	m.sessionCfg.Directory = "stale"
	m.state = viewStateBranchPicker
	m.branchPicker = NewBranchPickerModel([]string{"feature/x"})
	got, _ := m.handleBranchPickerUpdate(pressKey("enter"))
	m = got.(*Model)
	if m.loadingNext != viewStateAgentPicker || m.sessionCfg.Directory != "" {
		t.Fatalf("next=%v dir=%q, want agent picker at the repo root", m.loadingNext, m.sessionCfg.Directory)
	}
	m.state = viewStateAgentPicker
	m.agentPicker = NewAgentPickerModel([]domain.Agent{{Name: "Claude", Command: "claude"}})
	got, _ = m.handleAgentPickerUpdate(pressKey("esc"))
	if s := got.(*Model).state; s != viewStateBranchPicker {
		t.Fatalf("esc from agents = %v, want the branch picker", s)
	}
}

func TestSummaryDirectoryRoundTripKeepsPrompt(t *testing.T) {
	m := wizardModel(t)
	m.sessionCfg.Repo = domain.Repo{Name: "No Repository"}
	m.summary = NewSummaryModel("PB-1", "feature/pb-1", m.sessionCfg.Repo, "")
	m.summary.SetAgent(&domain.Agent{Name: "Claude"})
	m.state = viewStateSummary
	got, _ := m.handleSummaryUpdate(pressKey("fix it"))
	m = got.(*Model)
	got, _ = m.handleSummaryUpdate(pressKey("tab"))
	m = got.(*Model)
	got, _ = m.handleSummaryUpdate(pressKey("enter"))
	m = got.(*Model)
	if m.loadingNext != viewStateDirPicker {
		t.Fatalf("next = %v, want the directory picker", m.loadingNext)
	}
	m.state = viewStateDirPicker
	m.dirPicker = NewDirPickerModel([]string{"scratch"}, m.sessionCfg.Repo)
	got, _ = m.handleDirPickerUpdate(pressKey("enter"))
	m = got.(*Model)
	cfg := m.summary.GetSessionConfig()
	if cfg.InitialPrompt != "fix it" || cfg.Directory != "scratch" {
		t.Fatalf("after round trip prompt=%q dir=%q", cfg.InitialPrompt, cfg.Directory)
	}
	// Focus is still on Directory; shift+tab back to the prompt and create.
	got, _ = m.handleSummaryUpdate(pressKey("shift+tab"))
	m = got.(*Model)
	got, cmd := m.handleSummaryUpdate(pressKey("enter"))
	m = got.(*Model)
	if cmd == nil || m.sessionCfg.Directory != "scratch" {
		t.Fatalf("create did not carry the directory: %q", m.sessionCfg.Directory)
	}
}
