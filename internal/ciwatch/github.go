// Package ciwatch watches a session's pull request and turns failed checks
// and new review comments into work for the agent.
package ciwatch

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrNoPR means the worktree's branch has no pull request yet.
var ErrNoPR = errors.New("no pull request for this branch")

// LogTailLines is how much of a failed job's log reaches the agent.
const LogTailLines = 150

// defaultGHTimeout bounds one gh call when GHCLI.Timeout is zero.
const defaultGHTimeout = 45 * time.Second

// Conclusion is a check result reduced to what the watcher acts on.
type Conclusion string

const (
	ConclusionFailure Conclusion = "failure"
	ConclusionSuccess Conclusion = "success"
	ConclusionPending Conclusion = "pending"
	ConclusionOther   Conclusion = "other" // cancelled, action required, stale
)

// Check is one entry of a PR's status rollup.
type Check struct {
	Name       string // CheckRun.name or StatusContext.context
	Conclusion Conclusion
	DetailsURL string // detailsUrl or targetUrl
	JobID      int64  // Actions job id; 0 when the check is not an Actions job
}

// PRView is the part of `gh pr view` the watcher needs.
type PRView struct {
	Number  int
	HeadSHA string
	State   string // OPEN | CLOSED | MERGED
	Checks  []Check
}

// ReviewComment is one inline PR review comment.
type ReviewComment struct {
	ID        int64
	Body      string
	Path      string
	Line      int // line, else original_line, else 0
	Author    string
	InReplyTo int64
	CreatedAt time.Time
}

// GitHub is everything the watcher asks of GitHub. GHCLI is the real one.
type GitHub interface {
	PRView(ctx context.Context, dir string) (PRView, error) // ErrNoPR when gh reports none
	FailedJobLog(ctx context.Context, dir string, jobID int64) (string, error)
	ReviewComments(ctx context.Context, dir string, pr int) ([]ReviewComment, error)
	ViewerLogin(ctx context.Context) (string, error)
}

// GHCLI implements GitHub by running gh in the session's worktree, so gh
// resolves the repository and branch from it.
type GHCLI struct {
	Timeout time.Duration // per call; zero means 45s
}

var _ GitHub = GHCLI{}

func (g GHCLI) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = defaultGHTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if strings.Contains(msg, "no pull requests found") {
			return nil, ErrNoPR
		}
		if msg == "" {
			return nil, fmt.Errorf("gh %s: %w", args[0], err)
		}
		return nil, fmt.Errorf("gh %s: %w: %s", args[0], err, msg)
	}
	return out, nil
}

// PRView reads the PR for the worktree's current branch.
func (g GHCLI) PRView(ctx context.Context, dir string) (PRView, error) {
	out, err := g.run(ctx, dir, "pr", "view", "--json", "number,headRefOid,state,statusCheckRollup")
	if err != nil {
		return PRView{}, err
	}
	return ParsePRView(out)
}

// FailedJobLog returns the tail of a failed Actions job's log.
func (g GHCLI) FailedJobLog(ctx context.Context, dir string, jobID int64) (string, error) {
	out, err := g.run(ctx, dir, "run", "view", "--job", strconv.FormatInt(jobID, 10), "--log-failed")
	if err != nil {
		return "", err
	}
	return TailLines(string(out), LogTailLines), nil
}

// ReviewComments lists every inline review comment on the PR.
func (g GHCLI) ReviewComments(ctx context.Context, dir string, pr int) ([]ReviewComment, error) {
	out, err := g.run(ctx, dir, "api", fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/comments", pr), "--paginate",
		"--jq", ".[] | {id, body, path, line, original_line, in_reply_to_id, created_at, user: .user.login}")
	if err != nil {
		return nil, err
	}
	return ParseReviewComments(out)
}

// ViewerLogin is the login gh is authenticated as.
func (g GHCLI) ViewerLogin(ctx context.Context) (string, error) {
	out, err := g.run(ctx, "", "api", "user", "--jq", ".login")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

var actionsJobRE = regexp.MustCompile(`/actions/runs/\d+/job/(\d+)`)

type rollupEntry struct {
	Typename   string `json:"__typename"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	DetailsURL string `json:"detailsUrl"`
	Context    string `json:"context"`
	State      string `json:"state"`
	TargetURL  string `json:"targetUrl"`
}

// ParsePRView parses `gh pr view --json number,headRefOid,state,statusCheckRollup`.
func ParsePRView(raw []byte) (PRView, error) {
	var v struct {
		Number     int           `json:"number"`
		HeadRefOid string        `json:"headRefOid"`
		State      string        `json:"state"`
		Rollup     []rollupEntry `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return PRView{}, fmt.Errorf("parse gh pr view: %w", err)
	}
	pr := PRView{Number: v.Number, HeadSHA: v.HeadRefOid, State: v.State}
	for _, e := range v.Rollup {
		pr.Checks = append(pr.Checks, e.check())
	}
	return pr, nil
}

func (e rollupEntry) check() Check {
	if e.Typename == "StatusContext" {
		return Check{Name: e.Context, Conclusion: statusContextConclusion(e.State), DetailsURL: e.TargetURL}
	}
	c := Check{Name: e.Name, Conclusion: checkRunConclusion(e.Status, e.Conclusion), DetailsURL: e.DetailsURL}
	if m := actionsJobRE.FindStringSubmatch(e.DetailsURL); m != nil {
		c.JobID, _ = strconv.ParseInt(m[1], 10, 64)
	}
	return c
}

func checkRunConclusion(status, conclusion string) Conclusion {
	if status != "COMPLETED" {
		return ConclusionPending
	}
	switch conclusion {
	case "FAILURE", "TIMED_OUT", "STARTUP_FAILURE":
		return ConclusionFailure
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return ConclusionSuccess
	default:
		return ConclusionOther
	}
}

func statusContextConclusion(state string) Conclusion {
	switch state {
	case "FAILURE", "ERROR":
		return ConclusionFailure
	case "SUCCESS":
		return ConclusionSuccess
	case "PENDING", "EXPECTED":
		return ConclusionPending
	default:
		return ConclusionOther
	}
}

// ParseReviewComments parses the one-object-per-line output of the
// ReviewComments jq filter.
func ParseReviewComments(ndjson []byte) ([]ReviewComment, error) {
	var out []ReviewComment
	sc := bufio.NewScanner(bytes.NewReader(ndjson))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // comment bodies can be long
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var c struct {
			ID           int64     `json:"id"`
			Body         string    `json:"body"`
			Path         string    `json:"path"`
			Line         *int      `json:"line"`
			OriginalLine *int      `json:"original_line"`
			InReplyTo    *int64    `json:"in_reply_to_id"`
			CreatedAt    time.Time `json:"created_at"`
			User         string    `json:"user"`
		}
		if err := json.Unmarshal(line, &c); err != nil {
			return nil, fmt.Errorf("parse review comment: %w", err)
		}
		rc := ReviewComment{ID: c.ID, Body: c.Body, Path: c.Path, Author: c.User, CreatedAt: c.CreatedAt}
		switch {
		case c.Line != nil:
			rc.Line = *c.Line
		case c.OriginalLine != nil:
			rc.Line = *c.OriginalLine
		}
		if c.InReplyTo != nil {
			rc.InReplyTo = *c.InReplyTo
		}
		out = append(out, rc)
	}
	return out, sc.Err()
}

// TailLines returns the last n lines of s. A trailing newline does not count
// as an extra empty line.
func TailLines(s string, n int) string {
	s = strings.TrimSuffix(s, "\n")
	if n <= 0 || s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= n {
		return s
	}
	return strings.Join(lines[len(lines)-n:], "\n")
}
