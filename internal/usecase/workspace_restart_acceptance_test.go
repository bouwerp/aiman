package usecase

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/local"
	"github.com/bouwerp/aiman/internal/shellx"
)

// Acceptance tests for multi-repo sessions, piece P4 (restart). Children must
// not edit this file.

func restartWorkspaceSession(ws string) *domain.Session {
	return &domain.Session{
		ID: "ws-1", Branch: "host-tab", AgentName: "claude", TerminalName: "host-tab",
		WorktreePath: ws, WorkingDirectory: ws,
		Members: []domain.SessionRepo{
			{Repo: "bouwerp/aiman", Branch: "host-tab", BaseBranch: "main", WorktreePath: filepath.Join(ws, "aiman"), Ord: 0},
			{Repo: "bouwerp/aiman-phone", Branch: "host-tab", BaseBranch: "main", WorktreePath: filepath.Join(ws, "aiman-phone"), Ord: 1},
		},
	}
}

// Restart trusts the workspace directory and sets safe.directory for every
// member, since each member is its own git repository.
func TestRestartWorkspaceTrustsWorkspaceAndMembers(t *testing.T) {
	remote := &restartSessionRemote{}
	ws := t.TempDir()
	s := restartWorkspaceSession(ws)
	err := RestartSession(context.Background(), remote, &agentRecordingEngine{}, RestartRequest{
		Session:    s,
		SessionCfg: domain.SessionConfig{Agent: &domain.Agent{Name: "claude", Command: "claude"}},
		WriteHandover: func(context.Context, string, string) (HandoverResult, error) {
			return HandoverResult{Exists: true}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	all := remote.all()
	for _, p := range []string{ws, s.Members[0].WorktreePath, s.Members[1].WorktreePath} {
		if !strings.Contains(all, "safe.directory "+shellx.Quote(p)) {
			t.Errorf("no safe.directory for %s:\n%s", p, all)
		}
	}
	if !strings.Contains(remote.files["$HOME/.aiman/tmp/aiman-pty-"+s.ID+".json"], ws) {
		t.Fatalf("agent not relaunched in the workspace directory: %v", remote.files)
	}
}

// wsKitMember makes a git repository at dir holding file and a repo checklist
// named name for glob.
func wsKitMember(t *testing.T, dir, file, name, glob string) {
	t.Helper()
	writeTestFile(t, filepath.Join(dir, file), "x\n")
	writeTestFile(t, filepath.Join(dir, ".aiman/checklists/"+name+".md"), "---\napplies_to: \""+glob+"\"\n---\n- check\n")
	if out, err := exec.Command("git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
}

// A workspace session has no RepoName, but its kit is rebuilt from its
// members' checklists rather than skipped.
func TestRestartWorkspaceBuildsKitFromMembers(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("HOME", t.TempDir())
	ws := t.TempDir()
	s := restartWorkspaceSession(ws)
	wsKitMember(t, s.Members[0].WorktreePath, "a.go", "go", "*.go")
	wsKitMember(t, s.Members[1].WorktreePath, "b.ts", "ts", "*.ts")

	eng := &kitRecordingEngine{}
	_, _, err := prepareRestartSession(context.Background(), local.NewExecutor(""), eng, restartPrep{
		Session:    *s,
		WorkingDir: ws,
		SessionCfg: domain.SessionConfig{Agent: &domain.Agent{Name: "Claude", Command: "claude"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(eng.kits) != 1 {
		t.Fatalf("PrepareSession calls %d, want 1", len(eng.kits))
	}
	kit := eng.kits[0]
	if kit.DoneWhen != nil {
		t.Fatalf("DoneWhen %v, want nil so the Done-when block is kept", kit.DoneWhen)
	}
	var names []string
	for _, c := range kit.Checklists {
		names = append(names, c.Name)
	}
	for _, want := range []string{"go", "ts"} {
		if !slices.Contains(names, want) {
			t.Fatalf("checklists %v, want both members' (%s missing)", names, want)
		}
	}
}
