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

// sanitizeGitBranchName converts a string to a valid git branch name
// - Replaces spaces with dashes
// - Replaces underscores with dashes (mutagen compatibility)
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

	// Replace underscores with dashes (mutagen compatibility)
	s = strings.ReplaceAll(s, "_", "-")

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
