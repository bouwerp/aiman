package ui

import (
	"fmt"
	"path"
	"sort"

	"github.com/bouwerp/aiman/internal/domain"
)

// workspaceMemberParts is one meta-line item per member of a workspace
// session, in Ord order: the member directory's name, " ↑<n>" for commits
// ahead of its base, and " ✎" when it has staged, unstaged or untracked
// files. A member with no status yet is its name alone.
func workspaceMemberParts(s domain.Session, statuses map[string]domain.GitStatus) []string {
	members := sortedMembers(s.Members)
	out := make([]string, 0, len(members))
	for _, mem := range members {
		text := memberDirName(mem)
		if st, ok := statuses[mem.WorktreePath]; ok {
			if st.AheadOfBase > 0 {
				text += fmt.Sprintf(" ↑%d", st.AheadOfBase)
			}
			if st.StagedCount+st.UnstagedCount+st.UntrackedCount > 0 {
				text += " ✎"
			}
		}
		out = append(out, text)
	}
	return out
}

func sortedMembers(members []domain.SessionRepo) []domain.SessionRepo {
	out := append([]domain.SessionRepo(nil), members...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Ord < out[j].Ord })
	return out
}

// memberDirName is the member's directory name inside the workspace, else
// the repository name when the path is not known yet.
func memberDirName(mem domain.SessionRepo) string {
	if mem.WorktreePath != "" {
		return path.Base(mem.WorktreePath)
	}
	return path.Base(mem.Repo)
}

// workspaceRepoLabel is a workspace session's repositories for a list row,
// "a, b" in Ord order.
func workspaceRepoLabel(s domain.Session) string {
	label := ""
	for i, mem := range sortedMembers(s.Members) {
		if i > 0 {
			label += ", "
		}
		label += mem.Repo
	}
	return label
}
