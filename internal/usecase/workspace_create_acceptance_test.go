package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// Acceptance tests for multi-repo sessions, piece P2. Children must not edit
// this file.

type wsRemote struct{ adHocCreateRemote }

func (r *wsRemote) GetRoot() string { return "/w" }

// wsGit is a RepositoryManager that also implements domain.WorkspaceGit.
type wsGit struct {
	domain.RepositoryManager
	busy     map[string]bool // repo -> branch checked out
	failRepo string
	order    []string // "probe:<repo>", "setup:<repo>:<base>:<path>", "remove:<repo>:<path>"
}

func (g *wsGit) BranchCheckedOut(_ context.Context, _ domain.RemoteExecutor, repo domain.Repo, branch string) (bool, error) {
	g.order = append(g.order, "probe:"+repo.Name)
	return g.busy[repo.Name], nil
}

func (g *wsGit) SetupWorkspaceMember(_ context.Context, _ domain.RemoteExecutor, repo domain.Repo, branch, base, path string) (domain.Worktree, error) {
	g.order = append(g.order, "setup:"+repo.Name+":"+base+":"+path)
	if repo.Name == g.failRepo {
		return domain.Worktree{}, errors.New("worktree add failed")
	}
	b := base
	if b == "" {
		b = "main"
	}
	return domain.Worktree{Path: path, Branch: branch, Base: b}, nil
}

func (g *wsGit) RemoveWorkspaceMember(_ context.Context, _ domain.RemoteExecutor, repo domain.Repo, path string) error {
	g.order = append(g.order, "remove:"+repo.Name+":"+path)
	return nil
}

func (g *wsGit) EnsureAimanSessionFilesGitignored(context.Context, domain.RemoteExecutor, string) error {
	return nil
}

func wsConfig(remote domain.RemoteExecutor, specs ...domain.RepoSpec) domain.SessionConfig {
	return domain.SessionConfig{
		Branch:     "host-tab",
		Workspace:  specs,
		Agent:      &domain.Agent{Name: "Claude Code", Command: "claude"},
		SSHManager: remote,
	}
}

func TestCreateWorkspace(t *testing.T) {
	remote := &wsRemote{}
	git := &wsGit{}
	m := &FlowManager{gitManager: git}
	sess, err := m.CreateSession(context.Background(), wsConfig(remote,
		domain.RepoSpec{Repo: "bouwerp/aiman"},
		domain.RepoSpec{Repo: "bouwerp/aiman-phone", Base: "develop"},
	))
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.WorktreePath != "/w/host-tab@ws" || sess.WorkingDirectory != "/w/host-tab@ws" {
		t.Fatalf("worktree %q working dir %q, want the workspace directory", sess.WorktreePath, sess.WorkingDirectory)
	}
	if sess.RepoName != "" || sess.Branch != "host-tab" {
		t.Fatalf("repo %q branch %q", sess.RepoName, sess.Branch)
	}
	want := []domain.SessionRepo{
		{Repo: "bouwerp/aiman", Branch: "host-tab", BaseBranch: "main", WorktreePath: "/w/host-tab@ws/aiman", Ord: 0},
		{Repo: "bouwerp/aiman-phone", Branch: "host-tab", BaseBranch: "develop", WorktreePath: "/w/host-tab@ws/aiman-phone", Ord: 1},
	}
	if len(sess.Members) != 2 || sess.Members[0] != want[0] || sess.Members[1] != want[1] {
		t.Fatalf("members = %+v, want %+v", sess.Members, want)
	}
	// Every repo is probed before any worktree is made.
	if len(git.order) < 4 || git.order[0] != "probe:bouwerp/aiman" || git.order[1] != "probe:bouwerp/aiman-phone" {
		t.Fatalf("git calls = %v, want both probes first", git.order)
	}
	if git.order[2] != "setup:bouwerp/aiman::/w/host-tab@ws/aiman" || git.order[3] != "setup:bouwerp/aiman-phone:develop:/w/host-tab@ws/aiman-phone" {
		t.Fatalf("git calls = %v", git.order)
	}
	var marker bool
	for _, c := range remote.commands {
		if strings.Contains(c, domain.WorkspaceMarkerFile) {
			marker = true
		}
		if strings.Contains(c, "/aiman-id") && strings.Contains(c, "@ws/") {
			t.Errorf("a member got aiman-id: %q", c)
		}
	}
	if !marker {
		t.Error("no command writes the aiman-workspace marker")
	}
}

// Two repos with the same name get <owner>-<name> directories.
func TestCreateWorkspaceSameNames(t *testing.T) {
	git := &wsGit{}
	m := &FlowManager{gitManager: git}
	sess, err := m.CreateSession(context.Background(), wsConfig(&wsRemote{},
		domain.RepoSpec{Repo: "acme/app"}, domain.RepoSpec{Repo: "other/app"},
	))
	if err != nil {
		t.Fatal(err)
	}
	if sess.Members[0].WorktreePath != "/w/host-tab@ws/acme-app" || sess.Members[1].WorktreePath != "/w/host-tab@ws/other-app" {
		t.Fatalf("members = %+v", sess.Members)
	}
}

// A branch checked out in any member repo stops the create before anything
// is made, with WORKTREE_EXISTS so serve's suffix retry can run.
func TestCreateWorkspaceBranchBusy(t *testing.T) {
	remote := &wsRemote{}
	git := &wsGit{busy: map[string]bool{"bouwerp/aiman-phone": true}}
	m := &FlowManager{gitManager: git}
	_, err := m.CreateSession(context.Background(), wsConfig(remote,
		domain.RepoSpec{Repo: "bouwerp/aiman"}, domain.RepoSpec{Repo: "bouwerp/aiman-phone"},
	))
	if err == nil || !strings.Contains(err.Error(), "WORKTREE_EXISTS") {
		t.Fatalf("err = %v, want WORKTREE_EXISTS", err)
	}
	for _, c := range git.order {
		if strings.HasPrefix(c, "setup:") {
			t.Fatalf("a worktree was made: %v", git.order)
		}
	}
	for _, c := range remote.commands {
		if strings.Contains(c, "mkdir") && strings.Contains(c, "host-tab@ws") {
			t.Fatalf("workspace directory was made: %q", c)
		}
	}
}

// A failed member removes the members already made and the directory.
func TestCreateWorkspaceRollback(t *testing.T) {
	remote := &wsRemote{}
	git := &wsGit{failRepo: "bouwerp/aiman-phone"}
	m := &FlowManager{gitManager: git}
	_, err := m.CreateSession(context.Background(), wsConfig(remote,
		domain.RepoSpec{Repo: "bouwerp/aiman"}, domain.RepoSpec{Repo: "bouwerp/aiman-phone"},
	))
	if err == nil {
		t.Fatal("create succeeded")
	}
	var removed bool
	for _, c := range git.order {
		if c == "remove:bouwerp/aiman:/w/host-tab@ws/aiman" {
			removed = true
		}
	}
	if !removed {
		t.Fatalf("first member not removed: %v", git.order)
	}
	var rmdir bool
	for _, c := range remote.commands {
		if strings.Contains(c, "rmdir") && strings.Contains(c, "/w/host-tab@ws") {
			rmdir = true
		}
	}
	if !rmdir {
		t.Fatalf("workspace directory not removed: %v", remote.commands)
	}
}
