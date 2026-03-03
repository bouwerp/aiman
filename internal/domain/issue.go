package domain

import (
	"regexp"
	"time"
)

var jiraKeyRegex = regexp.MustCompile(`[A-Z]+-[0-9]+`)

// ExtractKey attempts to find a JIRA key in a string.
func ExtractKey(s string) string {
	return jiraKeyRegex.FindString(s)
}

type IssueStatus string

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
	// Logic to transform title to branch name will be implemented here
	// (Step 2 of SPEC)
	return i.Key + "-" + i.Summary
}
