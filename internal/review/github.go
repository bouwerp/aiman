package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
)

// PR is the part of a pull request a review needs.
type PR struct {
	Number    int
	Title     string
	Author    string
	Body      string
	BaseRef   string
	HeadRef   string
	HeadSHA   string
	Additions int
	Deletions int
}

// Comment is one inline review comment on the right side of the diff.
type Comment struct {
	Path string
	Line int
	Body string
}

// Post is a review as GitHub's create-review API takes it.
type Post struct {
	Event    string
	Body     string
	CommitID string
	Comments []Comment
}

// GitHub is what a review needs from GitHub.
type GitHub interface {
	ListPRs(ctx context.Context, slug string) ([]PR, error)
	PR(ctx context.Context, ref Ref) (PR, error)
	PostReview(ctx context.Context, ref Ref, post Post) error
}

// GHCLI implements GitHub with the gh CLI. Run executes a command and returns
// its output; RunInput does the same with input on stdin and is required only
// by PostReview. Both are injected so tests never reach gh or the network.
type GHCLI struct {
	Run      func(ctx context.Context, name string, args ...string) (string, error)
	RunInput func(ctx context.Context, input, name string, args ...string) (string, error)
}

var _ GitHub = (*GHCLI)(nil)

type ghPR struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	BaseRefName string `json:"baseRefName"`
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
	Additions   int    `json:"additions"`
	Deletions   int    `json:"deletions"`
	Author      struct {
		Login string `json:"login"`
	} `json:"author"`
}

func (g ghPR) pr() PR {
	return PR{
		Number: g.Number, Title: g.Title, Author: g.Author.Login, Body: g.Body,
		BaseRef: g.BaseRefName, HeadRef: g.HeadRefName, HeadSHA: g.HeadRefOid,
		Additions: g.Additions, Deletions: g.Deletions,
	}
}

// ListPRs lists the open pull requests of a repository.
func (c *GHCLI) ListPRs(ctx context.Context, slug string) ([]PR, error) {
	out, err := c.Run(ctx, "gh", "pr", "list", "--repo", slug, "--state", "open",
		"--json", "number,title,author,headRefName,additions,deletions")
	if err != nil {
		return nil, fmt.Errorf("gh pr list %s: %w", slug, err)
	}
	var raw []ghPR
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("parse gh pr list: %w", err)
	}
	prs := make([]PR, len(raw))
	for i, r := range raw {
		prs[i] = r.pr()
	}
	return prs, nil
}

// PR reads one pull request, including its body and head SHA.
func (c *GHCLI) PR(ctx context.Context, ref Ref) (PR, error) {
	out, err := c.Run(ctx, "gh", "pr", "view", strconv.Itoa(ref.Number), "--repo", ref.Slug(),
		"--json", "number,title,author,body,baseRefName,headRefName,headRefOid,additions,deletions")
	if err != nil {
		return PR{}, fmt.Errorf("gh pr view %s: %w", ref, err)
	}
	var raw ghPR
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return PR{}, fmt.Errorf("parse gh pr view: %w", err)
	}
	return raw.pr(), nil
}

type reviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
	Side string `json:"side"`
}

type reviewBody struct {
	CommitID string          `json:"commit_id"`
	Body     string          `json:"body"`
	Event    string          `json:"event"`
	Comments []reviewComment `json:"comments"`
}

// PostReview creates a review with its inline comments in one API call.
func (c *GHCLI) PostReview(ctx context.Context, ref Ref, post Post) error {
	if c.RunInput == nil {
		return errors.New("post review: GHCLI.RunInput is not set")
	}
	body := reviewBody{CommitID: post.CommitID, Body: post.Body, Event: post.Event, Comments: []reviewComment{}}
	for _, cm := range post.Comments {
		body.Comments = append(body.Comments, reviewComment{Path: cm.Path, Line: cm.Line, Body: cm.Body, Side: "RIGHT"})
	}
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode review: %w", err)
	}
	_, err = c.RunInput(ctx, string(data), "gh", "api", "--method", "POST",
		fmt.Sprintf("repos/%s/pulls/%d/reviews", ref.Slug(), ref.Number), "--input", "-")
	if err != nil {
		return fmt.Errorf("post review on %s: %w", ref, err)
	}
	return nil
}
