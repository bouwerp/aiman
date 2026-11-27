package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/shellx"
)

// wsRemote is a mockRemote that keeps every command it was asked to run.
type wsRemote struct {
	mockRemote
	cmds []string
}

func (r *wsRemote) Execute(ctx context.Context, cmd string) (string, error) {
	r.cmds = append(r.cmds, cmd)
	return r.mockRemote.Execute(ctx, cmd)
}

func (r *wsRemote) ran(cmd string) bool {
	for _, c := range r.cmds {
		if c == cmd {
			return true
		}
	}
	return false
}

func (r *wsRemote) ranContaining(sub string) []string {
	var out []string
	for _, c := range r.cmds {
		if strings.Contains(c, sub) {
			out = append(out, c)
		}
	}
	return out
}

const wsMain = "/repos/myrepo"

func newWorkspaceRemote() *wsRemote {
	return &wsRemote{mockRemote: mockRemote{
		root:    "/repos",
		dirs:    map[string]bool{wsMain: true},
		outputs: map[string]string{},
		errors:  map[string]error{},
	}}
}

func originRefCmd(branch string) string {
	return fmt.Sprintf("git -C %s rev-parse --verify --quiet %s 2>/dev/null", shellx.Quote(wsMain), shellx.Quote("refs/remotes/origin/"+branch+"^{commit}"))
}

func worktreeListCmd() string {
	return fmt.Sprintf("git -C %s worktree list --porcelain", shellx.Quote(wsMain))
}

func TestPorcelainHasBranch(t *testing.T) {
	out := "worktree /repos/myrepo\nHEAD abc\nbranch refs/heads/main\n\n" +
		"worktree /repos/ws/myrepo\nHEAD def\nbranch refs/heads/feat-2\n\n" +
		"worktree /repos/detached\nHEAD 123\ndetached\n"
	cases := []struct {
		branch string
		want   bool
	}{
		{"main", true},
		{"feat-2", true},
		{"feat", false},
		{"ma", false},
		{"other", false},
	}
	for _, c := range cases {
		if got := porcelainHasBranch(out, c.branch); got != c.want {
			t.Errorf("porcelainHasBranch(%q) = %v, want %v", c.branch, got, c.want)
		}
	}
	if !porcelainHasBranch("branch refs/heads/x\r\n", "x") {
		t.Error("CRLF line should match")
	}
}

func TestBranchCheckedOut(t *testing.T) {
	for _, tc := range []struct {
		name string
		list string
		want bool
	}{
		{"present", "worktree /repos/a\nHEAD 1\nbranch refs/heads/feat\n", true},
		{"absent", "worktree /repos/myrepo\nHEAD 1\nbranch refs/heads/main\n", false},
		{"prefix only", "worktree /repos/a\nHEAD 1\nbranch refs/heads/feat-2\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newWorkspaceRemote()
			r.outputs[worktreeListCmd()] = tc.list
			got, err := NewManager(nil).BranchCheckedOut(context.Background(), r, domain.Repo{Name: "owner/myrepo"}, "feat")
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			if !r.ran(fmt.Sprintf("git -C %s fetch origin 2>&1", shellx.Quote(wsMain))) {
				t.Fatalf("main clone was not fetched: %v", r.cmds)
			}
		})
	}
}

func TestBranchCheckedOutListError(t *testing.T) {
	r := newWorkspaceRemote()
	r.errors[worktreeListCmd()] = errors.New("boom")
	if _, err := NewManager(nil).BranchCheckedOut(context.Background(), r, domain.Repo{Name: "myrepo"}, "feat"); err == nil {
		t.Fatal("want error")
	}
}

func TestSetupWorkspaceMember(t *testing.T) {
	const path = "/repos/ws/ABC-1/myrepo"
	addCmd := func(track, start string) string {
		return fmt.Sprintf("git -C %s worktree add %s-B %s -- %s %s", shellx.Quote(wsMain), track, shellx.Quote("feat"), shellx.Quote(path), shellx.Quote(start))
	}
	for _, tc := range []struct {
		name     string
		base     string
		setup    func(*wsRemote)
		wantCmd  string
		wantBase string
	}{
		{
			name: "origin has branch",
			base: "develop",
			setup: func(r *wsRemote) {
				r.outputs[originRefCmd("feat")] = "abc\n"
			},
			wantCmd:  addCmd("--track ", "origin/feat"),
			wantBase: "origin/feat",
		},
		{
			name: "base on origin",
			base: "develop",
			setup: func(r *wsRemote) {
				r.outputs[originRefCmd("develop")] = "def\n"
			},
			wantCmd:  addCmd("", "origin/develop"),
			wantBase: "origin/develop",
		},
		{
			name:     "base already qualified",
			base:     "origin/develop",
			wantCmd:  addCmd("", "origin/develop"),
			wantBase: "origin/develop",
		},
		{
			name: "default branch",
			setup: func(r *wsRemote) {
				r.outputs[fmt.Sprintf("git -C %s rev-parse --abbrev-ref refs/remotes/origin/HEAD 2>/dev/null", shellx.Quote(wsMain))] = "origin/trunk\n"
			},
			wantCmd:  addCmd("", "origin/trunk"),
			wantBase: "origin/trunk",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newWorkspaceRemote()
			if tc.setup != nil {
				tc.setup(r)
			}
			wt, err := NewManager(nil).SetupWorkspaceMember(context.Background(), r, domain.Repo{Name: "owner/myrepo"}, "feat", tc.base, path)
			if err != nil {
				t.Fatal(err)
			}
			if !r.ran(tc.wantCmd) {
				t.Fatalf("missing %q in %v", tc.wantCmd, r.ranContaining("worktree add"))
			}
			if adds := r.ranContaining("worktree add"); len(adds) != 1 {
				t.Fatalf("want one worktree add, got %v", adds)
			}
			want := domain.Worktree{Path: path, Branch: "feat", Base: tc.wantBase}
			if wt != want {
				t.Fatalf("got %+v, want %+v", wt, want)
			}
		})
	}
}

func TestSetupWorkspaceMemberAddFails(t *testing.T) {
	const path = "/repos/ws/myrepo"
	r := newWorkspaceRemote()
	r.outputs[originRefCmd("feat")] = "abc\n"
	add := fmt.Sprintf("git -C %s worktree add --track -B %s -- %s %s", shellx.Quote(wsMain), shellx.Quote("feat"), shellx.Quote(path), shellx.Quote("origin/feat"))
	r.errors[add] = errors.New("exit 128")
	if _, err := NewManager(nil).SetupWorkspaceMember(context.Background(), r, domain.Repo{Name: "myrepo"}, "feat", "", path); err == nil {
		t.Fatal("want error")
	}
}

func TestSetupWorkspaceMemberNeedsAbsolutePath(t *testing.T) {
	r := newWorkspaceRemote()
	if _, err := NewManager(nil).SetupWorkspaceMember(context.Background(), r, domain.Repo{Name: "myrepo"}, "feat", "", "ws/myrepo"); err == nil {
		t.Fatal("want error")
	}
	if len(r.cmds) != 0 {
		t.Fatalf("ran commands: %v", r.cmds)
	}
}

func TestRemoveWorkspaceMember(t *testing.T) {
	const path = "/repos/ws/myrepo"
	remove := fmt.Sprintf("git -C %s worktree remove --force %s", shellx.Quote(wsMain), shellx.Quote(path))
	prune := fmt.Sprintf("git -C %s worktree prune", shellx.Quote(wsMain))

	t.Run("removes and prunes", func(t *testing.T) {
		r := newWorkspaceRemote()
		if err := NewManager(nil).RemoveWorkspaceMember(context.Background(), r, domain.Repo{Name: "myrepo"}, path); err != nil {
			t.Fatal(err)
		}
		if len(r.cmds) != 2 || r.cmds[0] != remove || r.cmds[1] != prune {
			t.Fatalf("cmds = %v", r.cmds)
		}
	})
	t.Run("gone path still prunes", func(t *testing.T) {
		r := newWorkspaceRemote()
		r.errors[remove] = errors.New("not a working tree")
		if err := NewManager(nil).RemoveWorkspaceMember(context.Background(), r, domain.Repo{Name: "myrepo"}, path); err != nil {
			t.Fatal(err)
		}
		if !r.ran(prune) {
			t.Fatalf("prune did not run: %v", r.cmds)
		}
	})
	t.Run("remove fails on existing path", func(t *testing.T) {
		r := newWorkspaceRemote()
		r.dirs[path] = true
		r.errors[remove] = errors.New("locked")
		if err := NewManager(nil).RemoveWorkspaceMember(context.Background(), r, domain.Repo{Name: "myrepo"}, path); err == nil {
			t.Fatal("want error")
		}
	})
}
