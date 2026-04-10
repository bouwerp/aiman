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

// ghJSONCmdSuffix hides stderr so SSH CombinedOutput (stdout+stderr) stays valid JSON.
const ghJSONCmdSuffix = " 2>/dev/null"

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
		Context    string `json:"context"`
		State      string `json:"state"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
	} `json:"statusCheckRollup"`
}

type ghPRListEntry struct {
	Number int `json:"number"`
}

type ghPRBranchListEntry struct {
	Number      int    `json:"number"`
	HeadRefName string `json:"headRefName"`
	State       string `json:"state"`
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
			cmd = fmt.Sprintf("cd %q && gh pr view %s --json %s%s", repoPath, extraArgs, prViewJSONFields, ghJSONCmdSuffix)
		} else {
			cmd = fmt.Sprintf("cd %q && gh pr view --json %s%s", repoPath, prViewJSONFields, ghJSONCmdSuffix)
		}
		// Ignore Execute error: gh may exit non-zero while still printing JSON; SSH also merges stderr into output.
		out, _ := remote.Execute(ctx, cmd)
		out = strings.TrimSpace(out)
		if out == "" || !json.Valid([]byte(out)) {
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
		repoSlug := owner + "/" + repo
		repoFlag := "--repo " + strconv.Quote(repoSlug)
		if v, ok := tryView(repoFlag); ok {
			data = v
		} else if v, ok := tryView(fmt.Sprintf("%s %s", strconv.Quote(branch), repoFlag)); ok {
			// Explicit branch + repo (works when gh's default remote/repo guess is wrong).
			data = v
		} else {
			head := owner + ":" + branch
			listCmd := fmt.Sprintf("cd %q && gh pr list %s --head %q --state all --limit 1 --json number%s",
				repoPath, repoFlag, head, ghJSONCmdSuffix)
			listOut, _ := remote.Execute(ctx, listCmd)
			listOut = strings.TrimSpace(listOut)
			if listOut != "" && json.Valid([]byte(listOut)) {
				var entries []ghPRListEntry
				if json.Unmarshal([]byte(listOut), &entries) == nil && len(entries) > 0 && entries[0].Number > 0 {
					n := strconv.Itoa(entries[0].Number)
					if v2, ok2 := tryView(n); ok2 {
						data = v2
					}
				}
			}
		}
	}

	if data == nil && hasRepo && owner != "" && repo != "" {
		// Case mismatch vs GitHub head ref, fork heads, or --head filter quirks: scan open/merged PRs by branch name.
		repoSlug := owner + "/" + repo
		repoFlag := "--repo " + strconv.Quote(repoSlug)
		scanCmd := fmt.Sprintf("cd %q && gh pr list %s --state all --limit 50 --json number,headRefName,state%s",
			repoPath, repoFlag, ghJSONCmdSuffix)
		scanOut, _ := remote.Execute(ctx, scanCmd)
		scanOut = strings.TrimSpace(scanOut)
		if scanOut != "" && json.Valid([]byte(scanOut)) {
			var entries []ghPRBranchListEntry
			if json.Unmarshal([]byte(scanOut), &entries) == nil {
				bestNum := 0
				bestScore := -1
				for _, e := range entries {
					if !strings.EqualFold(strings.TrimSpace(e.HeadRefName), branch) {
						continue
					}
					score := 0
					switch strings.ToUpper(strings.TrimSpace(e.State)) {
					case "OPEN":
						score = 3
					case "MERGED":
						score = 2
					case "CLOSED":
						score = 1
					}
					if score > bestScore {
						bestScore = score
						bestNum = e.Number
					}
				}
				if bestNum > 0 {
					if v2, ok2 := tryView(strconv.Itoa(bestNum)); ok2 {
						data = v2
					}
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
			state := strings.ToUpper(strings.TrimSpace(c.State))
			status := strings.ToUpper(strings.TrimSpace(c.Status))
			conclusion := strings.ToUpper(strings.TrimSpace(c.Conclusion))
			switch {
			case conclusion == "SUCCESS":
				passed++
			case conclusion == "FAILURE" || conclusion == "ERROR" || conclusion == "CANCELLED" ||
				conclusion == "TIMED_OUT" || conclusion == "ACTION_REQUIRED" || conclusion == "STARTUP_FAILURE":
				failed++
			case status == "IN_PROGRESS" || status == "QUEUED" || status == "PENDING" || status == "WAITING":
				pending++
			case state == "SUCCESS" || state == "COMPLETED":
				passed++
			case state == "FAILURE" || state == "ERROR" || state == "CANCELLED" || state == "TIMED_OUT":
				failed++
			case state == "PENDING" || state == "IN_PROGRESS" || state == "QUEUED" || state == "WAITING":
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
	cmd := fmt.Sprintf("cd %q && gh api graphql -f query=%s -f owner=%q -f name=%q -F num=%d%s",
		repoPath, shellSingleQuote(q), owner, repo, prNumber, ghJSONCmdSuffix)
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
