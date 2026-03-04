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
	// Sanitize summary for git branch name compatibility
	summary := sanitizeGitBranchName(i.Summary)
	return i.Key + "-" + summary
}

// sanitizeGitBranchName converts a string to a valid git branch name
// - Replaces spaces with dashes
// - Removes invalid characters: ~^:\ and control characters
// - Removes consecutive dots
// - Ensures it doesn't start with a dash
// - Ensures it doesn't end with a dot
func sanitizeGitBranchName(s string) string {
	if s == "" {
		return ""
	}

	// Replace spaces with dashes
	s = strings.ReplaceAll(s, " ", "-")

	// Remove invalid characters: ~^:\ and control characters
	// Also remove other problematic chars
	invalidChars := regexp.MustCompile(`[\x00-\x1f\x7f~^:\\@\{\}\[\]\*\?\|<>"'!]`)
	s = invalidChars.ReplaceAllString(s, "")

	// Remove consecutive dots
	s = regexp.MustCompile(`\.\.+`).ReplaceAllString(s, ".")

	// Remove leading dashes (git branch names cannot start with -)
	s = strings.TrimLeft(s, "-")

	// Remove trailing dots (git branch names cannot end with .)
	s = strings.TrimRight(s, ".")

	// Remove leading/trailing whitespace (just in case)
	s = strings.TrimSpace(s)

	// Collapse multiple dashes into one
	s = regexp.MustCompile(`-+`).ReplaceAllString(s, "-")

	return s
}
