package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/ciwatch"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/handover"
	"github.com/bouwerp/aiman/internal/infra/local"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
)

// Acceptance tests for multi-repo sessions, piece P6: ci.get reports one
// entry per member of a workspace session, and the mechanical handover
// renders a diff stat per member. Children must not edit this file.

// p6CIResult decodes ci.get's wire shape without naming Go types the piece
// may choose freely.
type p6CIResult struct {
	Type string `json:"type"`
	CI   struct {
		Watch     bool            `json:"watch"`
		PRNumber  int             `json:"pr_number"`
		Escalated []string        `json:"escalated"`
		LastError string          `json:"last_error"`
		Repos     []p6CIRepoEntry `json:"repos"`
	} `json:"ci"`
}

type p6CIRepoEntry struct {
	Repo       string   `json:"repo"`
	PRNumber   int      `json:"pr_number"`
	PRState    string   `json:"pr_state"`
	HeadSHA    string   `json:"head_sha"`
	Escalated  []string `json:"escalated"`
	LastPolled string   `json:"last_polled_at"`
	LastError  string   `json:"last_error"`
}

// p6CIServer is ciServer with a workspace session ws1 (members o/a, o/b) and
// a plain session s1.
func p6CIServer(t *testing.T) (*Server, *sqlite.Repository) {
	t.Helper()
	ws := domain.Session{ID: "ws1", Branch: "host-tab", WorktreePath: "/r/host-tab@ws", WorkingDirectory: "/r/host-tab@ws", Mode: domain.SessionModeAutonomous}
	plain := domain.Session{ID: "s1", WorktreePath: "/w/s1", RepoName: "o/a", Mode: domain.SessionModeAutonomous}
	srv, _, _ := todoServer(t, ws, plain)
	repo := srv.repo.(*sqlite.Repository)
	if err := repo.SetSessionRepos(context.Background(), "ws1", []domain.SessionRepo{
		{Repo: "o/a", Branch: "host-tab", BaseBranch: "main", WorktreePath: "/r/host-tab@ws/a", Ord: 0},
		{Repo: "o/b", Branch: "host-tab", BaseBranch: "develop", WorktreePath: "/r/host-tab@ws/b", Ord: 1},
	}); err != nil {
		t.Fatal(err)
	}
	srv.SetCIWatcher(ciwatch.New(ciwatch.Deps{
		Sessions: repo,
		GitHub:   noGitHub{},
		Todos:    srv.TodoAccess(),
		Steer:    func(string, domain.TodoAuthor, string) {},
		State:    repo,
		Now:      func() time.Time { return time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC) },
	}))
	return srv, repo
}

func TestCIGetWorkspacePerMember(t *testing.T) {
	srv, repo := p6CIServer(t)
	ctx := context.Background()
	polled := time.Date(2026, 11, 27, 11, 58, 0, 0, time.UTC)
	if err := repo.SavePRWatch(ctx, domain.PRWatchState{SessionID: "ws1", Repo: "o/b", PRNumber: 22, PRState: "OPEN", HeadSHA: "bbb", LastPolledAt: polled, LastError: "gh: boom"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SavePRWatch(ctx, domain.PRWatchState{SessionID: "ws1", Repo: "o/a", PRNumber: 11, PRState: "OPEN", HeadSHA: "aaa"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.MarkCheckEscalatedRepo(ctx, "ws1", "o/b", "build", polled); err != nil {
		t.Fatal(err)
	}

	var res p6CIResult
	rpcOK(t, srv, "", "ci.get", map[string]any{"session": "ws1"}, &res)
	if res.Type != "ci" || !res.CI.Watch {
		t.Fatalf("result %+v", res)
	}
	if len(res.CI.Repos) != 2 {
		t.Fatalf("repos %+v, want one entry per member", res.CI.Repos)
	}
	a, b := res.CI.Repos[0], res.CI.Repos[1]
	if a.Repo != "o/a" || b.Repo != "o/b" {
		t.Fatalf("repos %+v, want member order o/a, o/b", res.CI.Repos)
	}
	if a.PRNumber != 11 || a.PRState != "OPEN" || a.HeadSHA != "aaa" || len(a.Escalated) != 0 || a.LastError != "" {
		t.Fatalf("o/a entry %+v", a)
	}
	if b.PRNumber != 22 || b.HeadSHA != "bbb" || b.LastError != "gh: boom" || b.LastPolled != polled.Format(time.RFC3339) ||
		len(b.Escalated) != 1 || b.Escalated[0] != "build" {
		t.Fatalf("o/b entry %+v", b)
	}
	// The scalar fields describe no single PR for a workspace session.
	if res.CI.PRNumber != 0 {
		t.Fatalf("top-level pr_number %d for a workspace session", res.CI.PRNumber)
	}
}

// A member with no stored state is still listed, with no PR.
func TestCIGetWorkspaceUnpolledMemberListed(t *testing.T) {
	srv, _ := p6CIServer(t)
	var res p6CIResult
	rpcOK(t, srv, "", "ci.get", map[string]any{"session": "ws1"}, &res)
	if len(res.CI.Repos) != 2 || res.CI.Repos[0].Repo != "o/a" || res.CI.Repos[1].Repo != "o/b" ||
		res.CI.Repos[0].PRNumber != 0 || res.CI.Repos[1].PRNumber != 0 {
		t.Fatalf("repos %+v", res.CI.Repos)
	}
}

func TestCIGetWorkspacePlainShapeUnchanged(t *testing.T) {
	srv, repo := p6CIServer(t)
	ctx := context.Background()
	if err := repo.SavePRWatch(ctx, domain.PRWatchState{SessionID: "s1", PRNumber: 7, PRState: "OPEN", HeadSHA: "abc"}); err != nil {
		t.Fatal(err)
	}
	resp := rpc(t, srv, "", "ci.get", map[string]any{"session": "s1"})
	if resp.Error != nil {
		t.Fatal(resp.Error)
	}
	var raw map[string]json.RawMessage
	b, _ := json.Marshal(resp.Result)
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	var ci map[string]any
	if err := json.Unmarshal(raw["ci"], &ci); err != nil {
		t.Fatal(err)
	}
	if _, has := ci["repos"]; has {
		t.Fatalf("plain session's ci carries repos: %v", ci)
	}
	if ci["pr_number"] != float64(7) || ci["head_sha"] != "abc" {
		t.Fatalf("plain ci %v", ci)
	}
}

// p6Git runs git in dir with a fixed identity.
func p6Git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func p6Commit(t *testing.T, dir, file string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(file+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p6Git(t, dir, "add", file)
	p6Git(t, dir, "commit", "-q", "-m", file)
}

// p6RecordingRemote runs commands on this machine and records them.
type p6RecordingRemote struct {
	*fakeRemote
	ex   *local.Executor
	mu   sync.Mutex
	cmds []string
}

func (r *p6RecordingRemote) Execute(ctx context.Context, cmd string) (string, error) {
	r.mu.Lock()
	r.cmds = append(r.cmds, cmd)
	r.mu.Unlock()
	return r.ex.Execute(ctx, cmd)
}

// p6HandoverWorkspace builds <root>/host-tab@ws (not a git repository) with
// members a (o/a, base main) and b (o/b, base develop). Each member's branch
// adds one file; b's develop adds dev-only.txt, which a diff against main
// would show.
func p6HandoverWorkspace(t *testing.T) (string, domain.Session) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(root, "host-tab@ws")
	a, b := filepath.Join(ws, "a"), filepath.Join(ws, "b")
	for _, d := range []string{a, b} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			t.Fatal(err)
		}
		p6Git(t, d, "init", "-q", "-b", "main")
	}
	p6Commit(t, a, "a-base.txt")
	p6Git(t, a, "checkout", "-q", "-b", "host-tab")
	p6Commit(t, a, "a-change.txt")

	p6Commit(t, b, "b-base.txt")
	p6Git(t, b, "checkout", "-q", "-b", "develop")
	p6Commit(t, b, "dev-only.txt")
	p6Git(t, b, "checkout", "-q", "-b", "host-tab")
	p6Commit(t, b, "b-change.txt")

	sess := domain.Session{
		ID: "ws1", Name: "host-tab", Branch: "host-tab", WorktreePath: ws, WorkingDirectory: ws,
		Members: []domain.SessionRepo{
			{Repo: "o/a", Branch: "host-tab", BaseBranch: "main", WorktreePath: a, Ord: 0},
			{Repo: "o/b", Branch: "host-tab", BaseBranch: "develop", WorktreePath: b, Ord: 1},
		},
	}
	return ws, sess
}

func TestHandoverWorkspaceDiffStatPerMember(t *testing.T) {
	ws, sess := p6HandoverWorkspace(t)
	remote := &p6RecordingRemote{fakeRemote: &fakeRemote{}, ex: local.NewExecutor(ws)}
	srv := &Server{remote: remote, pty: paneRuntime("$ ready", "ws1")}

	res, err := srv.writeHandover(context.Background(), sess, "restart")
	if err != nil {
		t.Fatal(err)
	}
	if res.Path != filepath.Join(ws, domain.AimanHandoverFileName) {
		t.Fatalf("handover path %q, want it in the workspace directory", res.Path)
	}
	body, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if missing := handover.MissingSections(text); len(missing) != 0 {
		t.Fatalf("missing sections %v\n%s", missing, text)
	}

	// One heading per member, naming its slug and its own base.
	lines := strings.Split(text, "\n")
	headA, headB, diffHeads := -1, -1, 0
	for i, line := range lines {
		if !strings.Contains(line, "git diff --stat") {
			continue
		}
		diffHeads++
		switch {
		case strings.Contains(line, "o/a") && strings.Contains(line, "main"):
			headA = i
		case strings.Contains(line, "o/b") && strings.Contains(line, "develop"):
			headB = i
		}
	}
	if diffHeads != 2 || headA < 0 || headB < 0 || headA > headB {
		t.Fatalf("want one diff heading for o/a (main) then o/b (develop), got %d heading(s)\n%s", diffHeads, text)
	}
	sectionA := strings.Join(lines[headA:headB], "\n")
	sectionB := strings.Join(lines[headB:], "\n")
	if !strings.Contains(sectionA, "a-change.txt") || strings.Contains(sectionA, "b-change.txt") {
		t.Fatalf("o/a section:\n%s", sectionA)
	}
	if !strings.Contains(sectionB, "b-change.txt") || strings.Contains(sectionB, "a-change.txt") {
		t.Fatalf("o/b section:\n%s", sectionB)
	}
	if strings.Contains(text, "dev-only.txt") {
		t.Fatalf("o/b was diffed against main, not its base develop:\n%s", text)
	}

	// Every git command runs in a member worktree, never in the workspace
	// directory itself.
	remote.mu.Lock()
	cmds := append([]string(nil), remote.cmds...)
	remote.mu.Unlock()
	a, b := sess.Members[0].WorktreePath, sess.Members[1].WorktreePath
	for _, cmd := range cmds {
		if strings.Contains(cmd, "git") && !strings.Contains(cmd, a) && !strings.Contains(cmd, b) {
			t.Fatalf("git command outside every member worktree:\n%s", cmd)
		}
	}
}

// A plain session keeps its single diff heading.
func TestHandoverWorkspacePlainSingleDiff(t *testing.T) {
	_, sess := p6HandoverWorkspace(t)
	member := sess.Members[0]
	plain := domain.Session{ID: "s1", Name: "plain", Branch: "host-tab", BaseBranch: "main", RepoName: "o/a",
		WorktreePath: member.WorktreePath, WorkingDirectory: member.WorktreePath}
	srv := &Server{remote: local.NewExecutor(member.WorktreePath), pty: paneRuntime("$ ready", "s1")}
	res, err := srv.writeHandover(context.Background(), plain, "restart")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if n := strings.Count(text, "git diff --stat"); n != 1 || !strings.Contains(text, "Diff (git diff --stat main):") || !strings.Contains(text, "a-change.txt") {
		t.Fatalf("plain handover (%d diff headings):\n%s", n, text)
	}
}
