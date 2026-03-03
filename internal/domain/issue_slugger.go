package domain

import (
	"regexp"
	"strings"
)

type Slugger interface {
	Slugify(key, summary string) string
}

type GitSlugger struct{}

func NewGitSlugger() *GitSlugger {
	return &GitSlugger{}
}

func (s *GitSlugger) Slugify(key, summary string) string {
	// 1. Convert to lowercase
	summary = strings.ToLower(summary)

	// 2. Replace non-alphanumeric characters with hyphens
	re := regexp.MustCompile(`[^a-z0-9]+`)
	summary = re.ReplaceAllString(summary, "-")

	// 3. Trim hyphens from both ends
	summary = strings.Trim(summary, "-")

	// 4. Combine with key
	return key + "/" + summary
}
