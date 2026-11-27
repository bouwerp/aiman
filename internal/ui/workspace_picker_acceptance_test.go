package ui

// Acceptance tests for multi-repo (workspace) sessions, piece P5, wizard side
// (.aiman_design.md S1, S2 client side, scenario steps 1-2).
//
// Contract:
//
//   - RepoPickerModel: in the list state, `space` toggles a mark on the
//     highlighted repo. Marked rows show `[x]` in the view and the footer says
//     `<n> marked`. At most 8 marks: a 9th `space` adds nothing and the view
//     shows a message containing "at most 8".
//   - func (m RepoPickerModel) Marked() []domain.Repo: the marked repos in
//     the order they were marked.
//   - func (m RepoPickerModel) Workspace() []domain.Repo: nil until `enter`
//     with 2+ marks, then the marked repos in mark order; `selected` stays nil.
//     `enter` with 0 or 1 mark sets `selected` as today.
//   - handleRepoPickerUpdate: a finished multi-pick sets
//     sessionCfg.Workspace (one domain.RepoSpec per repo, mark order, empty
//     Base), leaves sessionCfg.Repo empty and Directory empty, and goes to the
//     agent picker without any directory step.
//   - handleAgentPickerUpdate: with sessionCfg.Workspace set the summary has
//     no Directory stop and lists every repo.
//   - remoteCreateSpecFrom: Repos carries sessionCfg.Workspace in order; Repo
//     and Dir are empty.

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

func wsPickerRepos(n int) []domain.Repo {
	out := make([]domain.Repo, n)
	for i := range out {
		out[i] = domain.Repo{Name: "org/r" + string(rune('a'+i))}
	}
	return out
}

func wsPicker(repos []domain.Repo) RepoPickerModel {
	p := NewRepoPickerModel(repos, &config.GitConfig{})
	p.SetSize(120, 60)
	return p
}

func wsPress(t *testing.T, p RepoPickerModel, keys ...string) RepoPickerModel {
	t.Helper()
	for _, k := range keys {
		next, _ := p.Update(pressKey(k))
		p = next.(RepoPickerModel)
	}
	return p
}

func repoNames(rs []domain.Repo) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Name
	}
	return out
}

func TestRepoPickerMultiSpaceTogglesMark(t *testing.T) {
	p := wsPicker(wsPickerRepos(3))

	p = wsPress(t, p, "down", "down", "space") // org/rc
	p = wsPress(t, p, "up", "up", "space")     // org/ra
	if got := repoNames(p.Marked()); strings.Join(got, ",") != "org/rc,org/ra" {
		t.Fatalf("Marked() = %v, want [org/rc org/ra] in mark order", got)
	}
	view := ansi.Strip(p.viewString())
	if !strings.Contains(view, "2 marked") {
		t.Fatalf("view does not count the marks:\n%s", view)
	}
	if n := strings.Count(view, "[x]"); n != 2 {
		t.Fatalf("view shows %d marked rows, want 2:\n%s", n, view)
	}

	p = wsPress(t, p, "space") // unmark org/ra
	if got := repoNames(p.Marked()); strings.Join(got, ",") != "org/rc" {
		t.Fatalf("Marked() after unmark = %v, want [org/rc]", got)
	}
	if p.selected != nil || p.Workspace() != nil {
		t.Fatal("space finished the picker")
	}
}

func TestRepoPickerMultiEnterFinishesInMarkOrder(t *testing.T) {
	p := wsPicker(wsPickerRepos(3))
	p = wsPress(t, p, "down", "space", "down", "space", "up", "up", "space") // rb, rc, ra
	p = wsPress(t, p, "enter")
	if p.selected != nil {
		t.Fatalf("enter with 3 marks set a single selection %+v", p.selected)
	}
	if got := repoNames(p.Workspace()); strings.Join(got, ",") != "org/rb,org/rc,org/ra" {
		t.Fatalf("Workspace() = %v, want [org/rb org/rc org/ra]", got)
	}
}

func TestRepoPickerMultiEnterWithoutMarksIsSingle(t *testing.T) {
	p := wsPicker(wsPickerRepos(3))
	p = wsPress(t, p, "down", "enter")
	if p.selected == nil || p.selected.Name != "org/rb" {
		t.Fatalf("selected = %+v, want the highlighted org/rb", p.selected)
	}
	if p.Workspace() != nil {
		t.Fatalf("Workspace() = %v with no marks", p.Workspace())
	}
}

func TestRepoPickerMultiEnterWithOneMarkIsSingle(t *testing.T) {
	p := wsPicker(wsPickerRepos(3))
	p = wsPress(t, p, "down", "down", "space", "enter")
	if p.selected == nil || p.selected.Name != "org/rc" {
		t.Fatalf("selected = %+v, want org/rc", p.selected)
	}
	if p.Workspace() != nil {
		t.Fatalf("Workspace() = %v with one mark", p.Workspace())
	}
}

func TestRepoPickerMultiRefusesNinthMark(t *testing.T) {
	p := wsPicker(wsPickerRepos(10))
	for i := 0; i < 9; i++ {
		p = wsPress(t, p, "space", "down")
	}
	if n := len(p.Marked()); n != 8 {
		t.Fatalf("Marked() has %d repos, want at most 8", n)
	}
	for _, r := range p.Marked() {
		if r.Name == "org/ri" {
			t.Fatal("the 9th repo was marked")
		}
	}
	if view := ansi.Strip(p.viewString()); !strings.Contains(view, "at most 8") {
		t.Fatalf("a refused 9th mark shows no message:\n%s", view)
	}
}

func TestRepoPickerMultiWizardSetsWorkspace(t *testing.T) {
	m := wizardModel(t)
	m.sessionCfg = domain.SessionConfig{Branch: "host-tab"}
	m.state = viewStateRepoPicker
	m.picker = wsPicker([]domain.Repo{{Name: "bouwerp/aiman"}, {Name: "bouwerp/aiman-phone"}})
	for _, k := range []string{"down", "space", "up", "space", "enter"} {
		got, _ := m.handleRepoPickerUpdate(pressKey(k))
		m = got.(*Model)
	}
	want := []domain.RepoSpec{{Repo: "bouwerp/aiman-phone"}, {Repo: "bouwerp/aiman"}}
	if len(m.sessionCfg.Workspace) != 2 || m.sessionCfg.Workspace[0] != want[0] || m.sessionCfg.Workspace[1] != want[1] {
		t.Fatalf("Workspace = %+v, want %+v", m.sessionCfg.Workspace, want)
	}
	if m.sessionCfg.Repo.Name != "" {
		t.Fatalf("Repo = %q, want empty for a workspace", m.sessionCfg.Repo.Name)
	}
	if m.sessionCfg.Directory != "" {
		t.Fatalf("Directory = %q, want empty", m.sessionCfg.Directory)
	}
	if m.state == viewStateDirPicker || m.loadingNext == viewStateDirPicker {
		t.Fatal("a multi-repo pick opened the directory step")
	}
	if m.loadingNext != viewStateAgentPicker {
		t.Fatalf("loadingNext = %v, want the agent picker", m.loadingNext)
	}
}

type wsNoopMsg struct{}

func TestRepoPickerMultiSummaryListsEveryRepo(t *testing.T) {
	m := wizardModel(t)
	m.sessionCfg = domain.SessionConfig{
		Branch:    "host-tab",
		Workspace: []domain.RepoSpec{{Repo: "bouwerp/aiman"}, {Repo: "bouwerp/aiman-phone"}},
	}
	m.state = viewStateAgentPicker
	m.agentPicker = NewAgentPickerModel([]domain.Agent{{Name: "Claude", Command: "claude"}})
	m.agentPicker.selected = &domain.Agent{Name: "Claude", Command: "claude"}
	got, _ := m.handleAgentPickerUpdate(wsNoopMsg{})
	m = got.(*Model)
	if m.state != viewStateSummary {
		t.Fatalf("state = %v, want the summary", m.state)
	}
	if m.summary.dirField {
		t.Fatal("a workspace summary has a Directory stop")
	}
	view := ansi.Strip(m.summary.viewString())
	for _, slug := range []string{"bouwerp/aiman", "bouwerp/aiman-phone"} {
		if !strings.Contains(view, slug) {
			t.Fatalf("summary does not list %s:\n%s", slug, view)
		}
	}
	if len(m.sessionCfg.Workspace) != 2 {
		t.Fatalf("the agent step dropped the workspace: %+v", m.sessionCfg.Workspace)
	}
}

func TestCreateSpecWorkspaceCarriesReposInOrder(t *testing.T) {
	cfg := domain.SessionConfig{
		Name:   "host-tab",
		Branch: "host-tab",
		Workspace: []domain.RepoSpec{
			{Repo: "bouwerp/aiman-phone"},
			{Repo: "bouwerp/aiman", Base: "develop"},
		},
		Agent: &domain.Agent{Name: "Claude", Command: "claude"},
	}
	spec := remoteCreateSpecFrom(cfg)
	if len(spec.Repos) != 2 || spec.Repos[0] != cfg.Workspace[0] || spec.Repos[1] != cfg.Workspace[1] {
		t.Fatalf("Repos = %+v, want %+v", spec.Repos, cfg.Workspace)
	}
	if spec.Repo != "" {
		t.Fatalf("Repo = %q, want empty with Repos", spec.Repo)
	}
	if spec.Dir != "" {
		t.Fatalf("Dir = %q, want empty with Repos", spec.Dir)
	}
	if spec.Branch != "host-tab" || spec.Agent != "claude" {
		t.Fatalf("spec = %+v, want branch host-tab and agent claude", spec)
	}
}

func TestCreateSpecWorkspacePlainUnchanged(t *testing.T) {
	spec := remoteCreateSpecFrom(domain.SessionConfig{Branch: "b", Repo: domain.Repo{Name: "org/x"}, Directory: "svc"})
	if spec.Repos != nil {
		t.Fatalf("plain create carries Repos %+v", spec.Repos)
	}
	if spec.Repo != "org/x" || spec.Dir != "svc" {
		t.Fatalf("plain spec = %+v", spec)
	}
}
