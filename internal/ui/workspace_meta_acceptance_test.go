package ui

// Acceptance tests for multi-repo (workspace) sessions, piece P5, detail and
// list side (.aiman_design.md S7, scenario step 7).
//
// Contract:
//
//   - detailMetaExtras gains `Members map[string]domain.GitStatus`, keyed by
//     member WorktreePath. renderMetaLine for a workspace session
//     (s.IsWorkspace()) adds one part per member in Ord order: the base name
//     of its WorktreePath, then " ↑<n>" when it is ahead (n > 0), then " ✎"
//     when it has staged, unstaged or untracked files. A member with no status
//     yet is its name alone. A plain session's meta line is unchanged.
//   - gitStatusMsg gains `members map[string]domain.GitStatus`. For a
//     workspace session fetchGitStatus calls gitStatusFetch once per member
//     with a copy of the session whose WorktreePath is the member's (never for
//     the workspace directory itself), and returns one gitStatusMsg whose
//     members is keyed by member WorktreePath. Applying it for the active
//     session makes renderSessionDetail draw the member parts.
//   - newCreatingPlaceholder copies sessionCfg.Workspace into Members (Repo
//     and Ord), so the placeholder IsWorkspace().
//   - creatingPlaceholderMatchesLive: a workspace placeholder matches only a
//     workspace session on the same host and branch with the same member
//     repos in the same order; a plain placeholder (empty RepoName) never
//     matches a workspace session unless the terminal names match.

import (
	"context"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

const wsDir = "/home/code/repos/host-tab@ws"

func wsSession() domain.Session {
	return domain.Session{
		ID:               "ws1",
		Name:             "host-tab",
		Group:            "G",
		RemoteHost:       "regent0",
		TerminalName:     "host-tab",
		Branch:           "host-tab",
		WorktreePath:     wsDir,
		WorkingDirectory: wsDir,
		AgentName:        "claude",
		Status:           domain.SessionStatusActive,
		Backend:          domain.BackendPTY,
		CreatedAt:        time.Now(),
		Members: []domain.SessionRepo{
			{Repo: "bouwerp/aiman", Branch: "host-tab", BaseBranch: "main", WorktreePath: wsDir + "/aiman", Ord: 0},
			{Repo: "bouwerp/aiman-phone", Branch: "host-tab", BaseBranch: "main", WorktreePath: wsDir + "/aiman-phone", Ord: 1},
		},
	}
}

func wsStatuses() map[string]domain.GitStatus {
	return map[string]domain.GitStatus{
		wsDir + "/aiman":       {Branch: "host-tab", Ahead: 2, AheadOfBase: 2, UnpushedCommits: 2},
		wsDir + "/aiman-phone": {Branch: "host-tab", Ahead: 1, AheadOfBase: 1, UnpushedCommits: 1, UnstagedCount: 1},
	}
}

func TestMetaWorkspaceReposOneItemPerMember(t *testing.T) {
	s := wsSession()
	line := ansi.Strip(renderMetaLine(s, detailMetaExtras{Members: wsStatuses()}, time.Now(), 300))
	if !strings.Contains(line, "aiman ↑2 · aiman-phone ↑1 ✎") {
		t.Fatalf("meta = %q, want the members as \"aiman ↑2 · aiman-phone ↑1 ✎\"", line)
	}
}

func TestMetaWorkspaceReposCleanAndUnknownMembers(t *testing.T) {
	s := wsSession()
	x := detailMetaExtras{Members: map[string]domain.GitStatus{wsDir + "/aiman": {Branch: "host-tab"}}}
	line := ansi.Strip(renderMetaLine(s, x, time.Now(), 300))
	if !strings.Contains(line, "aiman · aiman-phone") {
		t.Fatalf("meta = %q, want bare names for a clean member and one with no status", line)
	}
	if strings.Contains(line, "↑") || strings.Contains(line, "✎") {
		t.Fatalf("meta = %q marks a clean member", line)
	}
}

func TestMetaWorkspaceReposPlainSessionUnchanged(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	s := domain.Session{AgentName: "claude", AgentModel: "opus", Branch: "wtb-2445-standby", CreatedAt: now.Add(-42 * time.Minute)}
	if got := ansi.Strip(renderMetaLine(s, detailMetaExtras{}, now, 120)); got != "claude opus · 42m · wtb-2445-standby" {
		t.Fatalf("plain meta changed: %q", got)
	}
}

func TestMetaWorkspaceReposGitStatusPerMember(t *testing.T) {
	s := wsSession()
	m, _ := pbModel(t, s)
	statuses := wsStatuses()

	var mu sync.Mutex
	var asked []string
	prev := gitStatusFetch
	gitStatusFetch = func(_ context.Context, _ *config.Config, got domain.Session) (domain.GitStatus, error) {
		mu.Lock()
		asked = append(asked, got.WorktreePath)
		mu.Unlock()
		return statuses[got.WorktreePath], nil
	}
	t.Cleanup(func() { gitStatusFetch = prev })

	m.activeSession = previewKey(s)
	cmd := m.fetchGitStatus(s, fetchBackground)
	if cmd == nil {
		t.Fatal("no git status fetch for a workspace session")
	}
	msg, ok := cmd().(gitStatusMsg)
	if !ok {
		t.Fatalf("fetch returned %T, want one gitStatusMsg", msg)
	}
	mu.Lock()
	sort.Strings(asked)
	got := strings.Join(asked, ",")
	mu.Unlock()
	if want := wsDir + "/aiman," + wsDir + "/aiman-phone"; got != want {
		t.Fatalf("git status asked for %q, want each member once (%q)", got, want)
	}
	if msg.err != nil {
		t.Fatalf("gitStatusMsg err = %v", msg.err)
	}
	if len(msg.members) != 2 || msg.members[wsDir+"/aiman"].Ahead != 2 || msg.members[wsDir+"/aiman-phone"].UnstagedCount != 1 {
		t.Fatalf("members = %+v, want both statuses keyed by member path", msg.members)
	}

	m.Update(msg)
	it, ok := m.selectedSessionItem()
	if !ok || it.session.ID != s.ID {
		t.Fatal("workspace session not selected")
	}
	detail := ansi.Strip(m.renderSessionDetail(it, 300))
	if !strings.Contains(detail, "aiman ↑2 · aiman-phone ↑1 ✎") {
		t.Fatalf("detail does not show the members' git status:\n%s", detail)
	}
}

func TestPlaceholderMatchWorkspaceCopiesMembers(t *testing.T) {
	cfg := domain.SessionConfig{
		Branch:    "host-tab",
		Workspace: []domain.RepoSpec{{Repo: "bouwerp/aiman"}, {Repo: "bouwerp/aiman-phone"}},
	}
	ph := newCreatingPlaceholder(cfg, config.Remote{Host: "regent0"})
	if !ph.IsWorkspace() || len(ph.Members) != 2 {
		t.Fatalf("placeholder members = %+v, want both repos", ph.Members)
	}
	if ph.Members[0].Repo != "bouwerp/aiman" || ph.Members[1].Repo != "bouwerp/aiman-phone" || ph.Members[1].Ord != 1 {
		t.Fatalf("placeholder members = %+v, want them in order", ph.Members)
	}
	if ph.RepoName != "" {
		t.Fatalf("placeholder RepoName = %q, want empty", ph.RepoName)
	}
}

func wsPlaceholder() domain.Session {
	return domain.Session{
		ID:           domain.EphemeralSessionIDPrefix + "1",
		RemoteHost:   "regent0",
		Branch:       "host-tab",
		TerminalName: "creating host tab",
		Members:      []domain.SessionRepo{{Repo: "bouwerp/aiman", Ord: 0}, {Repo: "bouwerp/aiman-phone", Ord: 1}},
	}
}

func TestPlaceholderMatchWorkspaceMatchesSameMembers(t *testing.T) {
	live := wsSession()
	live.TerminalName = "q7"
	if !creatingPlaceholderMatchesLive(wsPlaceholder(), live) {
		t.Fatal("a workspace placeholder did not match its workspace session")
	}
}

func TestPlaceholderMatchWorkspaceRejectsOtherShapes(t *testing.T) {
	ph := wsPlaceholder()

	plain := domain.Session{ID: "p1", RemoteHost: "regent0", Branch: "host-tab", TerminalName: "q8"}
	if creatingPlaceholderMatchesLive(ph, plain) {
		t.Fatal("a workspace placeholder matched a plain session with no repo")
	}

	other := wsSession()
	other.TerminalName = "q9"
	other.Members = other.Members[:1]
	if creatingPlaceholderMatchesLive(ph, other) {
		t.Fatal("a workspace placeholder matched a workspace with other members")
	}
}

func TestPlaceholderMatchWorkspacePlainPlaceholderSkipsWorkspace(t *testing.T) {
	ph := domain.Session{ID: domain.EphemeralSessionIDPrefix + "2", RemoteHost: "regent0", Branch: "host-tab", TerminalName: "adhoc"}

	same := wsSession()
	same.TerminalName = "q7"
	if creatingPlaceholderMatchesLive(ph, same) {
		t.Fatal("a plain placeholder with no repo matched a workspace session on its branch")
	}

	elsewhere := wsSession()
	elsewhere.RemoteHost = "devbox"
	elsewhere.Branch = "other"
	if creatingPlaceholderMatchesLive(ph, elsewhere) {
		t.Fatal("a plain placeholder matched a workspace session on another host and branch")
	}
}
