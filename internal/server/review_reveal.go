package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/bouwerp/aiman/internal/coordinator"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/review"
	"github.com/bouwerp/aiman/internal/todo"
)

// errNotReady wraps every reason a reveal is refused as review_not_ready.
var errNotReady = errors.New("review not ready")

// reviewCall is the params of review.reveal and review.refresh. Session
// defaults to the caller.
type reviewCall struct {
	Session string `json:"session"`
	Single  bool   `json:"single"`
	Force   bool   `json:"force"`
}

// reviewTarget is a review session a reveal or refresh acts on.
type reviewTarget struct {
	call reviewCall
	sess domain.Session
	ref  review.Ref
}

// resolveReview reads the params and finds the review session they name.
func (s *Server) resolveReview(ctx context.Context, req Request) (reviewTarget, *Response) {
	var t reviewTarget
	fail := func(code, msg string) (reviewTarget, *Response) {
		resp := errResp(req.ID, code, msg)
		return reviewTarget{}, &resp
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &t.call); err != nil {
			return fail(CodeInvalidParams, "invalid params")
		}
	}
	target := strings.TrimSpace(t.call.Session)
	if target == "" {
		target = req.Caller
	}
	if target == "" {
		return fail(CodeInvalidParams, "session is required")
	}
	if s.reviewGH == nil || s.reviewBase() == "" {
		return fail(CodeInvalidParams, "review not configured")
	}
	sess, ok := s.findSession(ctx, target)
	if !ok {
		return fail(CodeNotFound, "session not found")
	}
	if sess.ReviewPR == "" {
		return fail(CodeInvalidParams, fmt.Sprintf("session %s is not a review", sess.Name))
	}
	ref, err := review.ParseRef(sess.ReviewPR)
	if err != nil {
		return fail(CodeInvalidParams, err.Error())
	}
	t.sess, t.ref = sess, ref
	return t, nil
}

// handleReviewReveal writes the PR description into the review's scratch
// directory once the blind passes are done: the review:blind to-do is done
// and every consult of the session has finished or been abandoned. With no
// finished consult the review needs single, which records it as a
// one-reviewer review.
func (s *Server) handleReviewReveal(ctx context.Context, req Request) Response {
	t, fail := s.resolveReview(ctx, req)
	if fail != nil {
		return *fail
	}
	defer s.lockReview(reviewLockKey(t.ref))()
	st, err := review.LoadState(s.reviewBase(), t.sess.ID)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, fmt.Sprintf("review state of %s: %v", t.sess.Name, err))
	}
	single, err := s.revealReady(ctx, t.sess, t.call.Single)
	if errors.Is(err, errNotReady) {
		return errResp(req.ID, CodeReviewNotReady, err.Error())
	}
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	pr, err := s.reviewGH.PR(ctx, t.ref)
	if err != nil {
		return errResp(req.ID, CodeCreateFailed, fmt.Sprintf("read %s: %v", t.ref, err))
	}
	path := filepath.Join(s.reviewScratch(t.sess.ID), review.DescriptionFileName)
	if err := os.WriteFile(path, []byte(review.Description(pr)), 0o600); err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	st.Revealed, st.Single = true, single
	if err := review.SaveState(s.reviewBase(), t.sess.ID, st); err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	s.appendEvent(ctx, t.sess.ID, domain.EventActorAiman, domain.EventReviewRevealed, map[string]any{"single": single})
	return Response{ID: req.ID, Result: map[string]any{"type": "review", "session": t.sess.ID, "state": st, "description": path}}
}

// revealReady checks the blind passes and returns whether the review goes
// ahead with one reviewer. Every refusal wraps errNotReady.
func (s *Server) revealReady(ctx context.Context, sess domain.Session, single bool) (bool, error) {
	list, err := s.TodoAccess().Get(sess.ID)
	if err != nil {
		return false, err
	}
	blindDone := false
	for _, it := range list.Items {
		if it.Ref == reviewBlindRef && it.State == domain.TodoDone {
			blindDone = true
		}
	}
	if !blindDone {
		return false, fmt.Errorf("%w: the Blind review to-do is not done", errNotReady)
	}
	finished, ended, err := s.consultOutcomes(ctx, sess.ID)
	if err != nil {
		return false, err
	}
	sessions, err := s.loadSessions(ctx)
	if err != nil {
		return false, err
	}
	for _, c := range sessions {
		if c.Role == domain.SessionRoleConsult && c.ParentID == sess.ID && !ended[c.ID] {
			return false, fmt.Errorf("%w: consult %s has not finished", errNotReady, c.ID)
		}
	}
	if finished > 0 {
		return false, nil
	}
	if !single {
		return false, fmt.Errorf("%w: no consult finished a blind review; pass single to review alone", errNotReady)
	}
	return true, nil
}

// consultOutcomes reads the session's ledger: how many consults finished
// since its last refresh, and the ids of every consult that ever finished or
// was abandoned. A consult with such a row has ended even when its stored row
// could not be forgotten.
func (s *Server) consultOutcomes(ctx context.Context, id string) (int, map[string]bool, error) {
	lister, ok := s.repo.(sessionEventLister)
	if !ok {
		return 0, nil, errors.New("session ledger is not available")
	}
	finished, ended := 0, map[string]bool{}
	var since int64
	for {
		events, err := lister.ListSessionEvents(ctx, id, since, 500)
		if err != nil {
			return 0, nil, err
		}
		for _, ev := range events {
			since = ev.Seq
			switch ev.Kind {
			case domain.EventReviewRefreshed:
				finished = 0
			case domain.EventConsultFinished, domain.EventConsultAbandoned:
				var p struct {
					Child string `json:"child"`
				}
				if json.Unmarshal(ev.Payload, &p) != nil || p.Child == "" {
					continue
				}
				ended[p.Child] = true
				if ev.Kind == domain.EventConsultFinished {
					finished++
				}
			default:
			}
		}
		if len(events) < 500 {
			return finished, ended, nil
		}
	}
}

// maxDeltaLines is the largest fix a refresh treats as a delta, in added plus
// deleted lines. A larger fix goes back to the blind pass.
const maxDeltaLines = 400

// fixPatchFile holds the newest fix's diff after a delta refresh, in the review
// files directory.
const fixPatchFile = "fix.patch"

// handleReviewRefresh restages the review at the PR's current head. A full
// refresh first withdraws the description, so a refresh that fails part-way
// leaves the review blind. It then retires every consult still running (each
// reviewed the old head), restages the PR worktree (fetched, reset hard to the
// new head and soft to the new merge base), rewrites .aiman_review, returns the
// state to blind and reopens the blind, informed, post and gate to-dos. A head
// that has not moved is a no-op unless force is set. A head aiman's own fixer
// pushed onto the reviewed head, with a small enough fix, is a delta: the
// description, the blind to-do and the reveal stay, and fix.patch holds the
// fix. Either way the fix rounds stay, so the round limit holds, and any
// assessment or approval is dropped. Only the review session itself or the
// operator may refresh it.
func (s *Server) handleReviewRefresh(ctx context.Context, req Request) Response {
	t, fail := s.resolveReview(ctx, req)
	if fail != nil {
		return *fail
	}
	if req.Caller != "" {
		if caller, ok := s.findSession(ctx, req.Caller); !ok || caller.ID != t.sess.ID {
			return errResp(req.ID, CodeGateNeedsOperator, fmt.Sprintf("only %s or the operator can refresh its review", t.sess.Name))
		}
	}
	if s.reviewWT == nil {
		return errResp(req.ID, CodeInvalidParams, "review not configured")
	}
	defer s.lockReview(reviewLockKey(t.ref))()
	st, err := review.LoadState(s.reviewBase(), t.sess.ID)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, fmt.Sprintf("review state of %s: %v", t.sess.Name, err))
	}
	pr, err := s.reviewGH.PR(ctx, t.ref)
	if err != nil {
		return errResp(req.ID, CodeCreateFailed, fmt.Sprintf("read %s: %v", t.ref, err))
	}
	if pr.HeadSHA != "" && pr.HeadSHA == st.HeadSHA && !t.call.Force {
		return Response{ID: req.ID, Result: map[string]any{"type": "review", "session": t.sess.ID, "refreshed": false, "delta": false, "state": st}}
	}
	round := s.deltaRound(ctx, t, st, pr.HeadSHA)
	if round == nil {
		if err := s.withdrawDescription(t.sess.ID); err != nil {
			return errResp(req.ID, CodeCreateFailed, err.Error())
		}
	}
	if err := s.retireReviewConsults(ctx, t.sess.ID); err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	meta, code, err := s.restageReview(ctx, t, pr)
	if err != nil {
		return errResp(req.ID, code, err.Error())
	}
	if round != nil {
		if err := s.writeFixPatch(ctx, t, *round); err != nil {
			return errResp(req.ID, CodeCreateFailed, err.Error())
		}
	}
	from := st.HeadSHA
	next := review.State{Ref: st.Ref, HeadSHA: meta.HeadSHA, Fixes: st.Fixes}
	if round != nil {
		next.Revealed, next.Single = st.Revealed, st.Single
	}
	if err := review.SaveState(s.reviewBase(), t.sess.ID, next); err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	delta := round != nil
	reopen := s.reopenReviewTodos
	if delta {
		reopen = s.reopenDeltaTodos
	}
	if err := reopen(ctx, t.sess.ID); err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	s.appendEvent(ctx, t.sess.ID, domain.EventActorAiman, domain.EventReviewRefreshed, map[string]any{"from": from, "to": meta.HeadSHA, "delta": delta})
	return Response{ID: req.ID, Result: map[string]any{"type": "review", "session": t.sess.ID, "refreshed": true, "delta": delta, "state": next}}
}

// withdrawDescription removes the description and any earlier fix.patch, which
// a return to the blind pass must not leave readable.
func (s *Server) withdrawDescription(id string) error {
	scratch := s.reviewScratch(id)
	for _, path := range []string{
		filepath.Join(scratch, review.DescriptionFileName),
		filepath.Join(scratch, reviewFilesDir, fixPatchFile),
	} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// newestPushedRound is the last round that pushed, or nil.
func newestPushedRound(st review.State) *review.FixRound {
	for i := len(st.Fixes) - 1; i >= 0; i-- {
		if st.Fixes[i].PushedSHA != "" {
			return &st.Fixes[i]
		}
	}
	return nil
}

// deltaRound is the round this refresh is a delta of, or nil for a full
// refresh. The PR head must be what the newest round pushed, on top of the head
// this review last covered, and the fix small. A size that cannot be read counts
// as too large.
func (s *Server) deltaRound(ctx context.Context, t reviewTarget, st review.State, head string) *review.FixRound {
	round := newestPushedRound(st)
	if round == nil || round.PushedSHA != head || round.StartSHA != st.HeadSHA {
		return nil
	}
	remote, err := s.reviewHostRemote(ctx, t.sess.ID)
	if err != nil {
		return nil
	}
	cmd := "git -C " + quoteArg(t.sess.WorktreePath) + " diff --no-renames --numstat " + quoteArg(round.StartSHA) + " " + quoteArg(round.PushedSHA)
	out, err := remote.Execute(ctx, cmd)
	if err != nil {
		return nil
	}
	if lines, ok := numstatLines(out); !ok || lines > maxDeltaLines {
		return nil
	}
	return round
}

// numstatLines sums git diff --numstat's added and deleted lines. A binary file
// has no count and reports false: its size cannot be judged.
func numstatLines(numstat string) (int, bool) {
	total := 0
	for _, line := range strings.Split(strings.TrimSpace(numstat), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) < 2 {
			return 0, false
		}
		added, err1 := strconv.Atoi(fields[0])
		deleted, err2 := strconv.Atoi(fields[1])
		if err1 != nil || err2 != nil {
			return 0, false
		}
		total += added + deleted
	}
	return total, true
}

// writeFixPatch writes the round's diff into the review files.
func (s *Server) writeFixPatch(ctx context.Context, t reviewTarget, round review.FixRound) error {
	remote, err := s.reviewHostRemote(ctx, t.sess.ID)
	if err != nil {
		return err
	}
	out, err := remote.Execute(ctx, "git -C "+quoteArg(t.sess.WorktreePath)+" diff "+quoteArg(round.StartSHA)+" "+quoteArg(round.PushedSHA))
	if err != nil {
		return fmt.Errorf("diff the fix: %w: %s", err, strings.TrimSpace(out))
	}
	return os.WriteFile(filepath.Join(s.reviewScratch(t.sess.ID), reviewFilesDir, fixPatchFile), []byte(out), 0o600)
}

// reviewHostRemote is the executor review id's git work runs on.
func (s *Server) reviewHostRemote(ctx context.Context, id string) (domain.RemoteExecutor, error) {
	existing, err := s.loadSessions(ctx)
	if err != nil {
		return nil, err
	}
	remote := s.reviewRemote(id, existing)
	if remote == nil {
		return nil, errors.New("no remote to run the review's git commands on")
	}
	return remote, nil
}

// retireReviewConsults unlinks, records abandoned and retires every consult of
// review id still stored: each reviewed the head a refresh replaces.
func (s *Server) retireReviewConsults(ctx context.Context, id string) error {
	sessions, err := s.loadSessions(ctx)
	if err != nil {
		return err
	}
	for _, c := range sessions {
		if c.Role != domain.SessionRoleConsult || c.ParentID != id {
			continue
		}
		todoID := ""
		err := s.TodoAccess().Mutate(ctx, id, domain.AuthorAiman, func(l *domain.TodoList) error {
			for i := range l.Items {
				if l.Items[i].Ref == coordinator.ChildRef(c.ID) {
					l.Items[i].Ref, todoID = "", l.Items[i].ID
				}
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("unlink consult %s: %w", c.ID, err)
		}
		s.appendEvent(ctx, id, domain.EventActorAiman, domain.EventConsultAbandoned, map[string]any{"child": c.ID, "todo": todoID, "reason": "refresh"})
		s.RetireConsult(ctx, c)
	}
	return nil
}

// restageReview stages the PR worktree at the PR's current head and rewrites
// the review files. On error it returns the response code.
func (s *Server) restageReview(ctx context.Context, t reviewTarget, pr review.PR) (reviewMeta, string, error) {
	existing, err := s.loadSessions(ctx)
	if err != nil {
		return reviewMeta{}, CodeCreateFailed, err
	}
	remote := s.reviewRemote(t.sess.ID, existing)
	if remote == nil {
		return reviewMeta{}, CodeCreateFailed, errors.New("no remote to refresh the review worktree on")
	}
	wt, err := s.reviewWT.SetupRemotePRWorktree(ctx, remote, domain.Repo{Name: t.ref.Slug()}, t.ref.Number, pr.BaseRef)
	if errors.Is(err, domain.ErrOriginMismatch) {
		return reviewMeta{}, CodeInvalidParams, err
	}
	if err != nil {
		return reviewMeta{}, CodeCreateFailed, err
	}
	wt.Path = filepath.Clean(wt.Path)
	if wt.Path != filepath.Clean(t.sess.WorktreePath) {
		return reviewMeta{}, CodeCreateFailed, fmt.Errorf("refresh staged %s, but the review's worktree is %s", wt.Path, t.sess.WorktreePath)
	}
	meta := reviewMeta{
		Ref: t.ref.String(), Number: t.ref.Number, Base: pr.BaseRef, HeadSHA: stagedHead(ctx, remote, wt.Path, pr.HeadSHA),
		Title: pr.Title, Author: pr.Author, Worktree: wt.Path,
	}
	if err := writeReviewFiles(ctx, remote, s.reviewScratch(t.sess.ID), meta); err != nil {
		return reviewMeta{}, CodeCreateFailed, err
	}
	if err := s.exportReviewTree(ctx, remote, wt.Path, t.sess.ID); err != nil {
		return reviewMeta{}, CodeCreateFailed, err
	}
	return meta, "", nil
}

// reopenReviewTodos sets the blind to-do, and everything a delta reopens, back
// to pending, as aiman.
func (s *Server) reopenReviewTodos(ctx context.Context, id string) error {
	return s.reopenTodos(ctx, id, reviewBlindRef, reviewInformedRef, reviewPostRef, todo.ReviewGateRef, todo.ApproveGateRef)
}

// reopenDeltaTodos sets the informed and post to-dos and the gates back to
// pending, as aiman. The blind pass still holds after a delta.
func (s *Server) reopenDeltaTodos(ctx context.Context, id string) error {
	return s.reopenTodos(ctx, id, reviewInformedRef, reviewPostRef, todo.ReviewGateRef, todo.ApproveGateRef)
}

func (s *Server) reopenTodos(ctx context.Context, id string, refs ...string) error {
	return s.TodoAccess().Mutate(ctx, id, domain.AuthorAiman, func(l *domain.TodoList) error {
		for i := range l.Items {
			if slices.Contains(refs, l.Items[i].Ref) {
				l.Items[i].State = domain.TodoPending
			}
		}
		return nil
	})
}
