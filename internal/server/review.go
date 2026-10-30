package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/local"
	"github.com/bouwerp/aiman/internal/review"
)

// reviewFilesDir is the directory serve writes a review's inputs into, inside
// the review worktree. It matches the .aiman_* exclude glob.
const reviewFilesDir = ".aiman_review"

// Ledger refs of the review to-dos the later review steps find by ref.
const (
	reviewBlindRef    = "review:blind"
	reviewInformedRef = "review:informed"
)

// reviewTodos are the to-dos every review session starts with, in order.
var reviewTodos = []domain.TodoItem{
	{Text: "Blind review", Ref: reviewBlindRef},
	{Text: "Informed pass", Ref: reviewInformedRef},
	{Text: "Verify findings"},
	{Text: "Publish the brief"},
	{Text: "Ask the three questions"},
	{Text: "Post the review"},
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

// SetReviewDir sets the directory review state files are kept in. Empty is
// ~/.aiman/reviews.
func (s *Server) SetReviewDir(dir string) {
	s.reviewDir = dir
}

// reviewStateDir is the directory holding every review's state file.
func (s *Server) reviewStateDir() string {
	if s.reviewDir != "" {
		return s.reviewDir
	}
	home := hostHome()
	if home == "" {
		return ""
	}
	return filepath.Join(home, config.DirName, "reviews")
}

// reviewMeta is .aiman_review/meta.json. It leaves out the description on
// purpose: the first pass is blind to it.
type reviewMeta struct {
	Ref     string `json:"ref"`
	Number  int    `json:"number"`
	Base    string `json:"base"`
	HeadSHA string `json:"head_sha"`
	Title   string `json:"title"`
	Author  string `json:"author"`
}

// handleReview starts an interactive session reviewing params.Review: a
// worktree <repo>@pr-<n> at the PR head, staged against its merge base, with
// the diff, the changed files and the PR's metadata written for the agent.
// The agent comes from the smart tier unless one is named.
func (s *Server) handleReview(ctx context.Context, req Request, p createParams, parentID string, existing []domain.Session) Response {
	ref, err := review.ParseRef(strings.TrimSpace(p.Review))
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	if p.Consult || p.Quick || strings.TrimSpace(p.For) != "" {
		return errResp(req.ID, CodeInvalidParams, "a review takes none of consult, quick and for")
	}
	if s.reviewGH == nil || s.reviewWT == nil || s.create == nil {
		return errResp(req.ID, CodeCreateFailed, "review not configured")
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
	pr, err := s.reviewGH.PR(ctx, ref)
	if err != nil {
		return errResp(req.ID, CodeCreateFailed, fmt.Sprintf("read %s: %v", ref, err))
	}
	remote := s.reviewRemote(req.Caller, existing)
	if remote == nil {
		return errResp(req.ID, CodeCreateFailed, "no remote to make the review worktree on")
	}
	repo := domain.Repo{Name: ref.Slug()}
	wt, err := s.reviewWT.SetupRemotePRWorktree(ctx, remote, repo, ref.Number, pr.BaseRef)
	if err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	wt.Path = filepath.Clean(wt.Path)
	meta := reviewMeta{Ref: ref.String(), Number: ref.Number, Base: pr.BaseRef, HeadSHA: stagedHead(ctx, remote, wt.Path, pr.HeadSHA), Title: pr.Title, Author: pr.Author}
	if err := writeReviewFiles(ctx, remote, wt.Path, meta); err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	group := domain.AssignSessionGroup(p.Group, "", repo.Name, false)
	agentName := launch.agentName
	if strings.TrimSpace(agentName) == "" {
		agentName = launch.agent
	}
	cfg := domain.SessionConfig{
		Name:             name,
		Group:            group,
		ParentID:         parentID,
		PromptFree:       true,
		InitialPrompt:    review.Prompt,
		BaseBranch:       pr.BaseRef,
		Agent:            &domain.Agent{Name: agentName, Command: launch.agent, LaunchModel: launch.model, LaunchEffort: launch.effort},
		Tier:             launch.tier,
		BudgetTokens:     p.BudgetTokens,
		Repo:             repo,
		PreparedWorktree: wt.Path,
		ReviewPR:         ref.String(),
		SSHManager:       remote,
	}
	created, err := s.create.CreateSession(ctx, cfg)
	if err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	sess := *created
	sess.Name, sess.Group, sess.ParentID, sess.Tier, sess.ReviewPR = name, group, parentID, launch.tier, ref.String()
	sess.WorktreePath, sess.WorkingDirectory = wt.Path, wt.Path
	if sess.LaunchModel == "" {
		sess.LaunchModel = launch.model
	}
	if sess.LaunchEffort == "" {
		sess.LaunchEffort = launch.effort
	}
	if s.repo != nil {
		if err := s.repo.Save(ctx, &sess); err != nil {
			return errResp(req.ID, CodeCreateFailed, err.Error())
		}
	}
	if err := s.startReviewRecords(ctx, sess.ID, meta); err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	s.appendEvent(ctx, sess.ID, domain.EventActorAiman, domain.EventSessionCreated, map[string]any{
		"name": name, "group": group, "repo": repo.Name, "review": ref.String(),
		"agent": launch.agent, "tier": launch.tier, "requested_tier": launch.requested,
		"model": launch.model, "effort": launch.effort, "tier_fallback": launch.fellBack,
	})
	s.appendEvent(ctx, sess.ID, domain.EventActorAiman, domain.EventReviewStarted, map[string]any{
		"ref": ref.String(), "base": meta.Base, "head_sha": meta.HeadSHA,
	})
	return Response{ID: req.ID, Result: map[string]any{
		"type": "session", "session": sessionInfo(sess, req.Caller),
		"tier_fallback": launch.fellBack, "requested_tier": launch.requested,
	}}
}

// liveReview finds a session reviewing ref that has not ended.
func liveReview(existing []domain.Session, ref string) (domain.Session, bool) {
	for _, sess := range existing {
		if sess.ReviewPR == ref && liveForUpdate(sess) {
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

// writeReviewFiles writes diff.patch (the staged diff), files.txt (its
// numstat) and meta.json into the worktree's .aiman_review directory.
func writeReviewFiles(ctx context.Context, remote domain.RemoteExecutor, worktree string, meta reviewMeta) error {
	wt := quoteArg(worktree)
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
	dir := filepath.Join(worktree, reviewFilesDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
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

// startReviewRecords saves the review state and seeds the review to-dos.
func (s *Server) startReviewRecords(ctx context.Context, sessionID string, meta reviewMeta) error {
	dir := s.reviewStateDir()
	if dir == "" {
		return errors.New("no home directory for review state")
	}
	if err := review.SaveState(dir, sessionID, review.State{Ref: meta.Ref, HeadSHA: meta.HeadSHA}); err != nil {
		return err
	}
	return s.TodoAccess().Mutate(ctx, sessionID, domain.AuthorAiman, func(l *domain.TodoList) error {
		for _, it := range reviewTodos {
			it.State = domain.TodoPending
			l.Items = append(l.Items, it)
		}
		return nil
	})
}
