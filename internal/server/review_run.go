package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/review"
)

// ReviewRunTimeout is the default bound of one review.run. CallRaw waits a
// little longer so it reads the answer.
const ReviewRunTimeout = 10 * time.Minute

type reviewRunParams struct {
	Session        string   `json:"session"`
	Cmd            []string `json:"cmd"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

// handleReviewRun runs cmd in the review's sandbox and logs it.
func (s *Server) handleReviewRun(ctx context.Context, req Request) Response {
	var p reviewRunParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, "invalid params")
		}
	}
	if len(p.Cmd) == 0 {
		return errResp(req.ID, CodeInvalidParams, "cmd is required")
	}
	timeout := ReviewRunTimeout
	if p.TimeoutSeconds > 0 {
		timeout = time.Duration(p.TimeoutSeconds) * time.Second
	}
	if p.TimeoutSeconds < 0 {
		return errResp(req.ID, CodeInvalidParams, "timeout_seconds must be >= 0")
	}
	rp := reviewCall{Session: p.Session}
	raw, _ := json.Marshal(rp)
	req.Params = raw
	t, fail := s.resolveReview(ctx, req)
	if fail != nil {
		return *fail
	}
	if s.reviewRunner.Docker == nil {
		return errResp(req.ID, CodeInvalidParams, "review sandbox not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	sb, err := s.sandboxFor(t.sess)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	out, exit, err := s.reviewRunner.Run(ctx, sb, p.Cmd, 0)
	if err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	n, err := s.logReviewRun(t.sess, p.Cmd, exit, out)
	if err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	s.appendEvent(ctx, t.sess.ID, domain.EventActorAiman, domain.EventReviewRun, map[string]any{"n": n, "exit": exit})
	return Response{ID: req.ID, Result: map[string]any{
		"type": "review_run", "output": out, "exit": exit, "n": n,
	}}
}

func (s *Server) sandboxFor(sess domain.Session) (review.Sandbox, error) {
	sb := s.reviewSandbox(sess.ID)
	sb.Network = s.reviewCfg.Sandbox.Network
	override := ""
	if ref, err := review.ParseRef(sess.ReviewPR); err == nil {
		if b, ok := config.LookupRepo(s.reviewRepos, ref.Slug()); ok {
			override = b.Review.Image
		}
	}
	img, err := review.Image(sb.Src, override)
	if err != nil {
		return review.Sandbox{}, err
	}
	sb.Image = img
	return sb, nil
}

func (s *Server) reviewSandbox(id string) review.Sandbox {
	base := filepath.Join(s.reviewBase(), id)
	return review.Sandbox{
		ID:  id,
		Dir: filepath.Join(base, "sandbox"),
		Src: filepath.Join(base, "src"),
		Out: filepath.Join(base, "out"),
	}
}

func (s *Server) exportReviewTree(ctx context.Context, remote domain.RemoteExecutor, worktree, id string) error {
	sb := s.reviewSandbox(id)
	if err := os.MkdirAll(sb.Out, 0o700); err != nil {
		return err
	}
	run := review.DirRun
	if remote != nil {
		run = func(ctx context.Context, dir, name string, args ...string) (string, error) {
			parts := make([]string, 0, 3+len(args))
			if name == "git" {
				parts = append(parts, "git", "-C", quoteArg(dir))
			} else {
				parts = append(parts, quoteArg(name))
			}
			for _, a := range args {
				parts = append(parts, quoteArg(a))
			}
			return remote.Execute(ctx, strings.Join(parts, " "))
		}
	}
	return review.Export(ctx, run, worktree, sb.Src)
}

func (s *Server) logReviewRun(sess domain.Session, cmd []string, exit int, out string) (int, error) {
	dir := filepath.Join(s.reviewScratch(sess.ID), reviewFilesDir, "runs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return 0, err
	}
	n, err := nextRunN(dir)
	if err != nil {
		return 0, err
	}
	body := fmt.Sprintf("command: %s\nexit: %d\n\n%s", strings.Join(cmd, " "), exit, out)
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.log", n)), []byte(body), 0o600); err != nil {
		return 0, err
	}
	return n, nil
}

func nextRunN(dir string) (int, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	max := 0
	for _, e := range ents {
		n, convErr := strconv.Atoi(strings.TrimSuffix(e.Name(), ".log"))
		if convErr != nil || n <= max || e.Name() != fmt.Sprintf("%d.log", n) {
			continue
		}
		max = n
	}
	return max + 1, nil
}

func (s *Server) downReviewSandbox(ctx context.Context, sess domain.Session) {
	if sess.ReviewPR == "" || s.reviewRunner.Docker == nil {
		return
	}
	if err := s.reviewRunner.Down(ctx, s.reviewSandbox(sess.ID)); err != nil {
		log.Printf("review: down sandbox %s: %v", sess.ID, err)
	}
}
