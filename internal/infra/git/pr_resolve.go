package git

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
)

// prViewJSONFields must stay compatible with reasonably current GitHub CLI releases.
const prViewJSONFields = "number,title,state,isDraft,mergedAt,url,reviewDecision,mergeable,mergeStateStatus,reviews,comments,statusCheckRollup"

type ghPRViewJSON struct {
	Number           int    `json:"number"`
	Title            string `json:"title"`
	State            string `json:"state"`
	IsDraft          bool   `json:"isDraft"`
	MergedAt         string `json:"mergedAt"`
	URL              string `json:"url"`
	ReviewDecision   string `json:"reviewDecision"`
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
	Reviews          []struct {
		State string `json:"state"`
	} `json:"reviews"`
	Comments          []json.RawMessage `json:"comments"`
	StatusCheckRollup []struct {
		Context string `json:"context"`
		State   string `json:"state"`
		Status  string `json:"status"`
	} `json:"statusCheckRollup"`
}

type ghPRListEntry struct {
	Number int `json:"number"`
}

type graphqlThreadsResp struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				ReviewThreads struct {
					Nodes []struct {
						IsResolved bool `json:"isResolved"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func parseGitHubOwnerRepo(raw string) (owner, repo string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	raw = strings.TrimSuffix(raw, ".git")

	// git@github.com:org/repo or git@github.enterprise:org/repo
	if strings.HasPrefix(raw, "git@") {
		idx := strings.Index(raw, ":")
		if idx < 0 || idx >= len(raw)-1 {
			return "", "", false
		}
		pathPart := raw[idx+1:]
		parts := strings.Split(pathPart, "/")
		if len(parts) < 2 {
			return "", "", false
		}
		return parts[0], strings.Join(parts[1:], "/"), true
	}

	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", false
	}
	segs := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segs) < 2 {
		return "", "", false
	}
	return segs[0], strings.Join(segs[1:], "/"), true
}

func resolvePullRequestForBranch(ctx context.Context, remote domain.RemoteExecutor, repoPath, owner, repo string, hasRepo bool, branch string) *domain.PullRequest {
	branch = strings.TrimSpace(branch)
	if branch == "" || branch == "HEAD" {
		return nil
	}

	tryView := func(extraArgs string) (*ghPRViewJSON, bool) {
		var cmd string
		if extraArgs != "" {
			cmd = fmt.Sprintf("cd %q && gh pr view %s --json %s", repoPath, extraArgs, prViewJSONFields)
		} else {
			cmd = fmt.Sprintf("cd %q && gh pr view --json %s", repoPath, prViewJSONFields)
		}
		out, err := remote.Execute(ctx, cmd)
		out = strings.TrimSpace(out)
		if err != nil || out == "" || !json.Valid([]byte(out)) {
			return nil, false
		}
		var v ghPRViewJSON
		if json.Unmarshal([]byte(out), &v) != nil || v.Number == 0 {
			return nil, false
		}
		return &v, true
	}

	var data *ghPRViewJSON
	if v, ok := tryView(""); ok {
		data = v
	} else if hasRepo && owner != "" && repo != "" {
		head := owner + ":" + branch
		listCmd := fmt.Sprintf("cd %q && gh pr list --repo %s/%s --head %q --state all --limit 1 --json number",
			repoPath, owner, repo, head)
		listOut, err := remote.Execute(ctx, listCmd)
		listOut = strings.TrimSpace(listOut)
		if err == nil && listOut != "" && json.Valid([]byte(listOut)) {
			var entries []ghPRListEntry
			if json.Unmarshal([]byte(listOut), &entries) == nil && len(entries) > 0 && entries[0].Number > 0 {
				n := strconv.Itoa(entries[0].Number)
				if v2, ok2 := tryView(n); ok2 {
					data = v2
				}
			}
		}
	}

	if data == nil {
		return nil
	}

	pr := domainViewToPullRequest(data)
	if hasRepo && owner != "" && repo != "" {
		pr.UnresolvedReviewThreads = countUnresolvedReviewThreads(ctx, remote, repoPath, owner, repo, data.Number)
	} else {
		pr.UnresolvedReviewThreads = -1
	}
	return pr
}

func domainViewToPullRequest(v *ghPRViewJSON) *domain.PullRequest {
	st := strings.ToUpper(strings.TrimSpace(v.State))
	pr := &domain.PullRequest{
		Number:           v.Number,
		Title:            v.Title,
		State:            st,
		IsDraft:          v.IsDraft,
		Merged:           strings.TrimSpace(v.MergedAt) != "" || st == "MERGED",
		URL:              v.URL,
		ReviewDecision:   strings.TrimSpace(v.ReviewDecision),
		CommentCount:     len(v.Comments),
		Mergeable:        strings.ToUpper(strings.TrimSpace(v.Mergeable)),
		MergeStateStatus: strings.ToUpper(strings.TrimSpace(v.MergeStateStatus)),
	}

	pr.DisplayState = deriveDisplayState(pr.State, pr.IsDraft, pr.Merged)
	pr.HasMergeConflict = pr.Mergeable == "CONFLICTING" ||
		strings.Contains(strings.ToUpper(v.MergeStateStatus), "DIRTY")

	switch strings.ToUpper(strings.TrimSpace(v.ReviewDecision)) {
	case "APPROVED":
		pr.ReviewStatus = "approved"
	case "CHANGES_REQUESTED":
		pr.ReviewStatus = "changes_requested"
	case "REVIEW_REQUIRED":
		pr.ReviewStatus = "pending"
	default:
		if len(v.Reviews) > 0 {
			approved := false
			changes := false
			for _, r := range v.Reviews {
				switch r.State {
				case "APPROVED":
					approved = true
				case "CHANGES_REQUESTED":
					changes = true
				}
			}
			switch {
			case changes:
				pr.ReviewStatus = "changes_requested"
			case approved:
				pr.ReviewStatus = "approved"
			default:
				pr.ReviewStatus = "pending"
			}
		} else {
			pr.ReviewStatus = "none"
		}
	}

	if len(v.StatusCheckRollup) > 0 {
		passed := 0
		failed := 0
		pending := 0
		for _, c := range v.StatusCheckRollup {
			switch strings.ToUpper(c.State) {
			case "SUCCESS", "COMPLETED":
				passed++
			case "FAILURE", "ERROR", "CANCELLED", "TIMED_OUT":
				failed++
			case "PENDING", "IN_PROGRESS", "QUEUED", "WAITING":
				pending++
			}
		}
		pr.ChecksSummary = fmt.Sprintf("%d/%d passed", passed, len(v.StatusCheckRollup))
		switch {
		case failed > 0:
			pr.ChecksStatus = "failure"
		case pending > 0:
			pr.ChecksStatus = "pending"
		default:
			pr.ChecksStatus = "success"
		}
	} else {
		pr.ChecksStatus = "none"
	}

	return pr
}

func deriveDisplayState(state string, draft, merged bool) string {
	s := strings.ToUpper(strings.TrimSpace(state))
	if draft {
		return "draft"
	}
	if merged || s == "MERGED" {
		return "merged"
	}
	if s == "CLOSED" {
		return "closed"
	}
	return "open"
}

func countUnresolvedReviewThreads(ctx context.Context, remote domain.RemoteExecutor, repoPath, owner, repo string, prNumber int) int {
	if prNumber <= 0 {
		return -1
	}
	q := `query($owner:String!,$name:String!,$num:Int!){repository(owner:$owner,name:$name){pullRequest(number:$num){reviewThreads(first:100){nodes{isResolved}}}}}`
	// Single-quote the GraphQL document so bash does not expand $variables in the query.
	cmd := fmt.Sprintf("cd %q && gh api graphql -f query=%s -f owner=%q -f name=%q -F num=%d",
		repoPath, shellSingleQuote(q), owner, repo, prNumber)
	out, err := remote.Execute(ctx, cmd)
	if err != nil {
		return -1
	}
	var resp graphqlThreadsResp
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &resp) != nil || len(resp.Errors) > 0 {
		return -1
	}
	nodes := resp.Data.Repository.PullRequest.ReviewThreads.Nodes
	unresolved := 0
	for _, n := range nodes {
		if !n.IsResolved {
			unresolved++
		}
	}
	return unresolved
}

func shellSingleQuote(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `'\''`) + `'`
}
