package server

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/artifact"
	"github.com/bouwerp/aiman/internal/cigate"
	"github.com/bouwerp/aiman/internal/design"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
	"github.com/bouwerp/aiman/internal/verify"
)

// Acceptance tests for multi-repo sessions, piece P3: the local CI gate,
// design publish and the refusals of land, --for, harness and verify for a
// workspace session. Children must not edit this file.

const p3Branch = "host-tab"

// p3Workspace is a workspace directory <root>/host-tab@ws (not a git repo)
// holding two member checkouts a (o/a) and b (o/b), each committed on
// host-tab with its own gate list.
type p3Workspace struct {
	dir     string
	a, b    string
	members []domain.SessionRepo
}

func p3MemberRepo(t *testing.T, dir, gate string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".aiman"), 0o750); err != nil {
		t.Fatal(err)
	}
	ciGit(t, dir, "init", "-q", "-b", p3Branch)
	ciGit(t, dir, "config", "user.name", "T")
	ciGit(t, dir, "config", "user.email", "t@example.com")
	if err := os.WriteFile(filepath.Join(dir, ".aiman", "ci-local.yaml"), []byte(gate), 0o600); err != nil {
		t.Fatal(err)
	}
	ciGit(t, dir, "add", "-A")
	ciGit(t, dir, "commit", "-q", "-m", "init")
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

func newP3Workspace(t *testing.T, gateA, gateB string) p3Workspace {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w := p3Workspace{dir: filepath.Join(root, p3Branch+"@ws")}
	w.a = p3MemberRepo(t, filepath.Join(w.dir, "a"), gateA)
	w.b = p3MemberRepo(t, filepath.Join(w.dir, "b"), gateB)
	w.members = []domain.SessionRepo{
		{Repo: "o/a", Branch: p3Branch, BaseBranch: "main", WorktreePath: w.a, Ord: 0},
		{Repo: "o/b", Branch: p3Branch, BaseBranch: "main", WorktreePath: w.b, Ord: 1},
	}
	return w
}

// session is the workspace session row: scalar paths are the workspace
// directory and RepoName is empty.
func (w p3Workspace) session(id string) domain.Session {
	s := todoSession(id)
	s.Branch, s.WorktreePath, s.WorkingDirectory, s.AgentName = p3Branch, w.dir, w.dir, "claude"
	return s
}

// p3SetMembers stores the members of a session already saved on srv.
func p3SetMembers(t *testing.T, srv *Server, id string, members []domain.SessionRepo) {
	t.Helper()
	repo, ok := srv.repo.(*sqlite.Repository)
	if !ok {
		t.Fatalf("server repo is %T, want *sqlite.Repository", srv.repo)
	}
	if err := repo.SetSessionRepos(context.Background(), id, members); err != nil {
		t.Fatal(err)
	}
}

// p3Lookup answers each checkout's PR by its root.
type p3Lookup struct {
	mu    sync.Mutex
	prs   map[string]*cigate.PR
	calls map[string]int
}

func (l *p3Lookup) lookup(_ context.Context, dir, _ string) (*cigate.PR, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	if l.calls == nil {
		l.calls = map[string]int{}
	}
	l.calls[dir]++
	return l.prs[dir], nil
}

func p3CIServer(t *testing.T, w p3Workspace, lookup *p3Lookup, others ...domain.Session) *Server {
	t.Helper()
	srv, _, _ := todoServer(t, append([]domain.Session{w.session("ws")}, others...)...)
	p3SetMembers(t, srv, "ws", w.members)
	srv.SetCILocal(CILocalDeps{
		Runner:    &cigate.Runner{Dir: filepath.Join(t.TempDir(), "ci-local"), MaxParallel: 2},
		Repos:     map[string]config.RepoBootstrap{},
		LookupPR:  lookup.lookup,
		MarkReady: func(context.Context, string, int) error { return nil },
	})
	return srv
}

func p3Pass(t *testing.T, srv *Server, session, repo string) {
	t.Helper()
	var local struct {
		Record cigate.Record `json:"record"`
		Done   bool          `json:"done"`
	}
	rpcOK(t, srv, "", "ci.local", map[string]any{"session": session, "repo": repo, "wait": true}, &local)
	if !local.Done || local.Record.Status != cigate.StatusPass {
		t.Fatalf("ci.local %s %s = %+v", session, repo, local)
	}
}

func p3Gate(t *testing.T, srv *Server, session, kind, command string) ciGateResult {
	t.Helper()
	var gate ciGateResult
	params := map[string]any{"session": session, "kind": kind, "command": command}
	if strings.Contains(command, "--draft") {
		params["draft"] = true
	}
	rpcOK(t, srv, "", "ci.gate", params, &gate)
	return gate
}

// git -C <member> push, absolute or relative to the workspace directory, is
// judged with that member's record and PR.
func TestCIGateWorkspaceDirPush(t *testing.T) {
	w := newP3Workspace(t, okGate, okGate)
	lookup := &p3Lookup{prs: map[string]*cigate.PR{
		w.a: {Number: 1, State: "OPEN"},
		w.b: {Number: 2, State: "OPEN"},
	}}
	srv := p3CIServer(t, w, lookup)

	gate := p3Gate(t, srv, "ws", "push", "git -C a push origin "+p3Branch)
	if gate.Allow || gate.PR == nil || gate.PR.Number != 1 {
		t.Fatalf("relative push of a before any run: %+v, want refused with a's PR #1", gate)
	}
	p3Pass(t, srv, "ws", "o/b")
	for _, cmd := range []string{"git -C b push origin " + p3Branch, "git -C " + w.b + " push origin " + p3Branch, "git -C b push"} {
		if gate := p3Gate(t, srv, "ws", "push", cmd); !gate.Allow {
			t.Fatalf("%q after b passed: %+v, want allowed", cmd, gate)
		}
	}
	gate = p3Gate(t, srv, "ws", "push", "git -C "+w.a+" push origin "+p3Branch)
	if gate.Allow || gate.PR == nil || gate.PR.Number != 1 {
		t.Fatalf("b's pass let a push: %+v", gate)
	}
	p3Pass(t, srv, "ws", "o/a")
	if gate := p3Gate(t, srv, "ws", "push", "git -C a push origin "+p3Branch); !gate.Allow {
		t.Fatalf("a passed: %+v, want allowed", gate)
	}
	if gate := p3Gate(t, srv, "ws", "push", "git -C b push --force"); gate.Allow || gate.Reason != cigate.PushRefusal {
		t.Fatalf("forced push of b: %+v, want the push refusal", gate)
	}
}

// gh -R / --repo <slug> pr create|ready is judged against the member with
// that slug.
func TestCIGateWorkspaceGHRepo(t *testing.T) {
	w := newP3Workspace(t, okGate, okGate)
	srv := p3CIServer(t, w, &p3Lookup{})

	if gate := p3Gate(t, srv, "ws", "create", "gh -R o/b pr create --draft"); gate.Allow {
		t.Fatalf("create for b before b ran: %+v, want refused", gate)
	}
	p3Pass(t, srv, "ws", "o/b")
	for _, c := range []struct{ kind, cmd string }{
		{"create", "gh -R o/b pr create --draft"},
		{"create", "gh --repo o/b pr create"},
		{"ready", "gh --repo o/b pr ready"},
		{"ready", "gh -R O/B pr ready 2"},
	} {
		if gate := p3Gate(t, srv, "ws", c.kind, c.cmd); !gate.Allow {
			t.Fatalf("%q after b passed: %+v, want allowed", c.cmd, gate)
		}
	}
	for _, c := range []struct{ kind, cmd string }{
		{"create", "gh --repo o/a pr create"},
		{"ready", "gh -R o/a pr ready"},
		{"create", "gh -R o/zzz pr create"},
	} {
		if gate := p3Gate(t, srv, "ws", c.kind, c.cmd); gate.Allow {
			t.Fatalf("%q: %+v, want refused (a has no pass; o/zzz is no member)", c.cmd, gate)
		}
	}
}

// A push or PR call that names no repository cannot be placed in a
// workspace session: refused, naming both forms and the members.
func TestCIGateWorkspaceBareRefused(t *testing.T) {
	w := newP3Workspace(t, okGate, okGate)
	srv := p3CIServer(t, w, &p3Lookup{})
	p3Pass(t, srv, "ws", "o/a")
	p3Pass(t, srv, "ws", "o/b")
	for _, c := range []struct{ kind, cmd string }{
		{"push", "git push"},
		{"push", "git push -u origin " + p3Branch},
		{"push", ""},
		{"create", "gh pr create --fill"},
		{"create", ""},
		{"ready", "gh pr ready"},
	} {
		gate := p3Gate(t, srv, "ws", c.kind, c.cmd)
		if gate.Allow {
			t.Fatalf("%s %q: allowed, want refused", c.kind, c.cmd)
		}
		for _, want := range []string{"git -C", "gh -R", "o/a", "o/b"} {
			if !strings.Contains(gate.Reason, want) {
				t.Fatalf("%s %q: reason %q does not name %q", c.kind, c.cmd, gate.Reason, want)
			}
		}
	}
}

// With verify.before_pr on, create and ready for a workspace session skip
// verify and say so; the local gate still applies per member.
func TestCIGateWorkspaceSkipsVerify(t *testing.T) {
	w := newP3Workspace(t, okGate, okGate)
	head := gitIn(t, w.b, "rev-parse", "HEAD")
	lookup := &p3Lookup{prs: map[string]*cigate.PR{w.b: {Number: 2, State: "OPEN", Draft: true, HeadSHA: head}}}
	srv := p3CIServer(t, w, lookup)
	store := verify.NewStore(filepath.Join(shortTempDir(t), "reviews"))
	m := verify.NewManager(verify.Config{BeforePR: true}, srv.VerifyDeps(store, func(string) bool { return true }))
	t.Cleanup(m.Close)
	srv.SetVerifier(m)

	if gate := p3Gate(t, srv, "ws", "create", "gh -R o/b pr create"); gate.Allow {
		t.Fatalf("create before b's local gate: %+v, want refused", gate)
	}
	p3Pass(t, srv, "ws", "o/b")
	const skip = "verify does not cover multi-repo sessions yet"
	for _, c := range []struct{ kind, cmd string }{
		{"create", "gh -R o/b pr create"},
		{"ready", "gh -R o/b pr ready"},
	} {
		gate := p3Gate(t, srv, "ws", c.kind, c.cmd)
		if !gate.Allow || !strings.Contains(gate.Reason, skip) {
			t.Fatalf("%s: %+v, want allowed with reason containing %q", c.kind, gate, skip)
		}
	}
	var ready struct {
		Ready  bool `json:"ready"`
		Number int  `json:"number"`
	}
	rpcOK(t, srv, "", "ci.ready", map[string]any{"session": "ws", "repo": "o/b"}, &ready)
	if !ready.Ready || ready.Number != 2 {
		t.Fatalf("ci.ready for b with no review: %+v, want ready #2", ready)
	}
}

// A git -C directory inside another stored session's workspace member is
// that session's member, not an unowned checkout.
func TestCIOwnerWorkspaceMember(t *testing.T) {
	w := newP3Workspace(t, okGate, okGate)
	lookup := &p3Lookup{prs: map[string]*cigate.PR{w.b: {Number: 7, State: "OPEN"}}}
	plain := todoSession("s1")
	plain.WorktreePath, plain.RepoName = ciRepo(t, okGate), "o/r"
	srv := p3CIServer(t, w, lookup, plain)

	cmd := "git -C " + filepath.Join(w.b, ".aiman") + " push origin " + p3Branch
	if gate := p3Gate(t, srv, "s1", "push", cmd); gate.Allow {
		t.Fatalf("b has an open PR and no pass: %+v, want refused", gate)
	}
	p3Pass(t, srv, "ws", "o/b")
	// An unowned checkout with an open PR stays refused whatever records
	// exist, so an allow here means the push was judged as ws's member b.
	if gate := p3Gate(t, srv, "s1", "push", cmd); !gate.Allow {
		t.Fatalf("ws passed for b: %+v, want allowed as ws's member", gate)
	}
}

// ci.local in a workspace session needs repo; each member has its own run
// and record.
func TestCILocalWorkspaceRepoParam(t *testing.T) {
	slow := "commands:\n  - name: slow\n    run: sleep 2\n"
	fail := "commands:\n  - name: bad\n    run: echo boom; exit 3\n"
	w := newP3Workspace(t, slow, fail)
	srv := p3CIServer(t, w, &p3Lookup{})

	for _, method := range []string{"ci.local", "ci.status"} {
		resp := rpc(t, srv, "", method, map[string]any{"session": "ws"})
		if resp.Error == nil || resp.Error.Code != CodeInvalidParams ||
			!strings.Contains(resp.Error.Message, "o/a") || !strings.Contains(resp.Error.Message, "o/b") {
			t.Fatalf("%s without repo: %+v, want invalid_params naming o/a and o/b", method, resp.Error)
		}
	}
	if resp := rpc(t, srv, "", "ci.local", map[string]any{"session": "ws", "repo": "o/zzz"}); resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("unknown member: %+v, want invalid_params", resp.Error)
	}

	// a's run is still going when b's starts: the runner keys runs by
	// (session, repo), so b is not refused as ci_running.
	rpcOK(t, srv, "", "ci.local", map[string]any{"session": "ws", "repo": "o/a"}, nil)
	var local struct {
		Record cigate.Record `json:"record"`
		Done   bool          `json:"done"`
	}
	rpcOK(t, srv, "", "ci.local", map[string]any{"session": "ws", "repo": "o/b", "wait": true}, &local)
	if !local.Done || local.Record.Status != cigate.StatusFail {
		t.Fatalf("b's run: %+v, want a finished fail", local)
	}

	var st struct {
		Status CIStatus `json:"status"`
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		rpcOK(t, srv, "", "ci.status", map[string]any{"session": "ws", "repo": "o/a"}, &st)
		if st.Status.Record != nil && st.Status.Record.Status.Finished() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("a's run did not finish: %+v", st.Status)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if st.Status.Record.Status != cigate.StatusPass || st.Status.SHA != gitIn(t, w.a, "rev-parse", "HEAD") {
		t.Fatalf("a's status: %+v, want a pass at a's HEAD", st.Status)
	}
	rpcOK(t, srv, "", "ci.status", map[string]any{"session": "ws", "repo": "o/b"}, &st)
	if st.Status.Record == nil || st.Status.Record.Status != cigate.StatusFail || !strings.Contains(st.Status.Tail, "boom") {
		t.Fatalf("b's status: %+v, want b's failed record", st.Status)
	}
}

// design.publish from a workspace session reads a file in the workspace
// directory and falls back to the first member's git dir.
func TestDesignPublishWorkspaceLocalFallback(t *testing.T) {
	w := newP3Workspace(t, okGate, okGate)
	if err := os.WriteFile(filepath.Join(w.dir, "d.md"), []byte("## Decisions\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, _, _ := todoServer(t, w.session("ws"))
	p3SetMembers(t, srv, "ws", w.members)
	srv.SetDesignPublisher(&design.Publisher{Home: t.TempDir(), Run: design.ExecRun})

	var out struct {
		Design design.Result `json:"design"`
	}
	rpcOK(t, srv, "ws", "design.publish", map[string]any{"file": "d.md"}, &out)
	want := filepath.Join(w.a, ".git", "aiman-design", p3Branch+".md")
	if !out.Design.Local || out.Design.Path != want {
		t.Fatalf("result %+v, want local at %s", out.Design, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}
}

// The page's repo name is the first member's, not "repo".
func TestDesignPublishWorkspaceFirstMemberSlug(t *testing.T) {
	w := newP3Workspace(t, okGate, okGate)
	if err := os.WriteFile(filepath.Join(w.dir, "d.md"), []byte("# Plan\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	srv, _, _ := todoServer(t, w.session("ws"))
	p3SetMembers(t, srv, "ws", w.members)
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store, err := artifact.OpenSQLStore(db)
	if err != nil {
		t.Fatal(err)
	}
	b := &memBucket{objs: map[string][]byte{}}
	srv.SetArtifactPublisher(&artifact.Publisher{Bucket: b, Store: store, Host: "aiman.fyi", CanWrite: srv.ArtifactCanWrite})
	srv.SetDesignPublisher(&design.Publisher{Home: t.TempDir(), Run: design.ExecRun})

	var out struct {
		Design design.Result `json:"design"`
	}
	rpcOK(t, srv, "ws", "design.publish", map[string]any{"file": "d.md"}, &out)
	if out.Design.Path != "a/"+p3Branch+".md" {
		t.Fatalf("path %q, want a/%s.md (first member o/a, key = branch)", out.Design.Path, p3Branch)
	}
}

func wantMultiRepoRefusal(t *testing.T, what string, resp Response) {
	t.Helper()
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams || !strings.Contains(resp.Error.Message, "multi-repo session") {
		t.Fatalf("%s: got error %+v result %+v, want invalid_params naming a multi-repo session", what, resp.Error, resp.Result)
	}
}

func TestWorkspaceRefusalsLand(t *testing.T) {
	w := newP3Workspace(t, okGate, okGate)
	p := todoSession("p")
	p.Branch, p.WorktreePath, p.WorkingDirectory, p.RepoName = "feat", ciRepo(t, okGate), "", "o/a"
	child := w.session("ws")
	child.ParentID = "p"
	srv, _, _ := todoServer(t, p, child)
	p3SetMembers(t, srv, "ws", w.members)
	srv.SetLandDir(filepath.Join(t.TempDir(), "land"))
	wantMultiRepoRefusal(t, "session.land", rpc(t, srv, "", "session.land", map[string]any{"session": "ws"}))
}

func TestWorkspaceRefusalsCreateFor(t *testing.T) {
	w := newP3Workspace(t, okGate, okGate)
	srv, _, _ := todoServer(t, w.session("ws"))
	p3SetMembers(t, srv, "ws", w.members)
	creator := &fakeCreator{}
	srv.create = creator
	srv.SetAgentChoice(config.AgentsConfig{Preferred: config.PreferredFromCommands("claude")}, func(context.Context, string) bool { return true })
	err := srv.TodoAccess().Mutate(context.Background(), "ws", domain.AuthorAgent, func(l *domain.TodoList) error {
		l.Items = append(l.Items, domain.TodoItem{ID: "t1", Text: "Add the parser", State: domain.TodoPending})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	wantMultiRepoRefusal(t, "session.create --for", rpc(t, srv, "", "session.create", map[string]any{"for": "t1", "parent": "ws", "repo": "o/a"}))
	if creator.calls != 0 {
		t.Fatalf("creator ran %d times", creator.calls)
	}
}

func TestWorkspaceRefusalsHarness(t *testing.T) {
	fx := harnessFixtureOf(t, harnessSetup{})
	w := newP3Workspace(t, okGate, okGate)
	ws := w.session("ws")
	ws.Status, ws.Name, ws.Group = domain.SessionStatusActive, "ws", "g"
	if err := fx.srv.repo.Save(context.Background(), &ws); err != nil {
		t.Fatal(err)
	}
	p3SetMembers(t, fx.srv, "ws", w.members)
	wantMultiRepoRefusal(t, "harness.list", rpc(t, fx.srv, "", "harness.list", map[string]any{"session": "ws"}))
	wantMultiRepoRefusal(t, "harness.up", rpc(t, fx.srv, "", "harness.up", map[string]any{"session": "ws", "scenario": "plain"}))
}

func TestWorkspaceRefusalsVerify(t *testing.T) {
	w := newP3Workspace(t, okGate, okGate)
	srv, _, _ := todoServer(t, w.session("ws"))
	p3SetMembers(t, srv, "ws", w.members)
	store := verify.NewStore(filepath.Join(shortTempDir(t), "reviews"))
	m := verify.NewManager(verify.Config{Agents: config.PreferredFromCommands("claude", "codex")}, srv.VerifyDeps(store, func(c string) bool { return c == "codex" }))
	t.Cleanup(m.Close)
	srv.SetVerifier(m)
	wantMultiRepoRefusal(t, "verify.start", rpc(t, srv, "", "verify.start", map[string]any{"session": "ws"}))
}
