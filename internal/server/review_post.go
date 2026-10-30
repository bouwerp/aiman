package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/review"
	"github.com/bouwerp/aiman/internal/todo"
)

// ReviewPostTimeout bounds one review.post: the PR lookup and the GitHub call.
// CallRaw waits a little longer so it reads the answer.
const ReviewPostTimeout = 3 * time.Minute

// dispatchReview routes the review.* methods.
func (s *Server) dispatchReview(ctx context.Context, req Request) Response {
	switch req.Method {
	case "review.reveal":
		return s.handleReviewReveal(ctx, req)
	case "review.post":
		return s.handleReviewPost(ctx, req)
	case "review.run":
		return s.handleReviewRun(ctx, req)
	case "review.debug":
		return s.handleReviewDebug(ctx, req)
	default:
		return s.handleReviewRefresh(ctx, req)
	}
}

// reviewBriefKey is the brief page key of a review: pr-<n>.
func reviewBriefKey(reviewPR string) string {
	if ref, err := review.ParseRef(reviewPR); err == nil {
		return fmt.Sprintf("pr-%d", ref.Number)
	}
	return "pr"
}

// addReviewGate adds the open review gate on a review's first brief publish.
// Later publishes leave an existing gate, open or closed, alone.
func (s *Server) addReviewGate(id string) error {
	return s.TodoAccess().Mutate(context.Background(), id, domain.AuthorAiman, func(l *domain.TodoList) error {
		for _, it := range l.Items {
			if it.Ref == todo.ReviewGateRef {
				return nil
			}
		}
		todo.AddGate(l, todo.ReviewGateRef)
		return nil
	})
}

type reviewPostParams struct {
	Session string `json:"session"`
	File    string `json:"file"`
	Event   string `json:"event"`
}

// handleReviewPost posts the review file as one GitHub review. It refuses
// while the review gate is open, after a post until a refresh reopens
// review:post, while the file lacks the operator's answers or calls, before
// the description is revealed, and when the PR head is not the one the
// review was staged from. COMMENT (default) and REQUEST_CHANGES are the
// allowed events; File.Post refuses APPROVE after those checks.
func (s *Server) handleReviewPost(ctx context.Context, req Request) Response {
	ctx, cancel := context.WithTimeout(ctx, ReviewPostTimeout)
	defer cancel()
	var p reviewPostParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, "invalid params")
		}
	}
	if strings.TrimSpace(p.File) == "" {
		return errResp(req.ID, CodeInvalidParams, "file is required")
	}
	if p.Event == "" {
		p.Event = "COMMENT"
	}
	rp := reviewCall{Session: p.Session}
	raw, _ := json.Marshal(rp)
	req.Params = raw
	t, fail := s.resolveReview(ctx, req)
	if fail != nil {
		return *fail
	}
	md, err := readConfined(t.sess, sessionDir(t.sess), p.File)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	defer s.lockReview(reviewLockKey(t.ref))()
	built, code, err := s.buildReviewPost(ctx, t, md, p.Event)
	if err != nil {
		return errResp(req.ID, code, err.Error())
	}
	if err := s.reviewGH.PostReview(ctx, t.ref, built.post); err != nil {
		return errResp(req.ID, CodeCreateFailed, fmt.Sprintf("post %s: %v", t.ref, err))
	}
	tickErr := s.completeReviewPost(ctx, t.sess.ID)
	counts := map[string]any{"event": p.Event, "single": built.single, "blocking": 0, "suggestion": 0, "question": 0, "nit": 0}
	for _, fd := range built.findings {
		if fd.Call != "agree" {
			continue
		}
		if n, ok := counts[fd.Label].(int); ok {
			counts[fd.Label] = n + 1
		}
	}
	s.appendEvent(ctx, t.sess.ID, domain.EventActorAiman, domain.EventReviewPosted, counts)
	if tickErr != nil {
		return errResp(req.ID, CodeCreateFailed, "review posted, but its to-do could not be ticked: "+tickErr.Error())
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "review_posted", "session": t.sess.ID, "event": p.Event, "comments": len(built.post.Comments)}}
}

type builtPost struct {
	post     review.Post
	findings []review.Finding
	single   bool
}

// buildReviewPost runs the refusals in order (gate, already posted,
// completeness, not revealed, head, event) and builds the post from the file.
// On error it returns the response code.
func (s *Server) buildReviewPost(ctx context.Context, t reviewTarget, md []byte, event string) (builtPost, string, error) {
	list, err := s.TodoAccess().Get(t.sess.ID)
	if err != nil {
		return builtPost{}, CodeInvalidParams, err
	}
	if todo.GateOpen(list, todo.ReviewGateRef) {
		return builtPost{}, CodeGateNeedsOperator, errors.New("the review gate is open: get the operator's approval, then run aiman todo approve-review")
	}
	for _, it := range list.Items {
		if it.Ref == reviewPostRef && it.State == domain.TodoDone {
			return builtPost{}, CodeReviewAlreadyPosted, errors.New("this review is already posted: run aiman review refresh to post again")
		}
	}
	f, err := review.ParseFile(string(md))
	if err != nil {
		return builtPost{}, CodeReviewIncomplete, fmt.Errorf("review file: %w", err)
	}
	if m := f.Missing(); len(m) > 0 {
		return builtPost{}, CodeReviewIncomplete, fmt.Errorf("review is incomplete: %s", strings.Join(m, "; "))
	}
	st, err := review.LoadState(s.reviewBase(), t.sess.ID)
	if err != nil {
		return builtPost{}, CodeInvalidParams, fmt.Errorf("review state of %s: %w", t.sess.Name, err)
	}
	if !st.Revealed {
		return builtPost{}, CodeReviewNotReady, errors.New("the PR description has not been revealed: run aiman review reveal first")
	}
	pr, err := s.reviewGH.PR(ctx, t.ref)
	if err != nil {
		return builtPost{}, CodeCreateFailed, fmt.Errorf("read %s: %w", t.ref, err)
	}
	if pr.HeadSHA != st.HeadSHA {
		return builtPost{}, CodeReviewHeadMoved, fmt.Errorf("%s moved from %s to %s: run aiman review refresh and review again", t.ref, st.HeadSHA, pr.HeadSHA)
	}
	post, err := f.Post(event, st.HeadSHA, st.Single)
	if err != nil {
		return builtPost{}, CodeInvalidParams, err
	}
	return builtPost{post: post, findings: f.Findings, single: st.Single}, "", nil
}

// completeReviewPost ticks the Post the review to-do.
func (s *Server) completeReviewPost(ctx context.Context, id string) error {
	return s.TodoAccess().Mutate(ctx, id, domain.AuthorAiman, func(l *domain.TodoList) error {
		for i := range l.Items {
			if l.Items[i].Ref == reviewPostRef {
				l.Items[i].State = domain.TodoDone
			}
		}
		return nil
	})
}
