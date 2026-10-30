package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/review"
)

var errNoCoverage = errors.New("no coverage method applies")

type reviewCoverageParams struct {
	Session string `json:"session"`
}

// handleReviewCoverage runs the PR's tests with coverage in the sandbox and
// writes .aiman_review/coverage.md for the changed lines.
func (s *Server) handleReviewCoverage(ctx context.Context, req Request) Response {
	ctx, cancel := context.WithTimeout(ctx, ReviewRunTimeout)
	defer cancel()
	var p reviewCoverageParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, "invalid params")
		}
	}
	raw, _ := json.Marshal(reviewCall{Session: p.Session})
	req.Params = raw
	t, fail := s.resolveReview(ctx, req)
	if fail != nil {
		return *fail
	}
	if s.reviewRunner.Docker == nil {
		return errResp(req.ID, CodeInvalidParams, "review sandbox not configured")
	}
	sb, err := s.sandboxFor(t.sess)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	diff, err := os.ReadFile(filepath.Join(s.reviewScratch(t.sess.ID), reviewFilesDir, "diff.patch")) //nolint:gosec // path is under the review scratch
	if err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	hunks := review.ChangedLines(string(diff))
	cov, err := s.collectCoverage(ctx, t, sb, hunks)
	if err != nil {
		if errors.Is(err, errNoCoverage) {
			return errResp(req.ID, CodeReviewNoCoverage, err.Error())
		}
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	md, ran, missed := review.CoverageReport(hunks, cov)
	out := filepath.Join(s.reviewScratch(t.sess.ID), reviewFilesDir, "coverage.md")
	if err := os.WriteFile(out, []byte(md), 0o600); err != nil {
		return errResp(req.ID, CodeCreateFailed, err.Error())
	}
	s.appendEvent(ctx, t.sess.ID, domain.EventActorAiman, domain.EventReviewCoverage, map[string]any{
		"ran": ran, "missed": missed,
	})
	return Response{ID: req.ID, Result: map[string]any{
		"type": "review_coverage", "ran": ran, "missed": missed,
	}}
}

func (s *Server) collectCoverage(ctx context.Context, t reviewTarget, sb review.Sandbox, hunks []review.Hunk) (map[string]map[int]bool, error) {
	_, err := os.Stat(filepath.Join(sb.Src, "go.mod"))
	if err == nil {
		return s.goCoverage(ctx, sb, hunks)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	cmd := repoCoverageCmd(s.reviewRepos, t.sess.ReviewPR)
	if cmd == "" {
		return nil, errNoCoverage
	}
	return s.istanbulCoverage(ctx, sb, cmd)
}

func repoCoverageCmd(repos map[string]config.RepoBootstrap, reviewPR string) string {
	ref, err := review.ParseRef(reviewPR)
	if err != nil {
		return ""
	}
	b, ok := config.LookupRepo(repos, ref.Slug())
	if !ok {
		return ""
	}
	return strings.TrimSpace(b.Review.Coverage)
}

func (s *Server) goCoverage(ctx context.Context, sb review.Sandbox, hunks []review.Hunk) (map[string]map[int]bool, error) {
	pkgs := goCoverPackages(hunks)
	cmd := append([]string{"go", "test", "-coverprofile=/out/cover.out"}, pkgs...)
	if _, _, err := s.reviewRunner.Run(ctx, sb, cmd, 0); err != nil {
		return nil, err
	}
	cov, err := review.GoCoverage(filepath.Join(sb.Out, "cover.out"))
	if err != nil {
		return nil, err
	}
	return review.RelocateCoverage(cov, review.ModulePath(sb.Src)), nil
}

func goCoverPackages(hunks []review.Hunk) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, h := range hunks {
		if filepath.Ext(h.Path) != ".go" {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(h.Path))
		pkg := "."
		if dir != "." {
			pkg = "./" + dir
		}
		if _, ok := seen[pkg]; ok {
			continue
		}
		seen[pkg] = struct{}{}
		out = append(out, pkg)
	}
	if len(out) == 0 {
		return []string{"./..."}
	}
	slices.Sort(out)
	return out
}

func (s *Server) istanbulCoverage(ctx context.Context, sb review.Sandbox, cmd string) (map[string]map[int]bool, error) {
	if _, _, err := s.reviewRunner.Run(ctx, sb, []string{"sh", "-c", cmd}, 0); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(sb.Out, "coverage-final.json")) //nolint:gosec // path is the sandbox /out mount
	if errors.Is(err, os.ErrNotExist) {
		data, err = os.ReadFile(filepath.Join(sb.Src, "coverage", "coverage-final.json")) //nolint:gosec // path is the sandbox /src mount
	}
	if err != nil {
		return nil, err
	}
	return review.IstanbulCoverage(data, "/src")
}
