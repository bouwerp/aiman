package usecase

import (
	"context"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestWorkspaceMemberDirs(t *testing.T) {
	got := workspaceMemberDirs([]domain.RepoSpec{{Repo: "acme/App"}, {Repo: "x/lib"}, {Repo: "other/app"}})
	want := []string{"acme-App", "lib", "other-app"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dirs = %v, want %v", got, want)
		}
	}
}

func TestWorkspaceRepositoriesSection(t *testing.T) {
	s := WorkspaceRepositoriesSection([]domain.SessionRepo{
		{Repo: "o/a", Branch: "b", BaseBranch: "main", WorktreePath: "/w/b@ws/a"},
		{Repo: "o/c", Branch: "b", WorktreePath: "/w/b@ws/c"},
	})
	for _, want := range []string{"## Repositories", "`o/a`: branch `b` from `main`, at `/w/b@ws/a`", "from `default branch`", "git -C <member dir> push", "gh -R <owner/repo> pr create", "aiman ci local --repo <owner/repo>", "CLAUDE.md or AGENTS.md"} {
		if !strings.Contains(s, want) {
			t.Errorf("section lacks %q:\n%s", want, s)
		}
	}
}

// An existing workspace directory stops the create with WORKTREE_EXISTS
// before anything is made.
type wsExistsRemote struct{ wsRemote }

func (r *wsExistsRemote) Execute(ctx context.Context, cmd string) (string, error) {
	if strings.HasPrefix(cmd, "if [ -e ") {
		r.commands = append(r.commands, cmd)
		return "EXISTS\n", nil
	}
	return r.wsRemote.Execute(ctx, cmd)
}

func TestCreateWorkspaceDirExists(t *testing.T) {
	remote := &wsExistsRemote{}
	git := &wsGit{}
	m := &FlowManager{gitManager: git}
	_, err := m.CreateSession(context.Background(), wsConfig(remote, domain.RepoSpec{Repo: "o/a"}, domain.RepoSpec{Repo: "o/b"}))
	if err == nil || !strings.Contains(err.Error(), "WORKTREE_EXISTS") {
		t.Fatalf("err = %v", err)
	}
	for _, c := range git.order {
		if strings.HasPrefix(c, "setup:") {
			t.Fatalf("a member was made: %v", git.order)
		}
	}
}

func TestMergeContextPacks(t *testing.T) {
	a := "# Shared context\n\nNotes.\n\n- **one** [fact] (`1`): x\n- **two** [fact] (`2`): y\n"
	b := "# Shared context\n\nNotes.\n\n- **two** [fact] (`2`): y\n- **three** [fact] (`3`): z\n"
	got := mergeContextPacks([]string{a, "", b})
	want := "# Shared context\n\nNotes.\n\n- **one** [fact] (`1`): x\n- **two** [fact] (`2`): y\n- **three** [fact] (`3`): z\n"
	if got != want {
		t.Fatalf("got %q", got)
	}
	if mergeContextPacks([]string{"", ""}) != "" {
		t.Fatal("empty packs gave text")
	}
}
