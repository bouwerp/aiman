package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/git"
	"github.com/bouwerp/aiman/internal/infra/local"
	"github.com/bouwerp/aiman/internal/review"
	"github.com/bouwerp/aiman/internal/todo"
)

const fixReviewFile = ".aiman_review.md"

// fixReviewBody has a blocking finding, a disagreed nit and a question with no
// path. Only the disagreed nit is out of a default fix.
const fixReviewBody = `# Review: o/r#7

## Findings

### F1 blocking pkg/a.go:1
Verified: yes (go test fails)
Source: primary

Nil deref on empty input.

### F2 nit other/c.go:1
Verified: no
Source: secondary
Call: disagree "style only"

Rename this thing.

### F3 question
Verified: no
Source: both

A thought with no location.

## Summary

One bug.
`

// fixRig is a review of o/r#7 over real git: an upstream with the PR's branch,
// a clone serve works from, and a fake GitHub that names the real head.
type fixRig struct {
	reviewRig
	gh       *fakeGitHub
	store    *fakeSteerStore
	upstream string
	clone    string
	head     string
	info     SessionInfo
	scratch  string
}

func newFixRig(t *testing.T) *fixRig {
	t.Helper()
	upstream, root := filepath.Join(t.TempDir(), "o", "r"), t.TempDir()
	if err := os.MkdirAll(upstream, 0o750); err != nil {
		t.Fatal(err)
	}
	gitIn(t, upstream, "init", "-q", "-b", "main")
	gitIn(t, upstream, "config", "user.name", "Aiman Test")
	gitIn(t, upstream, "config", "user.email", "aiman@example.com")
	commitFile(t, upstream, "main.txt", "base\n")
	gitIn(t, upstream, "checkout", "-q", "-b", "feat/parser")
	commitPath(t, upstream, "pkg/a.go", "package pkg\n")
	commitPath(t, upstream, "other/c.go", "package other\n")
	commitPath(t, upstream, ".github/workflows/ci.yml", "on: push\n")
	head := gitIn(t, upstream, "rev-parse", "HEAD")
	gitIn(t, upstream, "update-ref", "refs/pull/7/head", head)
	gitIn(t, upstream, "checkout", "-q", "main")
	gitIn(t, root, "clone", "-q", upstream, "r")
	clone := filepath.Join(root, "r")
	gitIn(t, clone, "remote", "set-url", "origin", "https://github.com/o/r.git")
	gitIn(t, clone, "config", "url."+upstream+".insteadOf", "https://github.com/o/r.git")

	srv, _, store := todoServer(t)
	r := wireReviewRig(t, srv)
	r.srv.remote = local.NewExecutor(root)
	r.srv.SetReviewWorktrees(git.NewManager(nil))
	gh := &fakeGitHub{prs: map[int]review.PR{7: {
		Number: 7, Title: "Add the parser", Author: "alice", Body: "the description",
		BaseRef: "main", HeadRef: "feat/parser", HeadSHA: head,
	}}}
	r.srv.SetReviewGitHub(gh)
	info := createReview(t, r.srv)
	f := &fixRig{
		reviewRig: r, gh: gh, store: store, upstream: upstream, clone: clone, head: head, info: info,
		scratch: filepath.Join(r.stateDir, info.ID, "work"),
	}
	f.writeFile(t, fixReviewBody)
	return f
}

func commitPath(t *testing.T, dir, rel, body string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", rel)
}

func (f *fixRig) writeFile(t *testing.T, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.scratch, fixReviewFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// revealSingle runs the blind phase with one reviewer and reveals.
func (f *fixRig) revealSingle(t *testing.T) {
	t.Helper()
	tickTodo(t, f.srv, f.info.ID, reviewBlindRef)
	rpcOK(t, f.srv, "", "review.reveal", map[string]any{"session": f.info.ID, "single": true}, nil)
}

// revealWithConsult runs the blind phase with a finished consult and reveals.
func (f *fixRig) revealWithConsult(t *testing.T) {
	t.Helper()
	tickTodo(t, f.srv, f.info.ID, reviewBlindRef)
	startConsult(t, f.srv, f.info.ID, "c-blind")
	endConsult(t, f.srv, f.info.ID, "c-blind", domain.EventConsultFinished)
	rpcOK(t, f.srv, "", "review.reveal", map[string]any{"session": f.info.ID}, nil)
}

type startedFixer struct {
	id, tree, work string
	startSHA       string
	findings       []string
}

// startFix runs review.fix as the review session and reads the fixer's row.
func (f *fixRig) startFix(t *testing.T, findings ...string) startedFixer {
	t.Helper()
	params := map[string]any{"file": fixReviewFile}
	if len(findings) > 0 {
		params["findings"] = findings
	}
	var res struct {
		Type     string      `json:"type"`
		Session  string      `json:"session"`
		Fixer    SessionInfo `json:"fixer"`
		Findings []string    `json:"findings"`
		StartSHA string      `json:"start_sha"`
		Round    int         `json:"round"`
	}
	rpcOK(t, f.srv, f.info.ID, "review.fix", params, &res)
	if res.Type != "review_fix" || res.Session != f.info.ID || res.Fixer.ID == "" {
		t.Fatalf("review.fix result %+v", res)
	}
	row, err := f.srv.repo.Get(context.Background(), res.Fixer.ID)
	if err != nil {
		t.Fatal(err)
	}
	return startedFixer{id: row.ID, tree: row.WorktreePath, work: row.WorkingDirectory, startSHA: res.StartSHA, findings: res.Findings}
}

// commitInTree commits files, path to body, in the fix tree.
func commitInTree(t *testing.T, tree string, files map[string]string) string {
	t.Helper()
	gitIn(t, tree, "config", "user.name", "Fixer")
	gitIn(t, tree, "config", "user.email", "fixer@example.com")
	for rel, body := range files {
		path := filepath.Join(tree, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, tree, "add", "-A")
	gitIn(t, tree, "commit", "-q", "-m", "fix")
	return gitIn(t, tree, "rev-parse", "HEAD")
}

const fixGoodReport = "## Findings\n\nF1 fixed go test ./pkg passes\nF3 not fixed nothing to change\n\n## Surprises\n\nnone\n"

func writeFixReport(t *testing.T, work, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(work, domain.AimanReportFileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *fixRig) push(t *testing.T, fixer startedFixer) string {
	t.Helper()
	var res struct {
		Type string `json:"type"`
		SHA  string `json:"sha"`
	}
	rpcOK(t, f.srv, fixer.id, "review.push", map[string]any{}, &res)
	if res.Type != "review_push" || res.SHA == "" {
		t.Fatalf("review.push result %+v", res)
	}
	return res.SHA
}

// fixAndPush runs one good round and leaves GitHub at the pushed head.
func (f *fixRig) fixAndPush(t *testing.T) string {
	t.Helper()
	fixer := f.startFix(t)
	commitInTree(t, fixer.tree, map[string]string{"pkg/a.go": "package pkg // fixed\n"})
	writeFixReport(t, fixer.work, fixGoodReport)
	sha := f.push(t, fixer)
	f.land(t, sha)
	return sha
}

// land makes sha the PR's head, as GitHub does after a push.
func (f *fixRig) land(t *testing.T, sha string) {
	t.Helper()
	gitIn(t, f.upstream, "update-ref", "refs/pull/7/head", sha)
	pr := f.gh.prs[7]
	pr.HeadSHA = sha
	f.gh.prs[7] = pr
}

func (f *fixRig) state(t *testing.T) review.State {
	t.Helper()
	st, err := review.LoadState(f.stateDir, f.info.ID)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func (f *fixRig) closeReviewGate(t *testing.T) {
	t.Helper()
	states := todoStates(t, f.srv, f.info.ID)
	if _, ok := states[todo.ReviewGateRef]; !ok {
		if err := f.srv.TodoAccess().Mutate(t.Context(), f.info.ID, domain.AuthorAiman, func(l *domain.TodoList) error {
			todo.AddGate(l, todo.ReviewGateRef)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	tickTodo(t, f.srv, f.info.ID, todo.ReviewGateRef)
}

func ledgerPayload(t *testing.T, srv *Server, id string, kind domain.EventKind) map[string]any {
	t.Helper()
	var out map[string]any
	for _, ev := range ledger(t, srv, id) {
		if ev.Kind == kind {
			out = map[string]any{}
			if err := json.Unmarshal(ev.Payload, &out); err != nil {
				t.Fatal(err)
			}
		}
	}
	return out
}

func TestReviewFixStartsFixer(t *testing.T) {
	f := newFixRig(t)
	f.revealSingle(t)
	reviewAgent := *f.creator.cfg.Agent
	fixer := f.startFix(t)

	if !slices.Equal(fixer.findings, []string{"F1", "F3"}) {
		t.Fatalf("default findings %v, want everything but the disagreed F2", fixer.findings)
	}
	if fixer.startSHA != f.head {
		t.Fatalf("start sha %s, want %s", fixer.startSHA, f.head)
	}
	row, err := f.srv.repo.Get(context.Background(), fixer.id)
	if err != nil {
		t.Fatal(err)
	}
	if row.Role != domain.SessionRoleFixer || row.Role.Hidden() {
		t.Fatalf("role %q", row.Role)
	}
	if row.FixPR != "o/r#7" || row.ReviewPR != "" || row.ParentID != f.info.ID {
		t.Fatalf("fix_pr %q review_pr %q parent %q", row.FixPR, row.ReviewPR, row.ParentID)
	}
	base := filepath.Join(f.stateDir, fixer.id)
	if fixer.work != filepath.Join(base, "work") || fixer.tree != filepath.Join(base, "tree") {
		t.Fatalf("work %q tree %q, want under %q", fixer.work, fixer.tree, base)
	}

	cfg := f.creator.cfg
	if cfg.Role != domain.SessionRoleFixer || cfg.FixPR != "o/r#7" || cfg.ReviewPR != "" {
		t.Fatalf("cfg role %q fix_pr %q review_pr %q", cfg.Role, cfg.FixPR, cfg.ReviewPR)
	}
	if cfg.PreparedWorktree != fixer.work || !cfg.PromptFree {
		t.Fatalf("agent cwd %q prompt-free %v, want the scratch directory", cfg.PreparedWorktree, cfg.PromptFree)
	}
	if cfg.Agent == nil || cfg.Agent.Command != reviewAgent.Command || cfg.Agent.LaunchModel != reviewAgent.LaunchModel {
		t.Fatalf("agent %+v, want the review's %+v", cfg.Agent, reviewAgent)
	}
	if !strings.Contains(cfg.InitialPrompt, fixer.tree) {
		t.Fatalf("prompt %q does not name the fix tree", cfg.InitialPrompt)
	}

	// The tree is a standalone clone at the head, with nowhere to push.
	if got := gitIn(t, fixer.tree, "remote"); got != "" {
		t.Fatalf("fix tree remotes %q", got)
	}
	if got := gitIn(t, fixer.tree, "rev-parse", "HEAD"); got != f.head {
		t.Fatalf("fix tree HEAD %s, want %s", got, f.head)
	}
	if got := gitIn(t, fixer.tree, "status", "--porcelain"); got != "" {
		t.Fatalf("fix tree status %q", got)
	}
	if st, err := os.Stat(filepath.Join(fixer.tree, ".git")); err != nil || !st.IsDir() {
		t.Fatalf("fix tree .git is not a directory: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(fixer.work, "findings.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "Nil deref on empty input.") || strings.Contains(string(raw), "Rename this thing.") {
		t.Fatalf("findings.md %q", raw)
	}

	st := f.state(t)
	if len(st.Fixes) != 1 || st.Fixes[0].Fixer != fixer.id || st.Fixes[0].StartSHA != f.head || st.Fixes[0].PushedSHA != "" {
		t.Fatalf("fixes %+v", st.Fixes)
	}
	if got := todoStates(t, f.srv, f.info.ID)["fixer:"+fixer.id]; got != domain.TodoPending {
		t.Fatalf("fixer to-do %q", got)
	}
	if ledgerPayload(t, f.srv, f.info.ID, domain.EventKind("review.fix_started")) == nil {
		t.Fatal("no review.fix_started")
	}
}

// Only the review session keeps ReviewPR: a live fixer is no second review.
func TestReviewFixIsNotASecondReview(t *testing.T) {
	f := newFixRig(t)
	f.revealSingle(t)
	fixer := f.startFix(t)
	resp := rpc(t, f.srv, fixer.id, "review.post", map[string]any{"file": "x.md"})
	wantCode(t, resp, CodeInvalidParams)
	wantCode(t, rpc(t, f.srv, fixer.id, "review.reveal", map[string]any{}), CodeInvalidParams)
}

func TestReviewFixNamedFindings(t *testing.T) {
	f := newFixRig(t)
	f.revealSingle(t)
	for _, findings := range [][]string{{"F2"}, {"F9"}} {
		wantCode(t, rpc(t, f.srv, f.info.ID, "review.fix", map[string]any{"file": fixReviewFile, "findings": findings}), CodeInvalidParams)
	}
	fixer := f.startFix(t, "F1")
	if !slices.Equal(fixer.findings, []string{"F1"}) {
		t.Fatalf("findings %v", fixer.findings)
	}
}

func TestReviewFixOperatorMayStart(t *testing.T) {
	f := newFixRig(t)
	f.revealSingle(t)
	rpcOK(t, f.srv, "", "review.fix", map[string]any{"session": f.info.ID, "file": fixReviewFile}, nil)
}

func TestReviewFixRefusals(t *testing.T) {
	t.Run("before reveal", func(t *testing.T) {
		f := newFixRig(t)
		wantCode(t, rpc(t, f.srv, f.info.ID, "review.fix", map[string]any{"file": fixReviewFile}), "review_not_ready")
	})
	t.Run("fork", func(t *testing.T) {
		f := newFixRig(t)
		f.revealSingle(t)
		pr := f.gh.prs[7]
		pr.Fork = true
		f.gh.prs[7] = pr
		wantCode(t, rpc(t, f.srv, f.info.ID, "review.fix", map[string]any{"file": fixReviewFile}), "review_fix_fork")
	})
	t.Run("head moved", func(t *testing.T) {
		f := newFixRig(t)
		f.revealSingle(t)
		pr := f.gh.prs[7]
		pr.HeadSHA = "0000"
		f.gh.prs[7] = pr
		wantCode(t, rpc(t, f.srv, f.info.ID, "review.fix", map[string]any{"file": fixReviewFile}), "review_head_moved")
	})
	t.Run("fixer running", func(t *testing.T) {
		f := newFixRig(t)
		f.revealSingle(t)
		f.startFix(t)
		wantCode(t, rpc(t, f.srv, f.info.ID, "review.fix", map[string]any{"file": fixReviewFile}), "review_fix_running")
	})
	t.Run("fourth round", func(t *testing.T) {
		f := newFixRig(t)
		f.revealSingle(t)
		st := f.state(t)
		st.Fixes = []review.FixRound{{Fixer: "a"}, {Fixer: "b"}, {Fixer: "c"}}
		if err := review.SaveState(f.stateDir, f.info.ID, st); err != nil {
			t.Fatal(err)
		}
		wantCode(t, rpc(t, f.srv, f.info.ID, "review.fix", map[string]any{"file": fixReviewFile}), "review_fix_limit")
	})
	t.Run("another session", func(t *testing.T) {
		f := newFixRig(t)
		f.revealSingle(t)
		other := domain.Session{ID: "other", Name: "other", Status: domain.SessionStatusActive}
		if err := f.srv.repo.Save(context.Background(), &other); err != nil {
			t.Fatal(err)
		}
		wantCode(t, rpc(t, f.srv, "other", "review.fix", map[string]any{"session": f.info.ID, "file": fixReviewFile}), CodeGateNeedsOperator)
	})
}

// A dead fixer's tree stays for inspection until the next round, and its round
// still counts.
func TestReviewFixAfterDeadFixer(t *testing.T) {
	f := newFixRig(t)
	f.revealSingle(t)
	first := f.startFix(t)
	row, err := f.srv.repo.Get(context.Background(), first.id)
	if err != nil {
		t.Fatal(err)
	}
	f.srv.RetireConsult(context.Background(), *row)
	if _, err := os.Stat(first.tree); err != nil {
		t.Fatalf("a retired fixer's tree is kept: %v", err)
	}
	second := f.startFix(t)
	if second.id == first.id {
		t.Fatal("no new fixer")
	}
	if _, err := os.Stat(filepath.Join(f.stateDir, first.id)); !os.IsNotExist(err) {
		t.Fatalf("the earlier fixer's directory stays after the next round: %v", err)
	}
	if st := f.state(t); len(st.Fixes) != 2 {
		t.Fatalf("rounds %d, want 2", len(st.Fixes))
	}
}

func TestReviewFixDirectoriesGoWithTheReview(t *testing.T) {
	f := newFixRig(t)
	f.revealSingle(t)
	fixer := f.startFix(t)
	row, err := f.srv.repo.Get(context.Background(), f.info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.srv.dropSession(context.Background(), *row); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.stateDir, fixer.id)); !os.IsNotExist(err) {
		t.Fatalf("fixer directory left after the review was dropped: %v", err)
	}
	if _, err := f.srv.repo.Get(context.Background(), fixer.id); err == nil {
		t.Fatal("a running fixer outlives its review")
	}
}

// review.run gives a fixer a sandbox of its own, over its working files, so it
// can test an edit before committing.
func TestReviewFixRunsInItsOwnSandbox(t *testing.T) {
	f := newFixRig(t)
	dock := &fakeDocker{out: "ok\n"}
	f.srv.SetReviewRunner(review.Runner{Docker: dock.run, UID: 1000, GID: 1000})
	f.srv.SetReviewConfig(config.ReviewConfig{}, map[string]config.RepoBootstrap{
		"o/r": {Review: config.RepoReview{Image: "golang:1.26"}},
	})
	f.revealSingle(t)
	fixer := f.startFix(t)
	if err := os.WriteFile(filepath.Join(fixer.tree, "pkg", "a.go"), []byte("package pkg // edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rpcOK(t, f.srv, fixer.id, "review.run", map[string]any{"cmd": []string{"go", "test", "./..."}}, nil)
	if _, err := os.Stat(filepath.Join(fixer.work, ".aiman_review", "runs", "1.log")); err != nil {
		t.Fatalf("run log: %v", err)
	}
	src, err := os.ReadFile(filepath.Join(f.stateDir, fixer.id, "src", "pkg", "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "edit") {
		t.Fatalf("sandbox source %q does not hold the uncommitted edit", src)
	}
}

func TestReviewPushPushes(t *testing.T) {
	f := newFixRig(t)
	f.revealSingle(t)
	fixer := f.startFix(t)
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hook := "#!/bin/sh\ntouch " + marker + "\n"
	if err := os.WriteFile(filepath.Join(f.clone, ".git", "hooks", "pre-push"), []byte(hook), 0o700); err != nil { // #nosec G306 -- a test hook must be executable
		t.Fatal(err)
	}
	sha := commitInTree(t, fixer.tree, map[string]string{"pkg/a.go": "package pkg // fixed\n", "pkg/a_test.go": "package pkg\n"})
	writeFixReport(t, fixer.work, fixGoodReport)

	if got := f.push(t, fixer); got != sha {
		t.Fatalf("pushed %s, want %s", got, sha)
	}
	if got := gitIn(t, f.upstream, "rev-parse", "feat/parser"); got != sha {
		t.Fatalf("upstream branch at %s, want %s", got, sha)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("the main clone's pre-push hook ran: %v", err)
	}
	st := f.state(t)
	if len(st.Fixes) != 1 || st.Fixes[0].PushedSHA != sha || !slices.Equal(st.Fixes[0].Fixed, []string{"F1"}) {
		t.Fatalf("fixes %+v", st.Fixes)
	}
	if got := st.FixedMap()["F1"]; got != sha {
		t.Fatalf("fixed map F1 = %q", got)
	}
	p := ledgerPayload(t, f.srv, f.info.ID, domain.EventKind("review.fix_pushed"))
	if p == nil || p["sha"] != sha || p["fixer"] != fixer.id {
		t.Fatalf("review.fix_pushed %v", p)
	}
	if got := todoStates(t, f.srv, f.info.ID)["fixer:"+fixer.id]; got != domain.TodoDone {
		t.Fatalf("fixer to-do %q, want done", got)
	}
	if _, err := f.srv.repo.Get(context.Background(), fixer.id); err == nil {
		t.Fatal("the fixer's row is still stored")
	}
	if _, err := os.Stat(fixer.tree); !os.IsNotExist(err) {
		t.Fatalf("fix tree left behind: %v", err)
	}
	waitForSteerNote(t, f.store, f.info.ID, sha, "aiman review refresh")
}

// waitForSteerNote polls until a note for id contains every part: the steerer
// coalesces before it persists.
func waitForSteerNote(t *testing.T, store *fakeSteerStore, id string, parts ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		store.mu.Lock()
		found := slices.ContainsFunc(store.recs, func(r domain.SteerRecord) bool {
			if r.SessionID != id {
				return false
			}
			for _, part := range parts {
				if !strings.Contains(r.Text, part) {
					return false
				}
			}
			return true
		})
		store.mu.Unlock()
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no steer note for %s containing %q", id, parts)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestReviewPushOnlyFromAFixer(t *testing.T) {
	f := newFixRig(t)
	f.revealSingle(t)
	fixer := f.startFix(t)
	commitInTree(t, fixer.tree, map[string]string{"pkg/a.go": "package pkg // fixed\n"})
	writeFixReport(t, fixer.work, fixGoodReport)
	for _, caller := range []string{f.info.ID, ""} {
		if resp := rpc(t, f.srv, caller, "review.push", map[string]any{"session": fixer.id}); resp.Error == nil {
			t.Fatalf("review.push from %q succeeded", caller)
		}
	}
	if got := gitIn(t, f.upstream, "rev-parse", "feat/parser"); got != f.head {
		t.Fatalf("upstream moved to %s", got)
	}
}

func TestReviewPushRefusals(t *testing.T) {
	cases := []struct {
		name     string
		findings []string
		prepare  func(t *testing.T, f *fixRig, fixer startedFixer)
		code     string
	}{
		{
			name: "no commits",
			prepare: func(t *testing.T, _ *fixRig, fixer startedFixer) {
				writeFixReport(t, fixer.work, fixGoodReport)
			},
			code: "review_fix_incomplete",
		},
		{
			name: "dirty tree",
			prepare: func(t *testing.T, _ *fixRig, fixer startedFixer) {
				commitInTree(t, fixer.tree, map[string]string{"pkg/a.go": "package pkg // fixed\n"})
				writeFixReport(t, fixer.work, fixGoodReport)
				if err := os.WriteFile(filepath.Join(fixer.tree, "pkg", "a.go"), []byte("package pkg // more\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			code: "review_fix_incomplete",
		},
		{
			name: "no report",
			prepare: func(t *testing.T, _ *fixRig, fixer startedFixer) {
				commitInTree(t, fixer.tree, map[string]string{"pkg/a.go": "package pkg // fixed\n"})
			},
			code: "review_fix_incomplete",
		},
		{
			name: "report misses a finding",
			prepare: func(t *testing.T, _ *fixRig, fixer startedFixer) {
				commitInTree(t, fixer.tree, map[string]string{"pkg/a.go": "package pkg // fixed\n"})
				writeFixReport(t, fixer.work, "## Findings\n\nF1 fixed yes\n\n## Surprises\n\nnone\n")
			},
			code: "review_fix_incomplete",
		},
		{
			name: "history rewritten past the start",
			prepare: func(t *testing.T, _ *fixRig, fixer startedFixer) {
				gitIn(t, fixer.tree, "reset", "-q", "--hard", "HEAD~1")
				commitInTree(t, fixer.tree, map[string]string{"pkg/a.go": "package pkg // fixed\n"})
				writeFixReport(t, fixer.work, fixGoodReport)
			},
			code: "review_fix_incomplete",
		},
		{
			name: "a path outside the findings",
			prepare: func(t *testing.T, _ *fixRig, fixer startedFixer) {
				commitInTree(t, fixer.tree, map[string]string{"pkg/a.go": "package pkg // fixed\n", "other/c.go": "package other // sneaked\n"})
				writeFixReport(t, fixer.work, fixGoodReport)
			},
			code: "review_fix_scope",
		},
		{
			name: "a subdirectory of a finding's directory",
			prepare: func(t *testing.T, _ *fixRig, fixer startedFixer) {
				commitInTree(t, fixer.tree, map[string]string{"pkg/sub/x.go": "package sub\n"})
				writeFixReport(t, fixer.work, fixGoodReport)
			},
			code: "review_fix_scope",
		},
		{
			name: "a rename out of scope",
			prepare: func(t *testing.T, _ *fixRig, fixer startedFixer) {
				gitIn(t, fixer.tree, "mv", "other/c.go", "pkg/c.go")
				commitInTree(t, fixer.tree, map[string]string{})
				writeFixReport(t, fixer.work, fixGoodReport)
			},
			code: "review_fix_scope",
		},
		{
			name: "a workflow",
			prepare: func(t *testing.T, _ *fixRig, fixer startedFixer) {
				commitInTree(t, fixer.tree, map[string]string{".github/workflows/ci.yml": "on: pull_request_target\n"})
				writeFixReport(t, fixer.work, fixGoodReport)
			},
			code: "review_fix_scope",
		},
		{
			name:     "a finding with no path grants no scope",
			findings: []string{"F3"},
			prepare: func(t *testing.T, _ *fixRig, fixer startedFixer) {
				commitInTree(t, fixer.tree, map[string]string{"main.txt": "changed\n"})
				writeFixReport(t, fixer.work, "## Findings\n\nF3 fixed yes\n\n## Surprises\n\nnone\n")
			},
			code: "review_fix_scope",
		},
		{
			name: "GitHub's head moved",
			prepare: func(t *testing.T, f *fixRig, fixer startedFixer) {
				commitInTree(t, fixer.tree, map[string]string{"pkg/a.go": "package pkg // fixed\n"})
				writeFixReport(t, fixer.work, fixGoodReport)
				pr := f.gh.prs[7]
				pr.HeadSHA = "0000"
				f.gh.prs[7] = pr
			},
			code: "review_head_moved",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixRig(t)
			f.revealSingle(t)
			fixer := f.startFix(t, tc.findings...)
			tc.prepare(t, f, fixer)
			wantCode(t, rpc(t, f.srv, fixer.id, "review.push", map[string]any{}), tc.code)
			if got := gitIn(t, f.upstream, "rev-parse", "feat/parser"); got != f.head {
				t.Fatalf("upstream moved to %s", got)
			}
			if _, err := f.srv.repo.Get(context.Background(), fixer.id); err != nil {
				t.Fatalf("a refusal must leave the fixer: %v", err)
			}
			if _, err := os.Stat(fixer.tree); err != nil {
				t.Fatalf("a refusal must leave the tree: %v", err)
			}
		})
	}
}

// A commit from anyone else that GitHub has not reported yet makes the plain
// push fail; it is never forced.
func TestReviewPushNeverForces(t *testing.T) {
	f := newFixRig(t)
	f.revealSingle(t)
	fixer := f.startFix(t)
	commitInTree(t, fixer.tree, map[string]string{"pkg/a.go": "package pkg // fixed\n"})
	writeFixReport(t, fixer.work, fixGoodReport)

	gitIn(t, f.upstream, "checkout", "-q", "feat/parser")
	commitPath(t, f.upstream, "pkg/theirs.go", "package pkg\n")
	theirs := gitIn(t, f.upstream, "rev-parse", "HEAD")
	gitIn(t, f.upstream, "checkout", "-q", "main")

	wantCode(t, rpc(t, f.srv, fixer.id, "review.push", map[string]any{}), "review_head_moved")
	if got := gitIn(t, f.upstream, "rev-parse", "feat/parser"); got != theirs {
		t.Fatalf("upstream at %s, want their commit %s", got, theirs)
	}
}

// An assigned finding's own workflow file may be changed.
func TestReviewPushAllowsAFindingsWorkflow(t *testing.T) {
	f := newFixRig(t)
	f.writeFile(t, strings.Replace(fixReviewBody, "## Summary", "### F4 blocking .github/workflows/ci.yml:1\nVerified: yes (read it)\nSource: primary\n\nUnsafe trigger.\n\n## Summary", 1))
	f.revealSingle(t)
	fixer := f.startFix(t, "F4")
	sha := commitInTree(t, fixer.tree, map[string]string{".github/workflows/ci.yml": "on: pull_request\n"})
	writeFixReport(t, fixer.work, "## Findings\n\nF4 fixed read it\n\n## Surprises\n\nnone\n")
	if got := f.push(t, fixer); got != sha {
		t.Fatalf("pushed %s", got)
	}
}

func TestReviewRefreshDeltaKeepsWhatStillHolds(t *testing.T) {
	f := newFixRig(t)
	f.revealSingle(t)
	before := f.head
	if err := f.srv.TodoAccess().Mutate(t.Context(), f.info.ID, domain.AuthorAiman, func(l *domain.TodoList) error {
		todo.AddGate(l, todo.ApproveGateRef)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	tickTodo(t, f.srv, f.info.ID, todo.ApproveGateRef)
	f.closeReviewGate(t)
	tickTodo(t, f.srv, f.info.ID, reviewInformedRef)
	tickTodo(t, f.srv, f.info.ID, reviewPostRef)
	st := f.state(t)
	st.Assessment = &review.AssessmentState{Consult: "c", HeadSHA: before}
	st.ApproveGate = &review.AssessmentState{Consult: "c", HeadSHA: before}
	st.ApprovedHead = before
	if err := review.SaveState(f.stateDir, f.info.ID, st); err != nil {
		t.Fatal(err)
	}
	sha := f.fixAndPush(t)

	var res struct {
		Refreshed bool `json:"refreshed"`
		Delta     bool `json:"delta"`
	}
	rpcOK(t, f.srv, f.info.ID, "review.refresh", map[string]any{}, &res)
	if !res.Refreshed || !res.Delta {
		t.Fatalf("refresh %+v, want a delta", res)
	}
	st = f.state(t)
	if !st.Revealed || !st.Single || st.HeadSHA != sha || len(st.Fixes) != 1 {
		t.Fatalf("state %+v", st)
	}
	if st.Assessment != nil || st.ApproveGate != nil || st.ApprovedHead != "" {
		t.Fatalf("assessment %+v gate %+v approved %q survive a refresh", st.Assessment, st.ApproveGate, st.ApprovedHead)
	}
	if _, err := os.Stat(filepath.Join(f.scratch, review.DescriptionFileName)); err != nil {
		t.Fatalf("description withdrawn on a delta: %v", err)
	}
	patch, err := os.ReadFile(filepath.Join(f.scratch, reviewFilesDir, "fix.patch"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(patch), "// fixed") || strings.Contains(string(patch), "on: push") {
		t.Fatalf("fix.patch holds more or less than the fix:\n%s", patch)
	}
	states := todoStates(t, f.srv, f.info.ID)
	if states[reviewBlindRef] != domain.TodoDone {
		t.Errorf("blind %s, want done", states[reviewBlindRef])
	}
	for _, ref := range []string{reviewInformedRef, reviewPostRef, todo.ReviewGateRef, todo.ApproveGateRef} {
		if states[ref] != domain.TodoPending {
			t.Errorf("%s is %s, want pending", ref, states[ref])
		}
	}
	p := ledgerPayload(t, f.srv, f.info.ID, domain.EventReviewRefreshed)
	if p == nil || p["delta"] != true || p["to"] != sha {
		t.Fatalf("review.refreshed %v", p)
	}
}

func TestReviewRefreshDeltaRetiresConsults(t *testing.T) {
	f := newFixRig(t)
	f.revealSingle(t)
	f.fixAndPush(t)
	startConsult(t, f.srv, f.info.ID, "c-running")
	rpcOK(t, f.srv, f.info.ID, "review.refresh", map[string]any{}, nil)
	if _, err := f.srv.repo.Get(context.Background(), "c-running"); err == nil {
		t.Fatal("a running consult survives a delta refresh")
	}
}

func TestReviewRefreshDeltaForeignPushIsFull(t *testing.T) {
	f := newFixRig(t)
	f.revealSingle(t)
	sha := f.fixAndPush(t)
	gitIn(t, f.upstream, "checkout", "-q", "feat/parser")
	if got := gitIn(t, f.upstream, "rev-parse", "HEAD"); got != sha {
		t.Fatalf("upstream branch at %s", got)
	}
	commitPath(t, f.upstream, "pkg/theirs.go", "package pkg\n")
	theirs := gitIn(t, f.upstream, "rev-parse", "HEAD")
	gitIn(t, f.upstream, "checkout", "-q", "main")
	f.land(t, theirs)

	var res struct {
		Delta bool `json:"delta"`
	}
	rpcOK(t, f.srv, f.info.ID, "review.refresh", map[string]any{}, &res)
	st := f.state(t)
	if res.Delta || st.Revealed || len(st.Fixes) != 1 {
		t.Fatalf("delta %v state %+v, want a full refresh keeping the rounds", res.Delta, st)
	}
	if got := todoStates(t, f.srv, f.info.ID)[reviewBlindRef]; got != domain.TodoPending {
		t.Fatalf("blind %s, want pending", got)
	}
	if _, err := os.Stat(filepath.Join(f.scratch, review.DescriptionFileName)); !os.IsNotExist(err) {
		t.Fatalf("description left after a full refresh: %v", err)
	}
}

func TestReviewRefreshDeltaLargeFixIsFull(t *testing.T) {
	f := newFixRig(t)
	f.revealSingle(t)
	fixer := f.startFix(t)
	commitInTree(t, fixer.tree, map[string]string{"pkg/big.go": strings.Repeat("// line\n", 401)})
	writeFixReport(t, fixer.work, fixGoodReport)
	f.land(t, f.push(t, fixer))

	var res struct {
		Delta bool `json:"delta"`
	}
	rpcOK(t, f.srv, f.info.ID, "review.refresh", map[string]any{}, &res)
	st := f.state(t)
	if res.Delta || st.Revealed || len(st.Fixes) != 1 {
		t.Fatalf("delta %v state %+v, want a full refresh keeping the rounds", res.Delta, st)
	}
}

// fixedBody marks F1 fixed at sha and completes the rest of the file: F2 is
// verified, and F3 gets a path and a call, since a posted finding needs both.
func fixedBody(sha string) string {
	body := strings.Replace(fixReviewBody, "Verified: yes (go test fails)\n", "Verified: yes (go test fails)\nFixed: "+sha+"\n", 1)
	body = strings.Replace(body, "Verified: no\nSource: secondary\n", "Verified: yes (read it)\nSource: secondary\n", 1)
	body = strings.Replace(body, "### F3 question\n", "### F3 question pkg/a.go:1\n", 1)
	return strings.Replace(body, "Source: both\n", "Source: both\nCall: agree \"ask it\"\n", 1)
}

func TestReviewRefreshDeltaPostNeedsAConsult(t *testing.T) {
	f := newFixRig(t)
	f.revealWithConsult(t)
	sha := f.fixAndPush(t)
	rpcOK(t, f.srv, f.info.ID, "review.refresh", map[string]any{}, nil)
	f.closeReviewGate(t)
	f.writeFile(t, fixedBody(sha))

	wantCode(t, rpc(t, f.srv, f.info.ID, "review.post", map[string]any{"file": fixReviewFile}), "review_not_ready")

	startConsult(t, f.srv, f.info.ID, "c-after")
	endConsult(t, f.srv, f.info.ID, "c-after", domain.EventConsultFinished)
	rpcOK(t, f.srv, f.info.ID, "review.post", map[string]any{"file": fixReviewFile}, nil)
	post := f.gh.posts[len(f.gh.posts)-1]
	if post.CommitID != sha {
		t.Fatalf("posted at %s, want %s", post.CommitID, sha)
	}
	if !strings.Contains(post.Body, fmt.Sprintf("Fixed in %s: F1 pkg/a.go", sha)) {
		t.Fatalf("body %q", post.Body)
	}
	if slices.ContainsFunc(post.Comments, func(c review.Comment) bool { return c.Path == "pkg/a.go" }) {
		t.Fatalf("a fixed finding got an inline comment: %+v", post.Comments)
	}
}

func TestReviewRefreshDeltaPostSingleNeedsNoConsult(t *testing.T) {
	f := newFixRig(t)
	f.revealSingle(t)
	sha := f.fixAndPush(t)
	rpcOK(t, f.srv, f.info.ID, "review.refresh", map[string]any{}, nil)
	f.closeReviewGate(t)
	f.writeFile(t, fixedBody(sha))
	rpcOK(t, f.srv, f.info.ID, "review.post", map[string]any{"file": fixReviewFile}, nil)
}

// A Fixed line nobody recorded takes nothing off the list: F1 still needs its
// call, so the post is refused.
func TestReviewFixFalseFixedLineIsRefused(t *testing.T) {
	f := newFixRig(t)
	f.revealSingle(t)
	f.closeReviewGate(t)
	f.writeFile(t, fixedBody(f.head))
	wantCode(t, rpc(t, f.srv, f.info.ID, "review.post", map[string]any{"file": fixReviewFile}), CodeReviewIncomplete)
}
