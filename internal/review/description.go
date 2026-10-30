package review

import (
	"regexp"
	"strings"
)

// issueRefRe matches issue references in a PR body: #12, owner/repo#3 and
// Jira keys such as PROJ-1.
var issueRefRe = regexp.MustCompile(`(?:[A-Za-z0-9][\w.-]*/[\w.-]+)?#\d+\b|\b[A-Z][A-Z0-9]+-\d+\b`)

// DescriptionFileName is the file reveal writes into a review session's
// working directory.
const DescriptionFileName = ".aiman_pr_description.md"

// LinkedIssues lists the issue references in body, each once, in the order
// they first appear.
func LinkedIssues(body string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range issueRefRe.FindAllString(body, -1) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// Description is the PR description file reveal writes: the title, the base
// branch, the linked issues and the body.
func Description(pr PR) string {
	links := "none"
	if l := LinkedIssues(pr.Body); len(l) > 0 {
		links = strings.Join(l, ", ")
	}
	body := strings.TrimSpace(pr.Body)
	if body == "" {
		body = "(no description)"
	}
	var b strings.Builder
	b.WriteString("# " + strings.TrimSpace(pr.Title) + "\n\n")
	b.WriteString("Base: " + pr.BaseRef + "\n")
	b.WriteString("Linked issues: " + links + "\n\n")
	b.WriteString(body + "\n")
	return b.String()
}
