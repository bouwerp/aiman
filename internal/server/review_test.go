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
	"github.com/bouwerp/aiman/internal/infra/skills"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
	"github.com/bouwerp/aiman/internal/review"
	"github.com/bouwerp/aiman/internal/verify"
)

// fakeGitHub serves fixed PRs by number.
type fakeGitHub struct {
	prs map[int]review.PR
}

func (f *fakeGitHub) ListPRs(context.Context, string) ([]review.PR, error) { return nil, nil }

func (f *fakeGitHub) PR(_ context.Context, ref review.Ref) (review.PR, error) {
	pr, ok := f.prs[ref.Number]
	if !ok {
		return review.PR{}, errors.New("no such PR")
	}
	return pr, nil
}

func (f *fakeGitHub) PostReview(context.Context, review.Ref, review.Post) error { return nil }

// fakePRWorktrees makes the PR worktree as a plain directory under dir.
type fakePRWorktrees struct {
	dir   string
	calls []string
}

func (f *fakePRWorktrees) SetupRemotePRWorktree(_ context.Context, _ domain.RemoteExecutor, repo domain.Repo, n int, base string) (domain.Worktree, error) {
	f.calls = append(f.calls, repo.Name+"|"+base)
	path := filepath.Join(f.dir, "r@pr-7")
	if err := os.MkdirAll(path, 0o750); err != nil {
		return domain.Worktree{}, err
	}
	// A later create resolves its git root from this path and runs git here.
	if out, err := exec.Command("git", "-C", path, "init", "-q").CombinedOutput(); err != nil {
		return domain.Worktree{}, fmt.Errorf("git init: %w: %s", err, out)
	}
	_ = n
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
	stateDir := filepath.Join(t.TempDir(), "reviews")
	srv.SetReviewDir(stateDir)
	return reviewRig{srv: srv, creator: creator, worktrees: wts, stateDir: stateDir}
}

func TestCreateReviewSession(t *testing.T) {
	r := newReviewRig(t)
	var res struct {
		Session SessionInfo `json:"session"`
	}
	rpcOK(t, r.srv, "", "session.create", map[string]any{"review": "o/r#7"}, &res)
	if res.Session.ReviewPR != "o/r#7" {
		t.Fatalf("info review_pr %q", res.Session.ReviewPR)
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
	wt := filepath.Join(r.worktrees.dir, "r@pr-7")
	if cfg.PreparedWorktree != wt || cfg.Repo.Name != "o/r" || cfg.BaseBranch != "main" {
		t.Fatalf("prepared %q repo %q base %q", cfg.PreparedWorktree, cfg.Repo.Name, cfg.BaseBranch)
	}
	if !slices.Equal(r.worktrees.calls, []string{"o/r|main"}) {
		t.Fatalf("worktree calls %v", r.worktrees.calls)
	}

	saved, err := r.srv.repo.Get(context.Background(), "new-id")
	if err != nil {
		t.Fatal(err)
	}
	if saved.ReviewPR != "o/r#7" || saved.Tier != config.TierSmart || saved.WorktreePath != wt {
		t.Fatalf("saved %+v", saved)
	}

	var meta map[string]any
	raw, err := os.ReadFile(filepath.Join(wt, ".aiman_review", "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatal(err)
	}
	if meta["number"] != float64(7) || meta["base"] != "main" || meta["head_sha"] != "abc123" ||
		meta["title"] != "Add the parser" || meta["author"] != "alice" {
		t.Fatalf("meta %v", meta)
	}
	if strings.Contains(string(raw), "secret description") {
		t.Fatal("meta.json carries the PR description")
	}
	for _, name := range []string{"diff.patch", "files.txt"} {
		if _, err := os.Stat(filepath.Join(wt, ".aiman_review", name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(wt, ".aiman_pr_description.md")); !os.IsNotExist(err) {
		t.Fatalf("description written before reveal: %v", err)
	}
	execs := strings.Join(r.srv.remote.(*fakeRemote).execs, "\n")
	if !strings.Contains(execs, "diff --cached") || !strings.Contains(execs, "diff --cached --numstat") {
		t.Fatalf("diff commands %q", execs)
	}

	st, err := review.LoadState(r.stateDir, "new-id")
	if err != nil {
		t.Fatal(err)
	}
	if st.HeadSHA != "abc123" || st.Ref != "o/r#7" || st.Revealed {
		t.Fatalf("state %+v", st)
	}

	list, err := r.srv.TodoAccess().Get("new-id")
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

	events, err := r.srv.repo.(*sqlite.Repository).ListSessionEvents(context.Background(), "new-id", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(events, func(ev domain.SessionEvent) bool { return ev.Kind == domain.EventReviewStarted }) {
		t.Fatalf("no review.started in %+v", events)
	}
}

func TestCreateReviewExists(t *testing.T) {
	r := newReviewRig(t)
	rpcOK(t, r.srv, "", "session.create", map[string]any{"review": "o/r#7"}, nil)
	resp := rpc(t, r.srv, "", "session.create", map[string]any{"review": "o/r#7"})
	if resp.Error == nil || resp.Error.Code != CodeReviewExists {
		t.Fatalf("second create %+v", resp.Error)
	}
	if r.creator.calls != 1 {
		t.Fatalf("creator ran %d times", r.creator.calls)
	}

	// An ended review no longer blocks a new one.
	sess, err := r.srv.repo.Get(context.Background(), "new-id")
	if err != nil {
		t.Fatal(err)
	}
	sess.AgentEnded = true
	if err := r.srv.repo.Save(context.Background(), sess); err != nil {
		t.Fatal(err)
	}
	rpcOK(t, r.srv, "", "session.create", map[string]any{"review": "o/r#7"}, nil)
}

func TestCreateReviewRefusals(t *testing.T) {
	r := newReviewRig(t)
	for name, params := range map[string]map[string]any{
		"bad ref":  {"review": "o/r"},
		"unknown":  {"review": "o/r#8"},
		"with for": {"review": "o/r#7", "for": "t1"},
		"quick":    {"review": "o/r#7", "quick": true},
		"consult":  {"review": "o/r#7", "consult": true},
	} {
		if resp := rpc(t, r.srv, "", "session.create", params); resp.Error == nil {
			t.Errorf("%s: create succeeded", name)
		}
	}
	if r.creator.calls != 0 {
		t.Fatalf("creator ran %d times", r.creator.calls)
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
