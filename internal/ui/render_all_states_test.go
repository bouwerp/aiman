package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/charmbracelet/bubbles/list"
)

// renderModelForAllStates builds a Model with every sub-model initialised, so a render of
// any view state exercises real code rather than tripping over a zero-valued picker.
func renderModelForAllStates(t *testing.T) *Model {
	t.Helper()
	cfg := twoRemoteCfg()
	s := domain.Session{
		ID: "s1", IssueKey: "AIM-1", RepoName: "org/repo", Branch: "feature/x",
		TmuxSession: "aiman-s1", RemoteHost: "10.0.1.5", Status: domain.SessionStatusActive,
		WorktreePath: "/home/ubuntu/repo@feature-x", AgentName: "Claude Code",
		MutagenSyncID: "sync-1", LocalPath: "/tmp/local",
	}
	m := NewModel(cfg, nil, []domain.Session{s}, nil, nil, nil, nil)
	m.SetSize(140, 44)
	m.list.SetItems([]list.Item{item{session: s, remoteName: "dev-box"}})
	m.list.Select(0)
	m.selectedRemote = cfg.Remotes[0]

	// Sub-models the real flows construct before entering their state.
	m.agentPicker = NewAgentPickerModel([]domain.Agent{{Name: "Claude Code", Command: "claude"}})
	m.dirPicker = NewDirPickerModel([]string{"."}, domain.Repo{Name: "org/repo"})
	m.picker = NewRepoPickerModel(nil, &cfg.Git)
	m.issuePicker = NewIssuePickerModel(nil)
	m.branchPicker = NewBranchPickerModel([]string{"main"})
	m.restartingSession = &s
	m.changingDirSession = &s
	return m
}

// statesWithoutOwnScreen are the states renderView draws nothing for. Each is deliberate:
//   - ArchiveSession is transient — it dispatches work and leaves before a frame is drawn.
//   - ScheduledPrompts delegates to a sub-model that is empty without a database.
//   - Picker is a leftover constant with no arm in renderView or Update; nothing sets it.
var statesWithoutOwnScreen = map[viewState]bool{
	viewStateArchiveSession:   true,
	viewStateScheduledPrompts: true,
	viewStatePicker:           true,
}

// Every view state must render without panicking. This is the safety net for the dashboard
// render split: renderView delegates to ~18 methods, and a mis-wired one would surface here.
func TestEveryViewStateRendersWithoutPanic(t *testing.T) {
	for st := viewState(0); st <= viewStateNewGroup; st++ {
		m := renderModelForAllStates(t)
		m.state = st
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("view state %d panicked while rendering: %v", int(st), r)
				}
			}()
			out := m.renderView()
			if strings.TrimSpace(out) == "" && !statesWithoutOwnScreen[st] {
				t.Errorf("view state %d rendered an empty screen", int(st))
			}
		}()
	}
}

// The session panel is assembled from renderSessionPanel + renderGitLines after the split;
// assert the pieces still land in the output.
func TestMainViewShowsSessionAndGitDetail(t *testing.T) {
	m := renderModelForAllStates(t)
	m.gitStatus = domain.GitStatus{Branch: "feature/x", Ahead: 1, StagedCount: 2}
	m.viewport.SetContent("agent output line 1\nagent output line 2")

	out := m.renderMainView()
	for _, want := range []string{"AIM-1", "org/repo", "feature/x", "Git", "agent output line 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in the main view, got:\n%s", want, out)
		}
	}
}
