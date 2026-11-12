package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/cigate"
	"github.com/bouwerp/aiman/internal/ciwatch"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/verify"
)

const (
	// CodeCIRunning refuses ci.local while a run for the session is going.
	CodeCIRunning = "ci_running"
	// CodeCIDirty refuses ci.local while the worktree has uncommitted or
	// untracked files.
	CodeCIDirty = "ci_dirty"
	// CodeCINoList refuses ci.local for a repository with no gate list.
	CodeCINoList = "ci_no_list"
	// CodeCINotReady refuses ci.ready until the PR may go ready.
	CodeCINotReady = "ci_not_ready"
)

const (
	// ciLocalWait is how long ci.local {wait:true} holds the call before it
	// answers with the running record; aiman ci local --wait then polls
	// ci.status.
	ciLocalWait = 20 * time.Second
	// prLookupBudget bounds serve's own gh pr view on a cache miss.
	prLookupBudget = 3 * time.Second
	// prCacheTTL is how long a gh answer about a branch's PR stands.
	prCacheTTL = 30 * time.Second
	// ciStatusTailLines is how much of a failed command's log ci.status sends.
	ciStatusTailLines = 60
	// prReadyBudget bounds ci.ready's own gh calls.
	prReadyBudget = 20 * time.Second
)

// MarkReady marks PR number ready for review from the checkout dir.
type MarkReady func(ctx context.Context, dir string, number int) error

// PRLookup reads the PR of branch from the worktree dir. No PR is nil, nil.
type PRLookup func(ctx context.Context, dir, branch string) (*cigate.PR, error)

// CILocalDeps is what the local CI gate methods need.
type CILocalDeps struct {
	Runner *cigate.Runner
	// Repos is the repos section of serve's config: copy files, ci.local
	// overrides and drafts_skip_ci.
	Repos map[string]config.RepoBootstrap
	// ChildPRs is coordinator.child_prs.
	ChildPRs bool
	// LookupPR asks GitHub; nil uses gh pr view.
	LookupPR PRLookup
	// MarkReady takes a PR out of draft; nil runs gh pr ready.
	MarkReady MarkReady
}

type ciLocalState struct {
	d     CILocalDeps
	cache *prCache
}

// SetCILocal enables ci.local, ci.status and ci.gate. Without it they answer
// unavailable.
func (s *Server) SetCILocal(d CILocalDeps) {
	if d.LookupPR == nil {
		d.LookupPR = GHPRLookup
	}
	if d.MarkReady == nil {
		d.MarkReady = GHMarkReady
	}
	s.ciLocal = &ciLocalState{d: d, cache: newPRCache(time.Now)}
}

// dispatchCI routes the ci.* methods: the PR watcher's and the local gate's.
func (s *Server) dispatchCI(ctx context.Context, req Request) Response {
	switch req.Method {
	case "ci.get":
		return s.handleCIGet(ctx, req)
	case "ci.watch":
		return s.handleCIWatch(ctx, req)
	default:
		return s.dispatchCILocal(ctx, req)
	}
}

func (s *Server) dispatchCILocal(ctx context.Context, req Request) Response {
	if s.ciLocal == nil || s.ciLocal.d.Runner == nil {
		return errResp(req.ID, CodeUnavailable, "the local CI gate is not enabled on this serve")
	}
	var p ciLocalParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	t, fail := s.ciLocalTarget(ctx, req, p.Session)
	if fail != nil {
		return *fail
	}
	switch req.Method {
	case "ci.local":
		return s.handleCILocal(ctx, req.ID, t, p.Wait)
	case "ci.status":
		return s.handleCIStatus(ctx, req.ID, t)
	case "ci.ready":
		return s.handleCIReady(ctx, req.ID, t)
	default:
		return s.handleCIGate(ctx, req.ID, t, p)
	}
}

type ciLocalParams struct {
	Session string `json:"session"`
	Wait    bool   `json:"wait"`
	Kind    string `json:"kind"`
	Command string `json:"command"`
	Draft   bool   `json:"draft"`
}

// ciTarget is a session and its checkout root.
type ciTarget struct {
	sess domain.Session
	top  string
	repo config.RepoBootstrap
}

func (s *Server) ciLocalTarget(ctx context.Context, req Request, name string) (ciTarget, *Response) {
	fail := func(code, msg string) (ciTarget, *Response) {
		r := errResp(req.ID, code, msg)
		return ciTarget{}, &r
	}
	if strings.TrimSpace(name) == "" {
		return fail(CodeInvalidParams, "session is required")
	}
	list, err := s.loadSessions(ctx)
	if err != nil {
		return fail(CodeInvalidParams, err.Error())
	}
	sess, ok := resolveSession(list, name)
	if !ok {
		return fail(CodeNotFound, "session not found")
	}
	if r, refused := s.refuseOutOfScope(ctx, req, sess.ID); refused {
		return ciTarget{}, &r
	}
	t, err := s.ciTargetFor(ctx, sess)
	if err != nil {
		return fail(CodeInvalidParams, err.Error())
	}
	return t, nil
}

// ciTargetFor finds the session's checkout root and repo settings.
func (s *Server) ciTargetFor(ctx context.Context, sess domain.Session) (ciTarget, error) {
	dir := sess.WorkingDirectory
	if dir == "" {
		dir = sess.WorktreePath
	}
	if dir == "" {
		return ciTarget{}, errors.New("session has no worktree")
	}
	top, err := gitLine(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return ciTarget{}, err
	}
	b, _ := config.LookupRepo(s.ciLocal.d.Repos, sess.RepoName)
	return ciTarget{sess: sess, top: top, repo: b}, nil
}

// CILandGate is session.land's view of the local gate; nil when the gate is
// not enabled.
func (s *Server) CILandGate() LandGate {
	if s.ciLocal == nil || s.ciLocal.d.Runner == nil {
		return nil
	}
	return ciLandGate{s: s}
}

type ciLandGate struct{ s *Server }

// PassedFor reports whether the child's repository has a gate list at sha
// and, if so, whether the record for sha passed.
func (g ciLandGate) PassedFor(ctx context.Context, child domain.Session, sha string) (applies, passed bool, err error) {
	t, err := g.s.ciTargetFor(ctx, child)
	if err != nil {
		return false, false, err
	}
	opt, err := t.options(0)
	if err != nil {
		return true, false, err
	}
	if _, has, lerr := cigate.LoadList(ctx, t.top, sha, opt.Override); lerr == nil && !has {
		return false, false, nil
	}
	rec, ok, err := g.s.ciLocal.d.Runner.Record(child.ID, sha)
	if err != nil {
		return true, false, err
	}
	return true, ok && rec.Status == cigate.StatusPass, nil
}

func (t ciTarget) options(nice int) (cigate.Options, error) {
	opt := cigate.Options{Copy: t.repo.Copy, Nice: nice}
	if len(t.repo.CI.Local) > 0 {
		raw := make([]cigate.RawCommand, 0, len(t.repo.CI.Local))
		for _, c := range t.repo.CI.Local {
			raw = append(raw, cigate.RawCommand{Name: c.Name, Run: c.Run, Dir: c.Dir, Timeout: c.Timeout})
		}
		l, err := cigate.BuildList("", raw)
		if err != nil {
			return opt, fmt.Errorf("repos.%s.ci.local: %w", t.sess.RepoName, err)
		}
		opt.Override = &l
	}
	return opt, nil
}

// handleCILocal starts a gate run for the session's HEAD. The run belongs to
// serve, not to the call: it goes on when the caller disconnects.
func (s *Server) handleCILocal(ctx context.Context, id string, t ciTarget, wait bool) Response {
	opt, err := t.options(int(s.agentNice.Load()))
	if err != nil {
		return errResp(id, CodeInvalidParams, err.Error())
	}
	pending, err := s.ciLocal.d.Runner.Prepare(ctx, t.sess.ID, t.top, opt)
	switch {
	case errors.Is(err, cigate.ErrRunning):
		return errResp(id, CodeCIRunning, err.Error())
	case errors.Is(err, cigate.ErrDirty):
		return errResp(id, CodeCIDirty, err.Error())
	case errors.Is(err, cigate.ErrNoList):
		return errResp(id, CodeCINoList, err.Error())
	case err != nil:
		return errResp(id, CodeInvalidParams, err.Error())
	}
	done := make(chan cigate.Record, 1)
	//nolint:gosec // G118: the run belongs to serve and outlives the call that started it
	go func() { done <- pending.Wait(context.Background()) }()
	rec, finished := pending.Record, false
	if wait {
		timer := time.NewTimer(ciLocalWait)
		defer timer.Stop()
		select {
		case rec = <-done:
			finished = true
		case <-timer.C:
		case <-ctx.Done():
		}
	}
	return Response{ID: id, Result: map[string]any{"type": "ci_local", "record": rec, "done": finished}}
}

// CIStatus is ci.status's answer: the record for HEAD, if any, and the tail
// of the failed command's log.
type CIStatus struct {
	SHA     string         `json:"sha"`
	Clean   bool           `json:"clean"`
	HasList bool           `json:"has_list"`
	Record  *cigate.Record `json:"record,omitempty"`
	Tail    string         `json:"tail,omitempty"`
}

func (s *Server) handleCIStatus(ctx context.Context, id string, t ciTarget) Response {
	st, err := s.ciStatus(ctx, t)
	if err != nil {
		return errResp(id, CodeInvalidParams, err.Error())
	}
	return Response{ID: id, Result: map[string]any{"type": "ci_status", "status": st}}
}

func (s *Server) ciStatus(ctx context.Context, t ciTarget) (CIStatus, error) {
	sha, err := gitLine(ctx, t.top, "rev-parse", "HEAD")
	if err != nil {
		return CIStatus{}, err
	}
	porcelain, err := gitLine(ctx, t.top, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return CIStatus{}, err
	}
	st := CIStatus{SHA: sha, Clean: porcelain == ""}
	opt, err := t.options(0)
	if err != nil {
		return st, err
	}
	_, st.HasList, err = cigate.LoadList(ctx, t.top, sha, opt.Override)
	if err != nil {
		// A broken list still gates: report it as present.
		st.HasList = true
	}
	rec, ok, err := s.ciLocal.d.Runner.Record(t.sess.ID, sha)
	if err != nil {
		return st, err
	}
	if ok {
		st.Record = &rec
		if rec.Status == cigate.StatusFail && len(rec.Commands) > 0 {
			last := rec.Commands[len(rec.Commands)-1]
			st.Tail, _ = cigate.Tail(last.Log, ciStatusTailLines)
		}
	}
	return st, nil
}

// handleCIGate answers whether a push, PR create or ready may go out for the
// session's HEAD. It never refuses the call itself: the answer is allow plus
// a reason.
func (s *Server) handleCIGate(ctx context.Context, id string, t ciTarget, p ciLocalParams) Response {
	kind := cigate.Kind(strings.TrimSpace(p.Kind))
	switch kind {
	case cigate.KindPush, cigate.KindCreate, cigate.KindReady:
	default:
		return errResp(id, CodeInvalidParams, "kind must be push, create or ready")
	}
	st, err := s.ciStatus(ctx, t)
	if err != nil {
		return errResp(id, CodeInvalidParams, err.Error())
	}
	branch, _ := gitLine(ctx, t.top, "symbolic-ref", "--short", "-q", "HEAD")
	in := cigate.Input{
		Kind:         kind,
		HasParent:    t.sess.ParentID != "",
		ChildPRs:     s.ciLocal.d.ChildPRs,
		HasList:      st.HasList,
		DraftsSkipCI: t.repo.CI.DraftsSkipCI,
		Draft:        p.Draft,
		Record:       st.Record,
	}
	key := prKey(t.sess.RepoName, t.top, branch)
	switch kind {
	case cigate.KindPush:
		if tag, ok := cigate.TagPushName(p.Command); ok && isTagOnly(ctx, t.top, tag) {
			// A release tag runs the release workflow, not the PR checks
			// this gate stands in for.
			return ciGateAnswer(id, true, "", st.SHA, nil)
		}
		if p.Command != "" && !cigate.PushCommandOK(p.Command, branch) {
			return ciGateAnswer(id, false, cigate.PushRefusal, st.SHA, nil)
		}
		if st.HasList {
			in.PR, in.LookupFailed = s.lookupPR(ctx, key, t.top, branch)
		}
	case cigate.KindCreate:
		// The PR this create opens is not on GitHub yet: forget what the
		// cache says so the next push asks again.
		s.ciLocal.cache.drop(key)
	}
	allow, reason := cigate.Decide(in)
	return ciGateAnswer(id, allow, reason, st.SHA, in.PR)
}

// isTagOnly reports whether name is a tag in dir and not also a branch.
func isTagOnly(ctx context.Context, dir, name string) bool {
	if _, err := gitLine(ctx, dir, "show-ref", "--verify", "refs/tags/"+name); err != nil {
		return false
	}
	_, err := gitLine(ctx, dir, "show-ref", "--verify", "refs/heads/"+name)
	return err != nil
}

func ciGateAnswer(id string, allow bool, reason, sha string, pr *cigate.PR) Response {
	res := map[string]any{"type": "ci_gate", "allow": allow, "sha": sha}
	if reason != "" {
		res["reason"] = reason
	}
	if pr != nil {
		res["pr"] = pr
	}
	return Response{ID: id, Result: res}
}

// lookupPR answers from the cache while its draft flag is fresh, else asks
// GitHub. When GitHub does not answer, a PR serve has seen counts as open and
// not draft (gated); no PR seen at all is a failed lookup.
func (s *Server) lookupPR(ctx context.Context, key, dir, branch string) (*cigate.PR, bool) {
	c := s.ciLocal.cache
	if pr, ok := c.fresh(key); ok {
		return pr, false
	}
	if branch == "" {
		return nil, true
	}
	lctx, cancel := context.WithTimeout(ctx, prLookupBudget)
	defer cancel()
	pr, err := s.ciLocal.d.LookupPR(lctx, dir, branch)
	if err == nil {
		c.put(key, pr)
		return pr, false
	}
	if seen, ok := c.seen(key); ok && seen != nil {
		gated := *seen
		gated.Draft = false
		return &gated, false
	}
	return nil, true
}

// PRCacheGitHub wraps the PR watcher's GitHub so each PR it reads updates
// serve's PR cache. ciwatch does not read the draft flag, so these entries
// only stand in when serve's own lookup fails.
func (s *Server) PRCacheGitHub(gh ciwatch.GitHub) ciwatch.GitHub {
	return prCacheGitHub{GitHub: gh, s: s}
}

type prCacheGitHub struct {
	ciwatch.GitHub
	s *Server
}

func (g prCacheGitHub) PRView(ctx context.Context, dir string) (ciwatch.PRView, error) {
	v, err := g.GitHub.PRView(ctx, dir)
	if g.s.ciLocal == nil || (err != nil && !errors.Is(err, ciwatch.ErrNoPR)) {
		return v, err
	}
	top, terr := gitLine(ctx, dir, "rev-parse", "--show-toplevel")
	branch, berr := gitLine(ctx, dir, "symbolic-ref", "--short", "-q", "HEAD")
	if terr != nil || berr != nil || branch == "" {
		return v, err
	}
	key := prKey(g.s.repoNameForDir(ctx, dir), top, branch)
	if errors.Is(err, ciwatch.ErrNoPR) {
		g.s.ciLocal.cache.observe(key, nil)
	} else {
		g.s.ciLocal.cache.observe(key, &cigate.PR{Number: v.Number, State: v.State, HeadSHA: v.HeadSHA})
	}
	return v, err
}

// repoNameForDir is the repo slug of the session whose worktree or working
// directory is dir, else empty.
func (s *Server) repoNameForDir(ctx context.Context, dir string) string {
	list, err := s.loadSessions(ctx)
	if err != nil {
		return ""
	}
	for _, sess := range list {
		if sess.WorkingDirectory == dir || sess.WorktreePath == dir {
			return sess.RepoName
		}
	}
	return ""
}

// prKey names a branch's PR: the repo slug, else the checkout root, plus the
// branch.
func prKey(repo, top, branch string) string {
	if repo = strings.ToLower(strings.TrimSpace(repo)); repo == "" {
		repo = top
	}
	return repo + "@" + branch
}

// handleCIReady marks the session's PR ready for review once nothing about
// HEAD would waste a CI run: a clean tree, HEAD pushed as the open PR's head,
// a pass record for HEAD when the repo has a gate list, and verify passed for
// HEAD when the host verifies before a PR. The PR comes from GitHub, never
// the cache; afterwards the cache knows the PR runs CI.
func (s *Server) handleCIReady(ctx context.Context, id string, t ciTarget) Response {
	rctx, cancel := context.WithTimeout(ctx, prReadyBudget)
	defer cancel()
	pr, err := s.ciReadyCheck(rctx, t)
	if err != nil {
		return errResp(id, CodeCINotReady, err.Error())
	}
	if err := s.ciLocal.d.MarkReady(rctx, t.top, pr.Number); err != nil {
		return errResp(id, CodeCINotReady, err.Error())
	}
	branch, _ := gitLine(ctx, t.top, "symbolic-ref", "--short", "-q", "HEAD")
	ready := *pr
	ready.Draft = false
	s.ciLocal.cache.put(prKey(t.sess.RepoName, t.top, branch), &ready)
	return Response{ID: id, Result: map[string]any{"type": "ci_ready", "ready": true, "number": pr.Number}}
}

// ciReadyCheck returns the session's open PR when it may go ready, else why
// not.
func (s *Server) ciReadyCheck(ctx context.Context, t ciTarget) (*cigate.PR, error) {
	st, err := s.ciStatus(ctx, t)
	if err != nil {
		return nil, err
	}
	if !st.Clean {
		return nil, errors.New("the worktree has uncommitted or untracked files; commit, push and run aiman ci local --wait first")
	}
	branch, err := gitLine(ctx, t.top, "symbolic-ref", "--short", "-q", "HEAD")
	if err != nil || branch == "" {
		return nil, errors.New("HEAD is not on a branch")
	}
	pr, err := s.ciLocal.d.LookupPR(ctx, t.top, branch)
	switch {
	case err != nil:
		return nil, fmt.Errorf("read the PR of %s: %w", branch, err)
	case pr == nil || !strings.EqualFold(pr.State, "OPEN"):
		return nil, fmt.Errorf("branch %s has no open PR; open it with gh pr create first", branch)
	case pr.HeadSHA != st.SHA:
		return nil, fmt.Errorf("PR #%d's head is %s, not HEAD %s; push first", pr.Number, shortSHA(pr.HeadSHA), shortSHA(st.SHA))
	}
	if st.HasList && (st.Record == nil || st.Record.Status != cigate.StatusPass) {
		return nil, fmt.Errorf("no passing local gate for %s; run aiman ci local --wait", shortSHA(st.SHA))
	}
	if err := s.verifiedHead(ctx, t); err != nil {
		return nil, err
	}
	return pr, nil
}

// verifiedHead applies the before-PR verify rule to the session's tree.
func (s *Server) verifiedHead(ctx context.Context, t ciTarget) error {
	if s.verifier == nil || !s.verifier.BeforePR() || t.sess.ReviewPR != "" || t.sess.FixPR != "" {
		return nil
	}
	tree, err := verify.TreeHash(ctx, t.top)
	if err != nil {
		return fmt.Errorf("hash the tree: %w", err)
	}
	if r, ok := s.verifier.ForTree(t.sess.ID, tree); !ok || !r.Passed() {
		return errors.New("verify has not passed for HEAD; run aiman verify --wait")
	}
	return nil
}

// forgetCILocal drops a session's gate records and logs.
func (s *Server) forgetCILocal(id string) {
	if s.ciLocal == nil || s.ciLocal.d.Runner == nil {
		return
	}
	if err := s.ciLocal.d.Runner.Forget(id); err != nil {
		log.Printf("ci local: forget %s: %v", id, err)
	}
}

// GHMarkReady runs gh pr ready number in dir.
func GHMarkReady(ctx context.Context, dir string, number int) error {
	cmd := exec.CommandContext(ctx, "gh", "pr", "ready", strconv.Itoa(number)) //nolint:gosec // G204: fixed gh subcommand and a PR number
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("gh pr ready %d: %w: %s", number, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// GHPRLookup runs gh pr view for branch in dir.
func GHPRLookup(ctx context.Context, dir, branch string) (*cigate.PR, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", branch, "--json", "number,isDraft,state,headRefOid")
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if strings.Contains(stderr.String(), "no pull requests found") {
			return nil, nil
		}
		return nil, fmt.Errorf("gh pr view: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var v struct {
		Number     int    `json:"number"`
		IsDraft    bool   `json:"isDraft"`
		State      string `json:"state"`
		HeadRefOid string `json:"headRefOid"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, fmt.Errorf("gh pr view: %w", err)
	}
	return &cigate.PR{Number: v.Number, Draft: v.IsDraft, State: v.State, HeadSHA: v.HeadRefOid}, nil
}

func gitLine(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...) //nolint:gosec // G204: fixed git subcommands; dir is a session's checkout
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", args[0], msg)
	}
	return strings.TrimSpace(string(out)), nil
}

// prCache is serve's record of each repo and branch's PR. An entry from
// GitHub with the draft flag (fresh) answers for prCacheTTL; an entry from
// the PR watcher (no draft flag) is only a fallback.
type prCache struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]prEntry
}

type prEntry struct {
	pr    *cigate.PR
	at    time.Time
	fresh bool
}

func newPRCache(now func() time.Time) *prCache {
	return &prCache{now: now, m: map[string]prEntry{}}
}

func (c *prCache) fresh(key string) (*cigate.PR, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || !e.fresh || c.now().Sub(e.at) >= prCacheTTL {
		return nil, false
	}
	return clonePR(e.pr), true
}

func (c *prCache) seen(key string) (*cigate.PR, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	return clonePR(e.pr), ok
}

func (c *prCache) put(key string, pr *cigate.PR) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = prEntry{pr: clonePR(pr), at: c.now(), fresh: true}
}

// observe records what the PR watcher saw. It keeps a fresh entry's draft
// flag for the same PR but never makes the entry fresh.
func (c *prCache) observe(key string, pr *cigate.PR) {
	c.mu.Lock()
	defer c.mu.Unlock()
	prev, ok := c.m[key]
	if ok && prev.fresh && prev.pr != nil && pr != nil && prev.pr.Number == pr.Number && prev.pr.State == pr.State {
		return
	}
	c.m[key] = prEntry{pr: clonePR(pr), at: c.now()}
}

func (c *prCache) drop(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, key)
}

func clonePR(pr *cigate.PR) *cigate.PR {
	if pr == nil {
		return nil
	}
	cp := *pr
	return &cp
}
