package server

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
)

// Acceptance tests for multi-repo sessions, piece P4 (terminate and
// ownership). Children must not edit this file.

// wsFixture is a workspace directory holding two member worktrees on branch
// host-tab, each linked to its own main clone with an origin.
type wsFixture struct {
	base, ws string
	mains    map[string]string // dir name -> main clone
	members  []domain.SessionRepo
}

func (f wsFixture) member(name string) string { return filepath.Join(f.ws, name) }

func newWSFixture(t *testing.T) wsFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := wsFixture{base: base, ws: filepath.Join(base, "host-tab@ws"), mains: map[string]string{}}
	if err := os.MkdirAll(f.ws, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"aiman", "aiman-phone"} {
		main := filepath.Join(base, "clones", name)
		if err := os.MkdirAll(main, 0o755); err != nil {
			t.Fatal(err)
		}
		origin := filepath.Join(base, name+".git")
		gitRun(t, base, "init", "--bare", "-q", origin)
		gitRun(t, main, "init", "-q", "-b", "main")
		putFile(t, filepath.Join(main, "a.txt"), "a\n")
		gitRun(t, main, "add", ".")
		gitRun(t, main, "commit", "-qm", "init")
		gitRun(t, main, "remote", "add", "origin", origin)
		gitRun(t, main, "push", "-q", "origin", "main")
		gitRun(t, main, "worktree", "add", "-q", "-b", "host-tab", f.member(name))
		f.mains[name] = main
		f.members = append(f.members, domain.SessionRepo{
			Repo: "bouwerp/" + name, Branch: "host-tab", BaseBranch: "main", WorktreePath: f.member(name), Ord: i,
		})
	}
	// aiman's scaffolding in the workspace directory, as create leaves it.
	putFile(t, filepath.Join(f.ws, domain.AimanTaskFileName), "task\n")
	putFile(t, filepath.Join(f.ws, ".claude/skills/aiman/SKILL.md"), "skill")
	return f
}

// workspaceSession stores the workspace session s1 and its members.
func (r *lifecycleRig) workspaceSession(f wsFixture) domain.Session {
	r.t.Helper()
	s := r.session(domain.Session{ID: "s1", Name: "host-tab", WorktreePath: f.ws, WorkingDirectory: f.ws, Branch: "host-tab", AgentName: "claude"})
	store, ok := r.repo.(*sqlite.Repository)
	if !ok {
		r.t.Fatalf("repo %T is not sqlite", r.repo)
	}
	if err := store.SetSessionRepos(context.Background(), s.ID, f.members); err != nil {
		r.t.Fatal(err)
	}
	got, err := r.repo.Get(context.Background(), s.ID)
	if err != nil || !got.IsWorkspace() {
		r.t.Fatalf("stored workspace session: %+v %v", got, err)
	}
	return *got
}

type wsRepoResult struct {
	Repo            string
	Branch          string
	WorktreeRemoved bool
	WorktreeNote    string
}

func wsRepos(t *testing.T, got map[string]any) map[string]wsRepoResult {
	t.Helper()
	list, ok := got["repos"].([]any)
	if !ok {
		t.Fatalf("result has no repos list: %v", got)
	}
	out := map[string]wsRepoResult{}
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("repos entry %T", item)
		}
		e := wsRepoResult{}
		e.Repo, _ = m["repo"].(string)
		e.Branch, _ = m["branch"].(string)
		removed, ok := m["worktree_removed"].(bool)
		if !ok {
			t.Fatalf("repos entry without worktree_removed: %v", m)
		}
		e.WorktreeRemoved = removed
		e.WorktreeNote, _ = m["worktree_note"].(string)
		out[e.Repo] = e
	}
	return out
}

// A clean, pushed workspace: every member goes through git worktree remove,
// then the workspace directory, which held only scaffolding.
func TestTerminateWorkspaceRemovesMembersThenDir(t *testing.T) {
	r := newLifecycleRig(t)
	f := newWSFixture(t)
	s := r.workspaceSession(f)

	got := lifecycleResult(t, r.terminate(s.ID, false))
	if got["type"] != "session_terminated" || got["session"] != "s1" || got["branch"] != "host-tab" {
		t.Fatalf("scalar fields: %v", got)
	}
	if _, ok := got["worktree_removed"]; !ok {
		t.Fatalf("worktree_removed missing: %v", got)
	}
	repos := wsRepos(t, got)
	if len(repos) != 2 {
		t.Fatalf("repos %v", repos)
	}
	for _, name := range []string{"aiman", "aiman-phone"} {
		e := repos["bouwerp/"+name]
		if !e.WorktreeRemoved || e.Branch != "host-tab" {
			t.Fatalf("%s: %+v", name, e)
		}
		if pathExists(f.member(name)) {
			t.Fatalf("member %s still there", name)
		}
		// git worktree remove, not rm: the main clone forgets the worktree
		// and keeps the branch.
		if strings.Contains(gitRun(t, f.mains[name], "worktree", "list", "--porcelain"), f.member(name)) {
			t.Fatalf("main clone of %s still lists the member", name)
		}
		if gitRun(t, f.mains[name], "rev-parse", "--verify", "refs/heads/host-tab") == "" {
			t.Fatalf("branch deleted in %s", name)
		}
	}
	if pathExists(f.ws) {
		t.Fatal("workspace directory still there")
	}
	if r.rowExists("s1") {
		t.Fatal("row survived")
	}
	if !slices.Contains(r.pty.Kills(), "s1") {
		t.Fatalf("terminal not stopped: %v", r.pty.Kills())
	}
}

// Anything but scaffolding left in the workspace directory keeps it.
func TestTerminateWorkspaceKeepsDirWithOtherFiles(t *testing.T) {
	r := newLifecycleRig(t)
	f := newWSFixture(t)
	s := r.workspaceSession(f)
	putFile(t, filepath.Join(f.ws, "notes.txt"), "mine\n")

	got := lifecycleResult(t, r.terminate(s.ID, false))
	for _, e := range wsRepos(t, got) {
		if !e.WorktreeRemoved {
			t.Fatalf("member kept: %+v", e)
		}
	}
	if !pathExists(filepath.Join(f.ws, "notes.txt")) {
		t.Fatal("workspace directory with a user file was removed")
	}
	if pathExists(f.member("aiman")) || pathExists(f.member("aiman-phone")) {
		t.Fatal("members not removed")
	}
}

// One dirty member refuses the whole call before anything stops; paths name
// the member directory.
func TestTerminateWorkspaceRefusesDirtyMember(t *testing.T) {
	cases := map[string]struct {
		dirty func(t *testing.T, f wsFixture)
		check func(t *testing.T, d WorktreeDirtyData)
	}{
		"uncommitted file": {
			dirty: func(t *testing.T, f wsFixture) { putFile(t, filepath.Join(f.member("aiman-phone"), "x.txt"), "x\n") },
			check: func(t *testing.T, d WorktreeDirtyData) {
				if d.ChangedFiles < 1 || !slices.Contains(d.Paths, "aiman-phone/x.txt") {
					t.Fatalf("%+v", d)
				}
			},
		},
		"unpushed commit": {
			dirty: func(t *testing.T, f wsFixture) {
				m := f.member("aiman")
				putFile(t, filepath.Join(m, "c.txt"), "c\n")
				gitRun(t, m, "add", ".")
				gitRun(t, m, "commit", "-qm", "unpushed")
			},
			check: func(t *testing.T, d WorktreeDirtyData) {
				if d.UnpushedCommits < 1 {
					t.Fatalf("%+v", d)
				}
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := newLifecycleRig(t)
			f := newWSFixture(t)
			s := r.workspaceSession(f)
			tc.dirty(t, f)

			resp := r.terminate(s.ID, false)
			wantCode(t, resp, CodeWorktreeDirty)
			data, ok := resp.Error.Data.(WorktreeDirtyData)
			if !ok {
				t.Fatalf("data %T", resp.Error.Data)
			}
			if data.Session != "s1" {
				t.Fatalf("%+v", data)
			}
			tc.check(t, data)
			if len(r.pty.Kills()) != 0 {
				t.Fatalf("stopped the terminal: %v", r.pty.Kills())
			}
			if !r.rowExists("s1") || !pathExists(f.member("aiman")) || !pathExists(f.member("aiman-phone")) {
				t.Fatal("row or a member gone after the refusal")
			}
		})
	}
}

func TestTerminateWorkspaceDiscardRemovesDirtyMember(t *testing.T) {
	r := newLifecycleRig(t)
	f := newWSFixture(t)
	s := r.workspaceSession(f)
	putFile(t, filepath.Join(f.member("aiman-phone"), "x.txt"), "x\n")

	got := lifecycleResult(t, r.terminate(s.ID, true))
	for repo, e := range wsRepos(t, got) {
		if !e.WorktreeRemoved {
			t.Fatalf("%s kept: %+v", repo, e)
		}
	}
	if pathExists(f.member("aiman-phone")) || pathExists(f.ws) {
		t.Fatal("dirty member or workspace directory left after discard")
	}
	if gitRun(t, f.mains["aiman-phone"], "rev-parse", "--verify", "refs/heads/host-tab") == "" {
		t.Fatal("branch deleted")
	}
}

// A member another stored session works in stays, with a note; so does the
// workspace directory that still holds it.
func TestTerminateWorkspaceKeepsMemberAnotherSessionUses(t *testing.T) {
	r := newLifecycleRig(t)
	f := newWSFixture(t)
	s := r.workspaceSession(f)
	r.session(domain.Session{ID: "s2", WorktreePath: f.member("aiman"), WorkingDirectory: f.member("aiman")})

	got := lifecycleResult(t, r.terminate(s.ID, true))
	repos := wsRepos(t, got)
	if e := repos["bouwerp/aiman"]; e.WorktreeRemoved || e.WorktreeNote == "" {
		t.Fatalf("shared member: %+v", e)
	}
	if e := repos["bouwerp/aiman-phone"]; !e.WorktreeRemoved {
		t.Fatalf("unshared member: %+v", e)
	}
	if !pathExists(f.member("aiman")) || pathExists(f.member("aiman-phone")) {
		t.Fatal("wrong members removed")
	}
	if !pathExists(f.ws) {
		t.Fatal("workspace directory removed while a member is in it")
	}
	if !r.rowExists("s2") || slices.Contains(r.pty.Kills(), "s2") {
		t.Fatal("the sharer was touched")
	}
}

// worktreeUser matches the members of every stored session, not only its
// scalar paths.
func TestWorktreeUserWorkspaceMember(t *testing.T) {
	f := newWSFixture(t)
	ws := domain.Session{ID: "s1", Name: "host-tab", WorktreePath: f.ws, WorkingDirectory: f.ws, Members: f.members}
	other := domain.Session{ID: "s2", Name: "plain", WorktreePath: f.member("aiman"), WorkingDirectory: f.member("aiman")}
	label, shared := worktreeUser(f.member("aiman"), other, []domain.Session{ws, other})
	if !shared || label != "host-tab" {
		t.Fatalf("worktreeUser = %q, %v; want host-tab, true", label, shared)
	}
	// The workspace session itself is never its own sharer.
	if label, shared := worktreeUser(f.member("aiman"), ws, []domain.Session{ws}); shared {
		t.Fatalf("workspace session shares with itself: %q", label)
	}
}

// Terminating another session in a workspace member keeps the member.
func TestWorktreeUserWorkspaceKeepsMemberOnOtherTerminate(t *testing.T) {
	sharers := map[string]func(member string) domain.Session{
		"same worktree path": func(m string) domain.Session { return domain.Session{ID: "s2", WorktreePath: m, WorkingDirectory: m} },
		"working inside it": func(m string) domain.Session {
			return domain.Session{ID: "s2", WorktreePath: m, WorkingDirectory: filepath.Join(m, "sub")}
		},
	}
	for name, mk := range sharers {
		t.Run(name, func(t *testing.T) {
			r := newLifecycleRig(t)
			f := newWSFixture(t)
			r.workspaceSession(f)
			member := f.member("aiman")
			if err := os.MkdirAll(filepath.Join(member, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			r.session(mk(member))

			got := lifecycleResult(t, r.terminate("s2", true))
			if got["worktree_removed"] != false {
				t.Fatalf("%v", got)
			}
			if !pathExists(filepath.Join(member, "a.txt")) {
				t.Fatal("workspace member removed")
			}
			if !r.rowExists("s1") || slices.Contains(r.pty.Kills(), "s1") {
				t.Fatal("the workspace session was touched")
			}
		})
	}
}
