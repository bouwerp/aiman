// Package review holds the parts of a pull request review that do not depend
// on a session: the PR reference, a GitHub client and the review file.
package review

import (
	"fmt"
	"strconv"
	"strings"
)

// Ref names one pull request as owner/repo#number.
type Ref struct {
	Owner  string
	Repo   string
	Number int
}

// ParseRef parses "owner/repo#n".
func ParseRef(s string) (Ref, error) {
	slug, num, ok := strings.Cut(s, "#")
	if !ok {
		return Ref{}, fmt.Errorf("pull request %q: want owner/repo#number", s)
	}
	owner, repo, ok := strings.Cut(slug, "/")
	if !ok || owner == "" || repo == "" || strings.Contains(repo, "/") {
		return Ref{}, fmt.Errorf("pull request %q: want owner/repo#number", s)
	}
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 {
		return Ref{}, fmt.Errorf("pull request %q: number must be a positive integer", s)
	}
	return Ref{Owner: owner, Repo: repo, Number: n}, nil
}

// String returns owner/repo#number.
func (r Ref) String() string { return fmt.Sprintf("%s#%d", r.Slug(), r.Number) }

// Slug returns owner/repo.
func (r Ref) Slug() string { return r.Owner + "/" + r.Repo }
