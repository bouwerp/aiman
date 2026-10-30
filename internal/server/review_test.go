package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/git"
	"github.com/bouwerp/aiman/internal/infra/local"
	"github.com/bouwerp/aiman/internal/infra/skills"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
	"github.com/bouwerp/aiman/internal/review"
	"github.com/bouwerp/aiman/internal/todo"
	"github.com/bouwerp/aiman/internal/verify"
)

// fakeGitHub serves fixed PRs by number.
type fakeGitHub struct {
	prs   map[int]review.PR
	posts []review.Post
}

func (f *fakeGitHub) ListPRs(context.Context, string) ([]review.PR, error) { return nil, nil }

func (f *fakeGitHub) PR(_ context.Context, ref review.Ref) (review.PR, error) {
	pr, ok := f.prs[ref.Number]
	if !ok {
		return review.PR{}, errors.New("no such PR")
	}
	return pr, nil
}

func (f *fakeGitHub) PostReview(_ context.Context, _ review.Ref, p review.Post) error {
	f.posts = append(f.posts, p)
	return nil
}

// fakePRWorktrees makes the PR worktree as a git repository under dir, or
// fails with err.
type fakePRWorktrees struct {
	dir   string
	err   error
	calls []string
}

func (f *fakePRWorktrees) SetupRemotePRWorktree(_ context.Context, _ domain.RemoteExecutor, repo domain.Repo, n int, base string) (domain.Worktree, error) {
	f.calls = append(f.calls, fmt.Sprintf("%s#%d|%s", repo.Name, n, base))
	if f.err != nil {
		return domain.Worktree{}, f.err
	}
	path := filepath.Join(f.dir, "r@pr-7")
	if err := os.MkdirAll(path, 0o750); err != nil {
		return domain.Worktree{}, err
	}
	// A later create resolves its git root from this path and runs git here.
	if out, err := exec.Command("git", "-C", path, "init", "-q").CombinedOutput(); err != nil {
		return domain.Worktree{}, fmt.Errorf("git init: %w: %s", err, out)
	}
	return domain.Worktree{Path: path, Base: base}, nil
}

type reviewRig struct {
	srv       *Server
	creator   *fakeCreator
	worktrees *fakePRWorktrees
	stateDir  string
}

func newReviewRig(t *testing.T) reviewRig {
	t.Helper()
	srv, _, _ := todoServer(t)
	return wireReviewRig(t, srv)
}

func wireReviewRig(t *testing.T, srv *Server) reviewRig {
	t.Helper()
	creator := &fakeCreator{}
	srv.create = creator
	srv.remote = &fakeRemote{}
	srv.SetAgentChoice(config.AgentsConfig{
		Smart:     []config.PreferredAgent{{Agent: "codex", Model: "gpt-5.5"}},
		Preferred: config.PreferredFromCommands("claude"),
	}, func(context.Context, string) bool { return true })
	srv.SetReviewGitHub(&fakeGitHub{prs: map[int]review.PR{7: {
		Number: 7, Title: "Add the parser", Author: "alice", Body: "secret description",
		BaseRef: "main", HeadRef: "feat/parser", HeadSHA: "abc123",
	}}})
	wts := &fakePRWorktrees{dir: t.TempDir()}
	srv.SetReviewWorktrees(wts)
	stateDir := filepath.Join(t.TempDir(), "pr-reviews")
	srv.SetReviewDir(stateDir)
	return reviewRig{srv: srv, creator: creator, worktrees: wts, stateDir: stateDir}
}

// createReview runs a review create of o/r#7 and returns the new session.
func createReview(t *testing.T, srv *Server) SessionInfo {
	t.Helper()
	var res struct {
		Session SessionInfo `json:"session"`
	}
	rpcOK(t, srv, "", "session.create", map[string]any{"review": "o/r#7"}, &res)
	return res.Session
}

func TestCreateReviewSession(t *testing.T) {
	r := newReviewRig(t)
	info := createReview(t, r.srv)
	if info.ReviewPR != "o/r#7" {
		t.Fatalf("info review_pr %q", info.ReviewPR)
	}
	cfg := r.creator.cfg
	if cfg.ReviewPR != "o/r#7" || !cfg.PromptFree || cfg.InitialPrompt != review.Prompt {
		t.Fatalf("cfg review=%q promptFree=%v prompt=%q", cfg.ReviewPR, cfg.PromptFree, cfg.InitialPrompt)
	}
	if strings.Contains(cfg.InitialPrompt, skills.DesignFirstSuffix) {
		t.Fatal("design-first suffix in a review prompt")
	}
	if cfg.Tier != config.TierSmart || cfg.Agent == nil || cfg.Agent.Command != "codex" {
		t.Fatalf("tier %q agent %+v", cfg.Tier, cfg.Agent)
	}
	if cfg.SessionID != info.ID || cfg.Repo.Name != "o/r" || cfg.BaseBranch != "main" {
		t.Fatalf("id %q/%q repo %q base %q", cfg.SessionID, info.ID, cfg.Repo.Name, cfg.BaseBranch)
	}
	if !slices.Equal(r.worktrees.calls, []string{"o/r#7|main"}) {
		t.Fatalf("worktree calls %v", r.worktrees.calls)
	}

	saved, err := r.srv.repo.Get(context.Background(), info.ID)
	if err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(r.worktrees.dir, "r@pr-7")
	scratch := filepath.Join(r.stateDir, info.ID, "work")
	if saved.ReviewPR != "o/r#7" || saved.Tier != config.TierSmart || saved.WorktreePath != wt || saved.WorkingDirectory != scratch {
		t.Fatalf("saved worktree %q workdir %q, want %q and %q", saved.WorktreePath, saved.WorkingDirectory, wt, scratch)
	}

	var meta map[string]any
	raw, err := os.ReadFile(filepath.Join(scratch, ".aiman_review", "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["number"] != float64(7) || meta["base"] != "main" || meta["head_sha"] != "abc123" ||
		meta["title"] != "Add the parser" || meta["author"] != "alice" || meta["worktree"] != wt {
		t.Fatalf("meta %v", meta)
	}
	if strings.Contains(string(raw), "secret description") {
		t.Fatal("meta.json carries the PR description")
	}
	for _, name := range []string{"diff.patch", "files.txt"} {
		if _, err := os.Stat(filepath.Join(scratch, ".aiman_review", name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, dir := range []string{wt, scratch} {
		if _, err := os.Stat(filepath.Join(dir, ".aiman_pr_description.md")); !os.IsNotExist(err) {
			t.Fatalf("description written before reveal in %s: %v", dir, err)
		}
	}
	if _, err := os.Stat(filepath.Join(wt, ".aiman_review")); !os.IsNotExist(err) {
		t.Fatalf("review files written into the PR worktree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(scratch, ".git")); err != nil {
		t.Fatalf("scratch directory is not a repository: %v", err)
	}
	execs := strings.Join(r.srv.remote.(*fakeRemote).execs, "\n")
	if !strings.Contains(execs, "diff --cached") || !strings.Contains(execs, "diff --cached --numstat") {
		t.Fatalf("diff commands %q", execs)
	}

	st, err := review.LoadState(r.stateDir, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.HeadSHA != "abc123" || st.Ref != "o/r#7" || st.Revealed {
		t.Fatalf("state %+v", st)
	}

	list, err := r.srv.TodoAccess().Get(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	refs := map[string]string{}
	for _, it := range list.Items {
		texts = append(texts, it.Text)
		refs[it.Text] = it.Ref
	}
	want := []string{"Blind review", "Informed pass", "Verify findings", "Publish the brief", "Ask the three questions", "Post the review"}
	if !slices.Equal(texts, want) {
		t.Fatalf("to-dos %q", texts)
	}
	if refs["Blind review"] != "review:blind" || refs["Informed pass"] != "review:informed" {
		t.Fatalf("refs %v", refs)
	}

	events, err := r.srv.repo.(*sqlite.Repository).ListSessionEvents(context.Background(), info.ID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(events, func(ev domain.SessionEvent) bool { return ev.Kind == domain.EventReviewStarted }) {
		t.Fatalf("no review.started in %+v", events)
	}
}

// The agent is launched in the scratch directory: nothing the creator could
// trust, cd into or launch from names the PR worktree, so the agent never
// loads project config from the untrusted PR.
func TestReviewAgentNeverRunsInThePRWorktree(t *testing.T) {
	r := newReviewRig(t)
	info := createReview(t, r.srv)
	cfg := r.creator.cfg
	wt := filepath.Join(r.worktrees.dir, "r@pr-7")
	scratch := filepath.Join(r.stateDir, info.ID, "work")
	if cfg.PreparedWorktree != scratch {
		t.Fatalf("agent cwd %q, want the scratch directory %q", cfg.PreparedWorktree, scratch)
	}
	for field, v := range map[string]string{
		"prepared": cfg.PreparedWorktree, "dir": cfg.Directory, "prompt": cfg.InitialPrompt, "brief": cfg.Brief,
	} {
		if strings.Contains(v, wt) {
			t.Errorf("cfg %s names the PR worktree: %q", field, v)
		}
	}
}

func TestCreateReviewExists(t *testing.T) {
	r := newReviewRig(t)
	first := createReview(t, r.srv)
	resp := rpc(t, r.srv, "", "session.create", map[string]any{"review": "o/r#7"})
	if resp.Error == nil || resp.Error.Code != CodeReviewExists {
		t.Fatalf("second create %+v", resp.Error)
	}
	if r.creator.calls != 1 {
		t.Fatalf("creator ran %d times", r.creator.calls)
	}

	// An ended review no longer blocks a new one.
	sess, err := r.srv.repo.Get(context.Background(), first.ID)
	if err != nil {
		t.Fatal(err)
	}
	sess.AgentEnded = true
	if err := r.srv.repo.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	rpcOK(t, r.srv, "", "session.create", map[string]any{"review": "o/r#7"}, nil)
}

// A second create that starts while the first is inside CreateSession waits
// for the first to save, then sees it and is refused.
func TestCreateReviewConcurrent(t *testing.T) {
	r := newReviewRig(t)
	second := make(chan Response, 1)
	r.creator.onCreate = func() {
		r.creator.onCreate = nil
		go func() { second <- rpc(t, r.srv, "", "session.create", map[string]any{"review": "o/r#7"}) }()
		// Give the second create time to reach the check.
		time.Sleep(50 * time.Millisecond)
	}
	createReview(t, r.srv)
	select {
	case resp := <-second:
		if resp.Error == nil || resp.Error.Code != CodeReviewExists {
			t.Fatalf("concurrent create %+v", resp.Error)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent create did not return")
	}
}

func TestCreateReviewRefusals(t *testing.T) {
	r := newReviewRig(t)
	for name, params := range map[string]map[string]any{
		"bad ref":    {"review": "o/r"},
		"unknown":    {"review": "o/r#8"},
		"with for":   {"review": "o/r#7", "for": "t1"},
		"quick":      {"review": "o/r#7", "quick": true},
		"consult":    {"review": "o/r#7", "consult": true},
		"repo":       {"review": "o/r#7", "repo": "o/r"},
		"branch":     {"review": "o/r#7", "branch": "x"},
		"dir":        {"review": "o/r#7", "dir": "sub"},
		"prompt":     {"review": "o/r#7", "prompt": "hi"},
		"issue":      {"review": "o/r#7", "issue": "PROJ-1"},
		"brief":      {"review": "o/r#7", "brief": "## Objective"},
		"brief file": {"review": "o/r#7", "brief_file": "/tmp/x"},
	} {
		resp := rpc(t, r.srv, "", "session.create", params)
		if resp.Error == nil {
			t.Errorf("%s: create succeeded", name)
			continue
		}
		if name != "unknown" && resp.Error.Code != CodeInvalidParams {
			t.Errorf("%s: code %s, want invalid_params", name, resp.Error.Code)
		}
	}
	if r.creator.calls != 0 {
		t.Fatalf("creator ran %d times", r.creator.calls)
	}
}

// A clone whose origin is another repository is refused before any fetch.
func TestCreateReviewOriginMismatch(t *testing.T) {
	r := newReviewRig(t)
	r.worktrees.err = fmt.Errorf("%w: origin git@github.com:evil/r.git is not o/r", domain.ErrOriginMismatch)
	resp := rpc(t, r.srv, "", "session.create", map[string]any{"review": "o/r#7"})
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams || !strings.Contains(resp.Error.Message, "evil/r") {
		t.Fatalf("create %+v", resp.Error)
	}
}

// A create whose records fail after the session started stops and forgets it.
func TestCreateReviewCleansUpOnFailure(t *testing.T) {
	repo := testRepo(t)
	srv := New(nil, repo, nil, nil, nil, nil, "test") // no to-do store: seeding fails
	r := wireReviewRig(t, srv)
	resp := rpc(t, r.srv, "", "session.create", map[string]any{"review": "o/r#7"})
	if resp.Error == nil || resp.Error.Code != CodeCreateFailed {
		t.Fatalf("create %+v", resp.Error)
	}
	id := r.creator.cfg.SessionID
	if _, err := repo.Get(context.Background(), id); err == nil {
		t.Fatal("the failed review's row is still stored")
	}
	if _, err := os.Stat(filepath.Join(r.stateDir, id)); !os.IsNotExist(err) {
		t.Fatalf("scratch directory left behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r.stateDir, id+".json")); !os.IsNotExist(err) {
		t.Fatalf("state file left behind: %v", err)
	}
}

func TestBeforePRSkipsReview(t *testing.T) {
	srv, store := verifyServer(t, &blockingCreator{})
	m := verify.NewManager(verify.Config{BeforePR: true}, srv.VerifyDeps(store, func(string) bool { return true }))
	t.Cleanup(m.Close)
	srv.SetVerifier(m)
	rv := domain.Session{ID: "rv", Name: "pr-7", Status: domain.SessionStatusActive, ReviewPR: "o/r#7", CreatedAt: time.Now()}
	if err := srv.repo.Save(context.Background(), &rv); err != nil {
		t.Fatal(err)
	}
	var res reviewResult
	rpcOK(t, srv, "rv", "verify.status", map[string]any{"tree_hash": "t1", "gate": true}, &res)
	if res.Review.Status != verify.StatusDisabled {
		t.Fatalf("status %q, want disabled", res.Review.Status)
	}
	events, err := srv.repo.(*sqlite.Repository).ListSessionEvents(context.Background(), "rv", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("gate recorded %+v", events)
	}
}

// GitHub names are case-insensitive: O/R#7 is the same review as o/r#7.
func TestCreateReviewExistsIgnoresCase(t *testing.T) {
	r := newReviewRig(t)
	createReview(t, r.srv)
	resp := rpc(t, r.srv, "", "session.create", map[string]any{"review": "O/R#7"})
	if resp.Error == nil || resp.Error.Code != CodeReviewExists {
		t.Fatalf("create O/R#7: %+v", resp.Error)
	}
	if got := reviewLockKey(review.Ref{Owner: "O", Repo: "R", Number: 7}); got != "o/r#7" {
		t.Fatalf("lock key %q", got)
	}
}

// tickTodo marks the review session's item with ref done.
func tickTodo(t *testing.T, srv *Server, id, ref string) {
	t.Helper()
	err := srv.TodoAccess().Mutate(context.Background(), id, domain.AuthorAiman, func(l *domain.TodoList) error {
		for i := range l.Items {
			if l.Items[i].Ref == ref {
				l.Items[i].State = domain.TodoDone
				return nil
			}
		}
		return fmt.Errorf("no item %s", ref)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// todoStates maps each ref of the session's list to its state.
func todoStates(t *testing.T, srv *Server, id string) map[string]domain.TodoState {
	t.Helper()
	list, err := srv.TodoAccess().Get(id)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]domain.TodoState{}
	for _, it := range list.Items {
		if it.Ref != "" {
			out[it.Ref] = it.State
		}
	}
	return out
}

// startConsult stores a running consult of parent, linked from a to-do.
func startConsult(t *testing.T, srv *Server, parent, id string) {
	t.Helper()
	c := domain.Session{ID: id, Name: "consult-" + id, ParentID: parent, Role: domain.SessionRoleConsult, Status: domain.SessionStatusActive, CreatedAt: time.Now()}
	if err := srv.repo.Save(context.Background(), &c); err != nil {
		t.Fatal(err)
	}
	err := srv.TodoAccess().Mutate(context.Background(), parent, domain.AuthorAgent, func(l *domain.TodoList) error {
		l.Items = append(l.Items, domain.TodoItem{Text: "Blind consult", State: domain.TodoPending, Ref: "child:" + id})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// endConsult records how the tracker settled a consult and forgets its row,
// as RetireConsult does.
func endConsult(t *testing.T, srv *Server, parent, id string, kind domain.EventKind) {
	t.Helper()
	srv.AppendLedger(context.Background(), parent, kind, map[string]any{"child": id, "todo": "t7"})
	if err := srv.repo.Delete(context.Background(), id); err != nil {
		t.Fatal(err)
	}
}

// revealCode is the error code of a review.reveal, or "" when it succeeded.
func revealCode(t *testing.T, srv *Server, params map[string]any) string {
	t.Helper()
	resp := rpc(t, srv, "", "review.reveal", params)
	if resp.Error == nil {
		return ""
	}
	return resp.Error.Code
}

func TestRevealRefusesBeforeBlind(t *testing.T) {
	r := newReviewRig(t)
	info := createReview(t, r.srv)
	if code := revealCode(t, r.srv, map[string]any{"session": info.ID, "single": true}); code != CodeReviewNotReady {
		t.Fatalf("reveal before the blind pass: code %q, want review_not_ready", code)
	}
	if _, err := os.Stat(filepath.Join(r.stateDir, info.ID, "work", ".aiman_pr_description.md")); !os.IsNotExist(err) {
		t.Fatalf("description written: %v", err)
	}
	// A session that is not a review has nothing to reveal.
	other := domain.Session{ID: "plain", Name: "plain", Status: domain.SessionStatusActive}
	if err := r.srv.repo.Save(context.Background(), &other); err != nil {
		t.Fatal(err)
	}
	if code := revealCode(t, r.srv, map[string]any{"session": "plain"}); code != CodeInvalidParams {
		t.Fatalf("reveal of a plain session: code %q", code)
	}
}

func TestRevealWaitsForBlindConsult(t *testing.T) {
	r := newReviewRig(t)
	info := createReview(t, r.srv)
	tickTodo(t, r.srv, info.ID, reviewBlindRef)
	startConsult(t, r.srv, info.ID, "c1")
	if code := revealCode(t, r.srv, map[string]any{"session": info.ID}); code != CodeReviewNotReady {
		t.Fatalf("reveal while the consult runs: code %q", code)
	}
	// single does not skip a consult that is still running.
	if code := revealCode(t, r.srv, map[string]any{"session": info.ID, "single": true}); code != CodeReviewNotReady {
		t.Fatalf("single reveal while the consult runs: code %q", code)
	}
	endConsult(t, r.srv, info.ID, "c1", domain.EventConsultFinished)
	rpcOK(t, r.srv, info.ID, "review.reveal", map[string]any{}, nil)
	st, err := review.LoadState(r.stateDir, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Revealed || st.Single {
		t.Fatalf("state %+v, want revealed with two reviewers", st)
	}
}

func TestRevealSingle(t *testing.T) {
	for _, name := range []string{"no consult", "consult abandoned"} {
		t.Run(name, func(t *testing.T) {
			r := newReviewRig(t)
			info := createReview(t, r.srv)
			tickTodo(t, r.srv, info.ID, reviewBlindRef)
			if name == "consult abandoned" {
				startConsult(t, r.srv, info.ID, "c1")
				endConsult(t, r.srv, info.ID, "c1", domain.EventConsultAbandoned)
			}
			if code := revealCode(t, r.srv, map[string]any{"session": info.ID}); code != CodeReviewNotReady {
				t.Fatalf("reveal without a second reviewer: code %q", code)
			}
			rpcOK(t, r.srv, "", "review.reveal", map[string]any{"session": info.ID, "single": true}, nil)
			st, err := review.LoadState(r.stateDir, info.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !st.Revealed || !st.Single {
				t.Fatalf("state %+v, want revealed single", st)
			}
			var single any
			for _, ev := range ledger(t, r.srv, info.ID) {
				if ev.Kind == domain.EventReviewRevealed {
					var p map[string]any
					_ = json.Unmarshal(ev.Payload, &p)
					single = p["single"]
				}
			}
			if single != true {
				t.Fatalf("review.revealed single = %v", single)
			}
		})
	}
}

func ledger(t *testing.T, srv *Server, id string) []domain.SessionEvent {
	t.Helper()
	events, err := srv.repo.(*sqlite.Repository).ListSessionEvents(context.Background(), id, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestRevealWritesDescription(t *testing.T) {
	r := newReviewRig(t)
	r.srv.SetReviewGitHub(&fakeGitHub{prs: map[int]review.PR{7: {
		Number: 7, Title: "Add the parser", Author: "alice", BaseRef: "main", HeadSHA: "abc123",
		Body: "Parses nested lists.\n\nCloses #12 and fixes PROJ-1; see other/repo#3.",
	}}})
	info := createReview(t, r.srv)
	tickTodo(t, r.srv, info.ID, reviewBlindRef)
	rpcOK(t, r.srv, info.ID, "review.reveal", map[string]any{"single": true}, nil)
	scratch := filepath.Join(r.stateDir, info.ID, "work")
	raw, err := os.ReadFile(filepath.Join(scratch, ".aiman_pr_description.md"))
	if err != nil {
		t.Fatal(err)
	}
	md := string(raw)
	for _, want := range []string{"# Add the parser", "Base: main", "Parses nested lists.", "#12", "PROJ-1", "other/repo#3"} {
		if !strings.Contains(md, want) {
			t.Errorf("description lacks %q:\n%s", want, md)
		}
	}
	if !strings.Contains(md, "Linked issues: #12, PROJ-1, other/repo#3") {
		t.Errorf("linked issues line missing:\n%s", md)
	}
	wt := filepath.Join(r.worktrees.dir, "r@pr-7")
	if _, err := os.Stat(filepath.Join(wt, ".aiman_pr_description.md")); !os.IsNotExist(err) {
		t.Fatalf("description written into the PR worktree: %v", err)
	}
	if !slices.ContainsFunc(ledger(t, r.srv, info.ID), func(ev domain.SessionEvent) bool { return ev.Kind == domain.EventReviewRevealed }) {
		t.Fatal("no review.revealed row")
	}
}

// refreshUpstream makes an origin o/r whose PR 7 adds pr.txt and old.txt
// after main's first commit, then moves main on, and clones it at root/r with
// a github.com origin rewritten to the local upstream.
func refreshUpstream(t *testing.T) (upstream, root string) {
	t.Helper()
	upstream, root = filepath.Join(t.TempDir(), "o", "r"), t.TempDir()
	if err := os.MkdirAll(upstream, 0o750); err != nil {
		t.Fatal(err)
	}
	gitIn(t, upstream, "init", "-q", "-b", "main")
	gitIn(t, upstream, "config", "user.name", "Aiman Test")
	gitIn(t, upstream, "config", "user.email", "aiman@example.com")
	commitFile(t, upstream, "main.txt", "base\n")
	gitIn(t, upstream, "checkout", "-q", "-b", "fork")
	commitFile(t, upstream, "pr.txt", "pr\n")
	commitFile(t, upstream, "old.txt", "old\n")
	gitIn(t, upstream, "update-ref", "refs/pull/7/head", "fork")
	gitIn(t, upstream, "checkout", "-q", "main")
	gitIn(t, upstream, "branch", "-q", "-D", "fork")
	commitFile(t, upstream, "main.txt", "later\n")
	gitIn(t, root, "clone", "-q", upstream, "r")
	clone := filepath.Join(root, "r")
	gitIn(t, clone, "remote", "set-url", "origin", "https://github.com/o/r.git")
	gitIn(t, clone, "config", "url."+upstream+".insteadOf", "https://github.com/o/r.git")
	return upstream, root
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", name)
	gitIn(t, dir, "commit", "-q", "-m", name)
}

// TestRefreshResets moves the PR head (rebased onto a newer main, dropping
// old.txt) and refreshes: the worktree is staged against the new merge base
// with nothing left from the old head, the review files and state name the
// new head, the description is withdrawn, the phase and gate reopen and the
// ledger records the move.
func TestRefreshResets(t *testing.T) {
	upstream, root := refreshUpstream(t)
	r := newReviewRig(t)
	r.srv.remote = local.NewExecutor(root)
	r.srv.SetReviewWorktrees(git.NewManager(nil))
	info := createReview(t, r.srv)
	sess, err := r.srv.repo.Get(context.Background(), info.ID)
	if err != nil {
		t.Fatal(err)
	}
	wt := sess.WorktreePath
	if got := gitIn(t, wt, "diff", "--cached", "--name-only"); got != "old.txt\npr.txt" {
		t.Fatalf("staged at create %q", got)
	}
	before, err := review.LoadState(r.stateDir, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	tickTodo(t, r.srv, info.ID, reviewBlindRef)
	tickTodo(t, r.srv, info.ID, reviewInformedRef)
	err = r.srv.TodoAccess().Mutate(context.Background(), info.ID, domain.AuthorAiman, func(l *domain.TodoList) error {
		todo.AddGate(l, todo.ReviewGateRef)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	rpcOK(t, r.srv, "", "todo.update", map[string]any{"session": info.ID, "id": reviewGateID(t, r.srv, info.ID), "state": "done"}, nil)
	rpcOK(t, r.srv, "", "review.reveal", map[string]any{"session": info.ID, "single": true}, nil)
	scratch := sess.WorkingDirectory

	// main moves on and the PR is rebased onto it without old.txt.
	commitFile(t, upstream, "main2.txt", "newer\n")
	gitIn(t, upstream, "checkout", "-q", "-b", "rebased")
	commitFile(t, upstream, "pr.txt", "pr v2\n")
	newHead := gitIn(t, upstream, "rev-parse", "HEAD")
	gitIn(t, upstream, "update-ref", "refs/pull/7/head", "rebased")
	gitIn(t, upstream, "checkout", "-q", "main")
	newBase := gitIn(t, upstream, "rev-parse", "main")

	rpcOK(t, r.srv, info.ID, "review.refresh", map[string]any{}, nil)

	if got := gitIn(t, wt, "rev-parse", "HEAD"); got != newBase {
		t.Fatalf("HEAD %s, want the new merge base %s", got, newBase)
	}
	if got := gitIn(t, wt, "diff", "--cached", "--name-only"); got != "pr.txt" {
		t.Fatalf("staged after refresh %q, want pr.txt", got)
	}
	if got := gitIn(t, wt, "status", "--porcelain", "--untracked-files=all"); got != "A  pr.txt" {
		t.Fatalf("status after refresh %q", got)
	}
	if _, err := os.Stat(filepath.Join(wt, "old.txt")); !os.IsNotExist(err) {
		t.Fatalf("old.txt left from the old head: %v", err)
	}
	st, err := review.LoadState(r.stateDir, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.HeadSHA != newHead || st.Revealed || st.Single {
		t.Fatalf("state %+v, want head %s, not revealed, not single", st, newHead)
	}
	var meta reviewMeta
	raw, err := os.ReadFile(filepath.Join(scratch, reviewFilesDir, "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &meta); err != nil || meta.HeadSHA != newHead {
		t.Fatalf("meta %s (%v), want head %s", raw, err, newHead)
	}
	diff, err := os.ReadFile(filepath.Join(scratch, reviewFilesDir, "diff.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(diff), "old.txt") || !strings.Contains(string(diff), "pr v2") {
		t.Fatalf("diff.patch not rewritten:\n%s", diff)
	}
	if _, err := os.Stat(filepath.Join(scratch, ".aiman_pr_description.md")); !os.IsNotExist(err) {
		t.Fatalf("description not withdrawn: %v", err)
	}
	states := todoStates(t, r.srv, info.ID)
	for _, ref := range []string{reviewBlindRef, reviewInformedRef, todo.ReviewGateRef} {
		if states[ref] != domain.TodoPending {
			t.Errorf("%s is %s, want pending", ref, states[ref])
		}
	}
	var from, to any
	for _, ev := range ledger(t, r.srv, info.ID) {
		if ev.Kind == domain.EventReviewRefreshed {
			var p map[string]any
			_ = json.Unmarshal(ev.Payload, &p)
			from, to = p["from"], p["to"]
		}
	}
	if from != before.HeadSHA || to != newHead {
		t.Fatalf("review.refreshed from %v to %v, want %s to %s", from, to, before.HeadSHA, newHead)
	}
}

func reviewGateID(t *testing.T, srv *Server, id string) string {
	t.Helper()
	list, err := srv.TodoAccess().Get(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range list.Items {
		if it.Ref == todo.ReviewGateRef {
			return it.ID
		}
	}
	t.Fatal("no review gate")
	return ""
}

// movePRHead makes the fake GitHub report a new head for o/r#7.
func movePRHead(r reviewRig, head string) {
	r.srv.SetReviewGitHub(&fakeGitHub{prs: map[int]review.PR{7: {
		Number: 7, Title: "Add the parser", Author: "alice", Body: "secret description",
		BaseRef: "main", HeadRef: "feat/parser", HeadSHA: head,
	}}})
}

func refreshResult(t *testing.T, srv *Server, caller string, params map[string]any) map[string]any {
	t.Helper()
	var res map[string]any
	rpcOK(t, srv, caller, "review.refresh", params, &res)
	return res
}

// A consult started before a refresh reviewed the old head: refresh retires
// it and records it abandoned, so only a consult started afterwards can
// satisfy the next reveal.
func TestRefreshRetiresRunningConsults(t *testing.T) {
	r := newReviewRig(t)
	info := createReview(t, r.srv)
	tickTodo(t, r.srv, info.ID, reviewBlindRef)
	startConsult(t, r.srv, info.ID, "c1")
	movePRHead(r, "def456")
	refreshResult(t, r.srv, "", map[string]any{"session": info.ID})

	if _, err := r.srv.repo.Get(context.Background(), "c1"); err == nil {
		t.Fatal("the old consult is still stored")
	}
	var abandoned map[string]any
	for _, ev := range ledger(t, r.srv, info.ID) {
		if ev.Kind == domain.EventConsultAbandoned {
			_ = json.Unmarshal(ev.Payload, &abandoned)
		}
	}
	if abandoned["child"] != "c1" || abandoned["reason"] != "refresh" || abandoned["todo"] == "" {
		t.Fatalf("consult.abandoned %v", abandoned)
	}
	list, err := r.srv.TodoAccess().Get(info.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range list.Items {
		if it.Ref == "child:c1" {
			t.Fatalf("item %s still links the retired consult", it.ID)
		}
	}
	tickTodo(t, r.srv, info.ID, reviewBlindRef)
	if code := revealCode(t, r.srv, map[string]any{"session": info.ID}); code != CodeReviewNotReady {
		t.Fatalf("reveal after refresh with no new consult: code %q", code)
	}
	startConsult(t, r.srv, info.ID, "c2")
	endConsult(t, r.srv, info.ID, "c2", domain.EventConsultFinished)
	if code := revealCode(t, r.srv, map[string]any{"session": info.ID}); code != "" {
		t.Fatalf("reveal after the new consult finished: code %q", code)
	}
}

// A consult whose row could not be forgotten still counts as ended once the
// ledger records it.
func TestRevealEndedConsultRowLeftBehind(t *testing.T) {
	r := newReviewRig(t)
	info := createReview(t, r.srv)
	tickTodo(t, r.srv, info.ID, reviewBlindRef)
	startConsult(t, r.srv, info.ID, "c1")
	r.srv.AppendLedger(context.Background(), info.ID, domain.EventConsultFinished, map[string]any{"child": "c1", "todo": "t7"})
	if code := revealCode(t, r.srv, map[string]any{"session": info.ID}); code != "" {
		t.Fatalf("reveal with a finished consult whose row remains: code %q", code)
	}
	// After a refresh the old consult no longer counts as a reviewer, but its
	// leftover row does not block the reveal either.
	r.srv.AppendLedger(context.Background(), info.ID, domain.EventReviewRefreshed, map[string]any{"from": "a", "to": "b"})
	if code := revealCode(t, r.srv, map[string]any{"session": info.ID}); code != CodeReviewNotReady {
		t.Fatalf("reveal after refresh without single: code %q", code)
	}
	if code := revealCode(t, r.srv, map[string]any{"session": info.ID, "single": true}); code != "" {
		t.Fatalf("single reveal with a leftover ended consult row: code %q", code)
	}
}

// GitHub and local write failures are create_failed, not caller errors.
func TestReviewFailuresAreCreateFailed(t *testing.T) {
	r := newReviewRig(t)
	info := createReview(t, r.srv)
	tickTodo(t, r.srv, info.ID, reviewBlindRef)
	r.srv.SetReviewGitHub(&fakeGitHub{})
	if code := revealCode(t, r.srv, map[string]any{"session": info.ID, "single": true}); code != CodeCreateFailed {
		t.Fatalf("reveal with GitHub down: code %q", code)
	}
	if resp := rpc(t, r.srv, "", "review.refresh", map[string]any{"session": info.ID}); resp.Error == nil || resp.Error.Code != CodeCreateFailed {
		t.Fatalf("refresh with GitHub down: %+v", resp.Error)
	}
	movePRHead(r, "abc123")
	// The scratch directory is gone, so the description cannot be written.
	if err := os.RemoveAll(filepath.Join(r.stateDir, info.ID, "work")); err != nil {
		t.Fatal(err)
	}
	if code := revealCode(t, r.srv, map[string]any{"session": info.ID, "single": true}); code != CodeCreateFailed {
		t.Fatalf("reveal with no scratch directory: code %q", code)
	}
}

// A refresh that fails part-way has already withdrawn the description.
func TestRefreshWithdrawsDescriptionFirst(t *testing.T) {
	r := newReviewRig(t)
	info := createReview(t, r.srv)
	tickTodo(t, r.srv, info.ID, reviewBlindRef)
	rpcOK(t, r.srv, "", "review.reveal", map[string]any{"session": info.ID, "single": true}, nil)
	desc := filepath.Join(r.stateDir, info.ID, "work", ".aiman_pr_description.md")
	if _, err := os.Stat(desc); err != nil {
		t.Fatal(err)
	}
	movePRHead(r, "def456")
	r.worktrees.err = errors.New("fetch failed")
	if resp := rpc(t, r.srv, "", "review.refresh", map[string]any{"session": info.ID}); resp.Error == nil || resp.Error.Code != CodeCreateFailed {
		t.Fatalf("refresh %+v", resp.Error)
	}
	if _, err := os.Stat(desc); !os.IsNotExist(err) {
		t.Fatalf("description survived a failed refresh: %v", err)
	}
}

// A head that has not moved is a no-op unless forced.
func TestRefreshNoOpWhenHeadUnchanged(t *testing.T) {
	r := newReviewRig(t)
	info := createReview(t, r.srv)
	tickTodo(t, r.srv, info.ID, reviewBlindRef)
	rpcOK(t, r.srv, "", "review.reveal", map[string]any{"session": info.ID, "single": true}, nil)
	res := refreshResult(t, r.srv, "", map[string]any{"session": info.ID})
	if res["refreshed"] != false || res["type"] != "review" {
		t.Fatalf("unchanged refresh %v", res)
	}
	st, err := review.LoadState(r.stateDir, info.ID)
	if err != nil || !st.Revealed {
		t.Fatalf("state after a no-op refresh %+v (%v)", st, err)
	}
	if todoStates(t, r.srv, info.ID)[reviewBlindRef] != domain.TodoDone {
		t.Fatal("a no-op refresh reopened the blind review")
	}
	res = refreshResult(t, r.srv, "", map[string]any{"session": info.ID, "force": true})
	if res["refreshed"] != true {
		t.Fatalf("forced refresh %v", res)
	}
	if st, _ := review.LoadState(r.stateDir, info.ID); st.Revealed {
		t.Fatal("forced refresh left the review revealed")
	}
}

// Only the review session itself or the operator may refresh it.
func TestRefreshRefusesOtherSession(t *testing.T) {
	r := newReviewRig(t)
	info := createReview(t, r.srv)
	other := domain.Session{ID: "plain", Name: "plain", Status: domain.SessionStatusActive}
	if err := r.srv.repo.Save(context.Background(), &other); err != nil {
		t.Fatal(err)
	}
	if resp := rpc(t, r.srv, "plain", "review.refresh", map[string]any{"session": info.ID}); resp.Error == nil || resp.Error.Code != CodeGateNeedsOperator {
		t.Fatalf("refresh by another session: %+v", resp.Error)
	}
	refreshResult(t, r.srv, info.ID, map[string]any{})
	refreshResult(t, r.srv, "", map[string]any{"session": info.ID})
}
