package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/charmbracelet/x/ansi"
)

// renderModelForAllStates builds a Model with every sub-model initialised, so a render of
// any view state exercises real code rather than tripping over a zero-valued picker.
func renderModelForAllStates(t *testing.T) *Model {
	t.Helper()
	cfg := twoRemoteCfg()
	s := domain.Session{
		ID: "s1", IssueKey: "AIM-1", RepoName: "org/repo", Branch: "feature/x",
		TerminalName: "aiman-s1", RemoteHost: "10.0.1.5", Status: domain.SessionStatusActive,
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
	m.agentDefaults = NewAgentDefaultsModel(cfg)
	m.contextStats = NewContextStatsModel(cfg)
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
	for st := viewState(0); st <= viewStateUsage; st++ {
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

// The session panel is the A1 detail pane: header, meta, git/PR, to-dos, preview.
func TestMainViewShowsA1Detail(t *testing.T) {
	m := renderModelForAllStates(t)
	m.gitStatus = domain.GitStatus{
		Branch:      "feature/x",
		Ahead:       1,
		StagedCount: 2,
		PullRequest: &domain.PullRequest{Number: 12, State: "OPEN", ReviewStatus: "approved", ChecksStatus: "success", ChecksSummary: "1/1"},
	}
	m.viewport.SetContent("agent output line 1\nagent output line 2")

	out := m.renderMainView()
	plain := ansi.Strip(out)
	for _, want := range []string{"AIM-1", "Claude Code", "feature/x", "no to-do list yet", "agent output line 1", "aiman ·", "PR #12"} {
		if !strings.Contains(plain, want) {
			t.Errorf("expected %q in the main view, got:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "Git") {
		t.Errorf("git heading should be gone, got:\n%s", plain)
	}
}
