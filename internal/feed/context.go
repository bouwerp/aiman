package feed

import "github.com/bouwerp/aiman/internal/domain"

// SessionFacts is what context.md says about one visible session.
type SessionFacts struct {
	ID, Name, Group, Repo, Branch, Issue string
	Todos                                []string
}

// SessionFactsFrom keeps the root sessions (no ParentID, not hidden) and
// attaches their open to-do texts by session id.
func SessionFactsFrom(sessions []domain.Session, todos map[string][]string) []SessionFacts {
	var out []SessionFacts
	for _, s := range sessions {
		if s.ParentID != "" || s.Role.Hidden() {
			continue
		}
		out = append(out, SessionFacts{
			ID: s.ID, Name: s.Name, Group: s.Group, Repo: s.RepoName,
			Branch: s.Branch, Issue: s.IssueKey, Todos: todos[s.ID],
		})
	}
	return out
}
