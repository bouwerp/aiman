package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/review"
	"github.com/charmbracelet/x/ansi"
)

func TestModePickerOffersReview(t *testing.T) {
	m := &Model{cfg: &config.Config{}, state: viewStateModePicker, width: 80, height: 24}
	out := ansi.Strip(m.renderModePicker())
	if !strings.Contains(out, "[6]") || !strings.Contains(out, "Review a PR") {
		t.Fatalf("mode picker missing Review a PR:\n%s", out)
	}

	next, cmd := m.handleModePickerUpdate(pressKey("6"))
	got := next.(*Model)
	if got.state != viewStateLoading || got.loadingNext != viewStateRepoPicker {
		t.Fatalf("state=%v next=%v, want loading then repo picker", got.state, got.loadingNext)
	}
	if !got.reviewMode {
		t.Fatal("reviewMode not set")
	}
	if cmd == nil {
		t.Fatal("want a fetch-repos command")
	}
}

func TestRepoPickThenPRPicker(t *testing.T) {
	m := &Model{
		state:      viewStateRepoPicker,
		reviewMode: true,
		width:      80,
		height:     24,
		picker:     NewRepoPickerModel([]domain.Repo{{Name: "o/r"}}, nil),
	}
	m.picker.SetSize(80, 24)

	next, cmd := m.handleRepoPickerUpdate(pressKey("enter"))
	got := next.(*Model)
	if got.sessionCfg.Repo.Name != "o/r" {
		t.Fatalf("repo = %q, want o/r", got.sessionCfg.Repo.Name)
	}
	if got.state != viewStateLoading || got.loadingNext != viewStatePRPicker {
		t.Fatalf("state=%v next=%v, want loading then PR picker", got.state, got.loadingNext)
	}
	if cmd == nil {
		t.Fatal("want a fetch-PRs command")
	}

	next, _ = got.handleLoadingUpdate(prsMsg{prs: []review.PR{
		{Number: 7, Title: "fix login", Author: "bob", Additions: 12, Deletions: 3},
		{Number: 2, Title: "older", Author: "ann", Additions: 1, Deletions: 0},
	}})
	got = next.(*Model)
	if got.state != viewStatePRPicker {
		t.Fatalf("state=%v, want PR picker", got.state)
	}
	view := got.prPicker.viewString()
	if !strings.Contains(view, "#7 fix login · bob · +12 -3") {
		t.Fatalf("PR picker missing formatted row:\n%s", view)
	}
}

func TestPRPickerSetsReview(t *testing.T) {
	m := &Model{
		state: viewStatePRPicker,
		sessionCfg: domain.SessionConfig{
			Repo: domain.Repo{Name: "o/r"},
		},
		width:  80,
		height: 24,
		prPicker: NewPRPickerModel([]review.PR{
			{Number: 7, Title: "fix login", Author: "bob", Additions: 12, Deletions: 3},
		}),
	}
	m.prPicker.SetSize(80, 24)

	next, cmd := m.handlePRPickerUpdate(pressKey("enter"))
	got := next.(*Model)
	if got.sessionCfg.ReviewPR != "o/r#7" {
		t.Fatalf("ReviewPR = %q, want o/r#7", got.sessionCfg.ReviewPR)
	}
	if got.state != viewStateLoading || got.loadingNext != viewStateAgentPicker {
		t.Fatalf("state=%v next=%v, want loading then agent picker", got.state, got.loadingNext)
	}
	if cmd == nil {
		t.Fatal("want a fetch-agents command")
	}
}

// Esc from the agent picker returns to the PR picker. A leftover selected
// pointer would treat the next key as another pick and fetch agents again.
func TestPRPickerEscDoesNotReenterAgentPicker(t *testing.T) {
	m := &Model{
		state: viewStatePRPicker,
		sessionCfg: domain.SessionConfig{
			Repo: domain.Repo{Name: "o/r"},
		},
		width:  80,
		height: 24,
		prPicker: NewPRPickerModel([]review.PR{
			{Number: 7, Title: "fix login", Author: "bob", Additions: 12, Deletions: 3},
		}),
	}
	m.prPicker.SetSize(80, 24)

	next, _ := m.handlePRPickerUpdate(pressKey("enter"))
	got := next.(*Model)
	if got.sessionCfg.ReviewPR != "o/r#7" {
		t.Fatalf("ReviewPR = %q, want o/r#7", got.sessionCfg.ReviewPR)
	}
	if got.prPicker.selected != nil {
		t.Fatal("selected still set after consuming the pick")
	}

	got.state = viewStatePRPicker
	next, _ = got.handlePRPickerUpdate(pressKey("down"))
	got = next.(*Model)
	if got.state != viewStatePRPicker {
		t.Fatalf("state=%v, want PR picker (re-entered agent fetch)", got.state)
	}
}
