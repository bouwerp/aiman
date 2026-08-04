package jira

import (
	"fmt"
	"regexp"
	"strings"
)

// DefaultIssueStatuses is the set of statuses that represent work in flight — the issues
// worth offering when starting a session. Backlog states ("To Do", "Later") and anything
// Done are deliberately absent. Overridden per-installation by
// integrations.jira.issue_statuses in config.yaml.
var DefaultIssueStatuses = []string{
	"Groomed",
	"Analysis In Progress",
	"Research",
	"Discovery",
	"Dev Ready",
	"In Development",
	"Dev Review",
}

// issueKeyPattern matches a Jira issue key such as AIMAN-42. Used to decide whether a
// search term can be fed to JQL's `key =` operator, which rejects non-key values outright
// (so `key = login bug` fails the whole request rather than matching nothing).
var issueKeyPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*-\d+$`)

func looksLikeIssueKey(s string) bool {
	return issueKeyPattern.MatchString(strings.TrimSpace(s))
}

// quoteJQL renders s as a double-quoted JQL string literal, escaping backslashes and
// quotes so a status name or search term cannot break out of the literal.
func quoteJQL(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// statusFilter builds the `status IN (...)` clause from the configured statuses, falling
// back to DefaultIssueStatuses when none are configured. Blank entries are dropped.
func statusFilter(statuses []string) string {
	quoted := make([]string, 0, len(statuses))
	for _, s := range statuses {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		quoted = append(quoted, quoteJQL(s))
	}
	if len(quoted) == 0 {
		for _, s := range DefaultIssueStatuses {
			quoted = append(quoted, quoteJQL(s))
		}
	}
	return "status IN (" + strings.Join(quoted, ", ") + ")"
}

// buildDefaultJQL returns the query behind the issue picker's initial list: issues
// assigned to the current user that sit in a working status.
func buildDefaultJQL(statuses []string) string {
	return fmt.Sprintf("assignee = currentUser() AND %s ORDER BY created DESC", statusFilter(statuses))
}

// buildSearchJQL returns the query for a user-typed search term. The assignee and status
// scoping is kept so the picker never offers work that is not mine or not ready to start.
func buildSearchJQL(query string, statuses []string) string {
	query = strings.TrimSpace(query)
	match := fmt.Sprintf("summary ~ %s", quoteJQL(query))
	if looksLikeIssueKey(query) {
		match = fmt.Sprintf("%s OR key = %s", match, query)
	}
	return fmt.Sprintf("assignee = currentUser() AND %s AND (%s) ORDER BY created DESC",
		statusFilter(statuses), match)
}
