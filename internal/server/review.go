package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/local"
	"github.com/bouwerp/aiman/internal/review"
)

// reviewFilesDir is the directory serve writes a review's inputs into, inside
// the review's scratch directory. It matches the .aiman_* exclude glob.
const reviewFilesDir = ".aiman_review"

// Refs of the review to-dos the later review steps find by ref.
const (
	reviewBlindRef    = "review:blind"
	reviewInformedRef = "review:informed"
	reviewPostRef     = "review:post"
)

// reviewTodos are the to-dos every review session starts with, in order.
var reviewTodos = []domain.TodoItem{
	{Text: "Blind review", Ref: reviewBlindRef},
	{Text: "Informed pass", Ref: reviewInformedRef},
	{Text: "Verify findings"},
	{Text: "Publish the brief"},
	{Text: "Ask the three questions"},
	{Text: "Post the review", Ref: reviewPostRef},
}

// ReviewWorktrees makes the detached, staged worktree of a pull request.
type ReviewWorktrees interface {
	SetupRemotePRWorktree(ctx context.Context, remote domain.RemoteExecutor, repo domain.Repo, number int, base string) (domain.Worktree, error)
}

// SetReviewGitHub sets the GitHub client review sessions read PRs through.
// Review creates fail until it is set.
func (s *Server) SetReviewGitHub(gh review.GitHub) {
	s.reviewGH = gh
}

// SetReviewWorktrees sets what makes review worktrees. Review creates fail
// until it is set.
func (s *Server) SetReviewWorktrees(w ReviewWorktrees) {
	s.reviewWT = w
}

// SetReviewDir sets the directory holding each review's state file
// (<id>.json) and scratch directory (<id>/work). Empty is ~/.aiman/pr-reviews.
func (s *Server) SetReviewDir(dir string) {
	s.reviewDir = dir
}

// reviewBase is the directory holding every review's state and scratch
// directory.
func (s *Server) reviewBase() string {
	if s.reviewDir != "" {
		return s.reviewDir
	}
	home := hostHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, config.DirName, "pr-reviews")
}

// reviewLockKey is ref lower-cased: GitHub owner and repository names are
// case-insensitive, so O/R#7 and o/r#7 are one review.
func reviewLockKey(ref review.Ref) string {
	return strings.ToLower(ref.String())
}

// lockReview serialises review creates of one PR, so the live-review check
// and the save of the new row cannot interleave with another create's.
func (s *Server) lockReview(ref string) func() {
	v, _ := s.reviewLocks.LoadOrStore(ref, &sync.Mutex{})
	mu, _ := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// reviewMeta is .aiman_review/meta.json. It leaves out the description on
// purpose: the first pass is blind to it. Worktree is the PR checkout, which
// the agent reads by path.
type reviewMeta struct {
	Ref      string `json:"ref"`
	Number   int    `json:"number"`
	Base     string `json:"base"`
	HeadSHA  string `json:"head_sha"`
	Title    string `json:"title"`
	Author   string `json:"author"`
	Worktree string `json:"worktree"`
}

// reviewOnlyParams are create parameters a review sets itself.
var reviewOnlyParams = []struct {
	name string
	set  func(createParams) bool
}{
	{"consult", func(p createParams) bool { return p.Consult }},
	{"quick", func(p createParams) bool { return p.Quick }},
	{"for", func(p createParams) bool { return strings.TrimSpace(p.For) != "" }},
	{"repo", func(p createParams) bool { return strings.TrimSpace(p.Repo) != "" }},
	{"branch", func(p createParams) bool { return strings.TrimSpace(p.Branch) != "" }},
	{"dir", func(p createParams) bool { return strings.TrimSpace(p.Dir) != "" }},
	{"prompt", func(p createParams) bool { return strings.TrimSpace(p.Prompt) != "" }},
	{"issue", func(p createParams) bool { return strings.TrimSpace(p.Issue) != "" }},
	{"brief", func(p createParams) bool { return strings.TrimSpace(p.Brief) != "" }},
	{"brief_file", func(p createParams) bool { return strings.TrimSpace(p.BriefFile) != "" }},
}

// handleReview starts an interactive session reviewing p.Review. The PR is
// checked out at <repo>@pr-<n>, staged against its merge base, for the
// operator's editor. The agent runs in an aiman-owned scratch directory
// holding the diff, the changed files and the PR's metadata, and reads the
// PR worktree by path, so it never loads project config from the PR. The
// agent comes from the smart tier unless one is named.
func (s *Server) handleReview(ctx context.Context, req Request, p createParams, parentID string) Response {
	ref, err := review.ParseRef(strings.TrimSpace(p.Review))
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	for _, rp := range reviewOnlyParams {
		if rp.set(p) {
			return errResp(req.ID, CodeInvalidParams, "a review takes no "+rp.name)
		}
	}
	if s.reviewGH == nil || s.reviewWT == nil || s.create == nil || s.reviewBase() == "" {
		return errResp(req.ID, CodeCreateFailed, "review not configured")
	}
	defer s.lockReview(reviewLockKey(ref))()
	// Read under the lock, so a create that saved meanwhile is seen.
	existing, err := s.loadSessions(ctx)
	if err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	if other, ok := liveReview(existing, ref.String()); ok {
		return errResp(req.ID, CodeReviewExists, fmt.Sprintf("session %s already reviews %s", other.Name, ref))
	}
	name, code, err := reviewName(p.Name, existing, ref)
	if err != nil {
		return errResp(req.ID, code, err.Error())
	}
	launch, code, err := s.launchFor(ctx, p, config.TierSmart)
	if err != nil {
		return errResp(req.ID, code, err.Error())
	}
	plan := reviewPlan{ref: ref, name: name, parentID: parentID, group: p.Group, budget: p.BudgetTokens, launch: launch}
	return s.startReview(ctx, req, plan, existing)
}

// reviewPlan is a validated review create.
type reviewPlan struct {
	ref                   review.Ref
	name, parentID, group string
	budget                int64
	launch                launchPick
}

// startReview makes the PR worktree and the scratch directory, starts the
// agent and stores the session's row, state and to-dos.
func (s *Server) startReview(ctx context.Context, req Request, plan reviewPlan, existing []domain.Session) Response {
	pr, err := s.reviewGH.PR(ctx, plan.ref)
	if err != nil {
		return errResp(req.ID, CodeCreateFailed, fmt.Sprintf("read %s: %v", plan.ref, err))
	}
	remote := s.reviewRemote(req.Caller, existing)
	if remote == nil {
		return errResp(req.ID, CodeCreateFailed, "no remote to make the review worktree on")
	}
	repo := domain.Repo{Name: plan.ref.Slug()}
	wt, err := s.reviewWT.SetupRemotePRWorktree(ctx, remote, repo, plan.ref.Number, pr.BaseRef)
	if errors.Is(err, domain.ErrOriginMismatch) {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	if err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	wt.Path = filepath.Clean(wt.Path)
	id := uuid.NewString()
	meta := reviewMeta{
		Ref: plan.ref.String(), Number: plan.ref.Number, Base: pr.BaseRef, HeadSHA: stagedHead(ctx, remote, wt.Path, pr.HeadSHA),
		Title: pr.Title, Author: pr.Author, Worktree: wt.Path,
	}
	scratch, err := s.prepareReviewScratch(ctx, remote, id, meta)
	if err != nil {
		s.removeReviewFiles(id)
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	sess, err := s.launchReview(ctx, plan, id, scratch, wt.Path, pr.BaseRef, remote)
	if err != nil {
		s.removeReviewFiles(id)
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	if err := s.storeReview(ctx, &sess, meta); err != nil {
		s.abandonReview(ctx, sess)
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	s.appendEvent(ctx, sess.ID, domain.EventActorAiman, domain.EventSessionCreated, map[string]any{
		"name": sess.Name, "group": sess.Group, "repo": repo.Name, "review": plan.ref.String(),
		"agent": plan.launch.agent, "tier": plan.launch.tier, "requested_tier": plan.launch.requested,
		"model": plan.launch.model, "effort": plan.launch.effort, "tier_fallback": plan.launch.fellBack,
	})
	s.appendEvent(ctx, sess.ID, domain.EventActorAiman, domain.EventReviewStarted, map[string]any{
		"ref": plan.ref.String(), "base": meta.Base, "head_sha": meta.HeadSHA,
	})
	return Response{ID: req.ID, Result: map[string]any{
		"type": "session", "session": sessionInfo(sess, req.Caller),
		"tier_fallback": plan.launch.fellBack, "requested_tier": plan.launch.requested,
	}}
}

// launchReview creates the review session with id in scratch, and returns
// its row with WorktreePath naming the PR worktree.
func (s *Server) launchReview(ctx context.Context, plan reviewPlan, id, scratch, worktree, base string, remote domain.RemoteExecutor) (domain.Session, error) {
	agentName := plan.launch.agentName
	if strings.TrimSpace(agentName) == "" {
		agentName = plan.launch.agent
	}
	repo := domain.Repo{Name: plan.ref.Slug()}
	group := domain.AssignSessionGroup(plan.group, "", repo.Name, false)
	cfg := domain.SessionConfig{
		SessionID:     id,
		Name:          plan.name,
		Group:         group,
		ParentID:      plan.parentID,
		PromptFree:    true,
		InitialPrompt: review.Prompt,
		BaseBranch:    base,
		Agent: &domain.Agent{
			Name: agentName, Command: plan.launch.agent,
			LaunchModel: plan.launch.model, LaunchEffort: plan.launch.effort,
		},
		Tier:         plan.launch.tier,
		BudgetTokens: plan.budget,
		Repo:         repo,
		// The agent's cwd, and the only directory create trusts, is the
		// scratch directory; the PR worktree is never handed to the creator.
		PreparedWorktree: scratch,
		ReviewPR:         plan.ref.String(),
		SSHManager:       remote,
	}
	created, err := s.create.CreateSession(ctx, cfg)
	if err != nil {
		return domain.Session{}, err
	}
	sess := *created
	sess.Name, sess.Group, sess.ParentID, sess.Tier, sess.ReviewPR = plan.name, group, plan.parentID, plan.launch.tier, plan.ref.String()
	sess.WorktreePath, sess.WorkingDirectory = worktree, scratch
	if sess.LaunchModel == "" {
		sess.LaunchModel = plan.launch.model
	}
	if sess.LaunchEffort == "" {
		sess.LaunchEffort = plan.launch.effort
	}
	return sess, nil
}

// storeReview saves the row, the review state and the review to-dos.
func (s *Server) storeReview(ctx context.Context, sess *domain.Session, meta reviewMeta) error {
	if s.repo != nil {
		if err := s.repo.Save(ctx, sess); err != nil {
			return err
		}
	}
	if err := review.SaveState(s.reviewBase(), sess.ID, review.State{Ref: meta.Ref, HeadSHA: meta.HeadSHA}); err != nil {
		return err
	}
	return s.TodoAccess().Mutate(ctx, sess.ID, domain.AuthorAiman, func(l *domain.TodoList) error {
		for _, it := range reviewTodos {
			it.State = domain.TodoPending
			l.Items = append(l.Items, it)
		}
		return nil
	})
}

// abandonReview stops and forgets a review session whose records could not
// be stored, and removes its scratch directory and state file. The PR
// worktree stays for a retry to reuse.
func (s *Server) abandonReview(ctx context.Context, sess domain.Session) {
	if err := (serverTerminal{s}).Stop(ctx, sess); err != nil {
		log.Printf("review: stop %s: %v", sess.ID, err)
	}
	if err := s.dropSession(ctx, sess); err != nil {
		log.Printf("review: forget %s: %v", sess.ID, err)
	}
	s.removeReviewFiles(sess.ID)
}

// removeReviewFiles removes review id's scratch directory and state file.
func (s *Server) removeReviewFiles(id string) {
	base := s.reviewBase()
	if base == "" || id == "" || id != filepath.Base(id) {
		return
	}
	for _, path := range []string{filepath.Join(base, id), filepath.Join(base, id+".json")} {
		if err := os.RemoveAll(path); err != nil {
			log.Printf("review: remove %s: %v", path, err)
		}
	}
}

// liveReview finds a session reviewing ref, in any letter case, that has not
// ended.
func liveReview(existing []domain.Session, ref string) (domain.Session, bool) {
	for _, sess := range existing {
		if strings.EqualFold(sess.ReviewPR, ref) && liveForUpdate(sess) {
			return sess, true
		}
	}
	return domain.Session{}, false
}

// reviewName is the requested name, validated, or pr-<n> made unique.
func reviewName(requested string, existing []domain.Session, ref review.Ref) (string, string, error) {
	if requested != "" {
		if err := domain.ValidateSessionName(requested); err != nil {
			return "", CodeInvalidParams, err
		}
		if domain.NameTaken(existing, requested) {
			return "", CodeNameTaken, errors.New("name already in use")
		}
		return requested, "", nil
	}
	name, err := domain.AssignSessionName(existing, fmt.Sprintf("pr-%d", ref.Number), false)
	if err != nil {
		return "", CodeInvalidParams, err
	}
	return name, "", nil
}

// reviewRemote is the executor the review worktree is made with: the git
// root other sessions on this host use, else serve's own remote.
func (s *Server) reviewRemote(caller string, existing []domain.Session) domain.RemoteExecutor {
	var callerSess *domain.Session
	if found, ok := resolveSession(existing, caller); ok {
		callerSess = &found
	}
	if root := createGitRoot(callerSess, existing, hostHome()); root != "" {
		return local.NewExecutor(root)
	}
	return s.remote
}

// stagedHead is the commit the soft reset left the worktree's files at
// (ORIG_HEAD), falling back to the head GitHub reported. The fetch can see a
// newer head than the PR read a moment earlier.
func stagedHead(ctx context.Context, remote domain.RemoteExecutor, worktree, reported string) string {
	out, err := remote.Execute(ctx, fmt.Sprintf("git -C %s rev-parse --verify -q ORIG_HEAD", quoteArg(worktree)))
	if sha := strings.TrimSpace(out); err == nil && isObjectID(sha) {
		return sha
	}
	return reported
}

// isObjectID reports whether s is a full SHA-1 or SHA-256 object id.
func isObjectID(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	return strings.Trim(s, "0123456789abcdef") == ""
}

// reviewScratch is review id's scratch directory, the agent's working
// directory.
func (s *Server) reviewScratch(id string) string {
	return filepath.Join(s.reviewBase(), id, "work")
}

// prepareReviewScratch makes <base>/<id>/work a fresh git repository (agents
// that need one are content) and writes .aiman_review into it.
func (s *Server) prepareReviewScratch(ctx context.Context, remote domain.RemoteExecutor, id string, meta reviewMeta) (string, error) {
	scratch := s.reviewScratch(id)
	if err := os.MkdirAll(filepath.Join(scratch, reviewFilesDir), 0o700); err != nil {
		return "", err
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", scratch, "init", "-q").CombinedOutput(); err != nil { //nolint:gosec // scratch is under the review base with a generated id
		return "", fmt.Errorf("git init %s: %w: %s", scratch, err, strings.TrimSpace(string(out)))
	}
	if err := writeReviewFiles(ctx, remote, scratch, meta); err != nil {
		return "", err
	}
	return scratch, nil
}

// writeReviewFiles writes scratch/.aiman_review: diff.patch (the PR
// worktree's staged diff), files.txt (its numstat) and meta.json.
func writeReviewFiles(ctx context.Context, remote domain.RemoteExecutor, scratch string, meta reviewMeta) error {
	wt := quoteArg(meta.Worktree)
	diff, err := remote.Execute(ctx, "git -C "+wt+" diff --cached")
	if err != nil {
		return fmt.Errorf("diff the PR: %w", err)
	}
	numstat, err := remote.Execute(ctx, "git -C "+wt+" diff --cached --numstat")
	if err != nil {
		return fmt.Errorf("list the PR's files: %w", err)
	}
	rawMeta, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Join(scratch, reviewFilesDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for name, body := range map[string][]byte{
		"diff.patch": []byte(diff),
		"files.txt":  []byte(numstat),
		"meta.json":  append(rawMeta, '\n'),
	} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
			return err
		}
	}
	return nil
}
