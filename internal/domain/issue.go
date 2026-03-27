package domain

import (
	"regexp"
	"strings"
	"time"
)

var jiraKeyRegex = regexp.MustCompile(`[A-Z]+-[0-9]+`)

// ExtractKey attempts to find a JIRA key in a string.
func ExtractKey(s string) string {
	return jiraKeyRegex.FindString(s)
}

type IssueStatus string

func (s IssueStatus) String() string {
	return string(s)
}

const (
	IssueStatusTodo       IssueStatus = "TODO"
	IssueStatusInProgress IssueStatus = "IN_PROGRESS"
	IssueStatusDone       IssueStatus = "DONE"
)

type Issue struct {
	ID          string
	Key         string
	Summary     string
	Description string
	Status      IssueStatus
	Assignee    string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (i Issue) Slug() string {
	summary := sanitizeBranchSegment(i.Summary)
	return truncateBranchName(i.Key, summary)
}

func truncateBranchName(key, summary string) string {
	const maxLen = 63
	if key == "" {
		if len(summary) > maxLen {
			return summary[:maxLen]
		}
		return summary
	}
	base := key + "-"
	maxSummary := maxLen - len(base)
	if maxSummary <= 0 {
		return key
	}
	if len(summary) > maxSummary {
		summary = summary[:maxSummary]
	}
	branch := base + summary
	branch = strings.TrimRight(branch, "-")
	return branch
}
