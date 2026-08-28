package ui

import (
	"sort"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
)

func itemByID(flat []item) map[string]item {
	byID := make(map[string]item, len(flat))
	for _, it := range flat {
		if it.session.ID != "" {
			byID[it.session.ID] = it
		}
	}
	return byID
}

// homeRoot walks ParentID until a session with no parent in this list.
func homeRoot(it item, byID map[string]item) item {
	cur := it
	seen := map[string]bool{}
	for cur.session.ParentID != "" {
		if seen[cur.session.ID] {
			break
		}
		seen[cur.session.ID] = true
		p, ok := byID[cur.session.ParentID]
		if !ok {
			break
		}
		cur = p
	}
	return cur
}

func groupHomeKey(it item, byID map[string]item) string {
	home := homeRoot(it, byID)
	g := domain.GroupLabel(home.session.Group)
	if home.remoteName != "" {
		return g + "\x00" + home.remoteName
	}
	return g
}

func flattenSessionForest(items []item, collapsedSessions map[string]bool) []item {
	if collapsedSessions == nil {
		collapsedSessions = map[string]bool{}
	}
	byID := itemByID(items)
	children := map[string][]item{}
	var roots []item
	for _, it := range items {
		pid := it.session.ParentID
		if pid == "" {
			roots = append(roots, it)
			continue
		}
		if _, ok := byID[pid]; !ok {
			roots = append(roots, it)
			continue
		}
		children[pid] = append(children[pid], it)
	}
	sortItemsByName(roots)
	for id, kids := range children {
		sortItemsByName(kids)
		children[id] = kids
	}
	return walkSessionForest(roots, children, collapsedSessions, 0)
}

func sortItemsByName(items []item) {
	sort.Slice(items, func(i, j int) bool {
		return strings.ToLower(items[i].session.Name) < strings.ToLower(items[j].session.Name)
	})
}

func walkSessionForest(nodes []item, children map[string][]item, collapsed map[string]bool, depth int) []item {
	var out []item
	for i, it := range nodes {
		it.treeLast = i == len(nodes)-1
		it.treeDepth = depth
		kids := children[it.session.ID]
		it.hasChildren = len(kids) > 0
		// Every descendant, not just the direct ones: the number's job is to say
		// how much a collapsed row is hiding, and the deeper levels are hidden too.
		it.childN = countDescendants(it.session.ID, children)
		it.collapsed = collapsed[it.session.ID]
		out = append(out, it)
		if it.hasChildren && !it.collapsed {
			out = append(out, walkSessionForest(kids, children, collapsed, depth+1)...)
		}
	}
	return out
}

// countDescendants totals the sessions under id at every depth. The forest is
// built from parent ids that may point anywhere, so a cycle would not terminate
// on its own; seen bounds it.
func countDescendants(id string, children map[string][]item) int {
	seen := map[string]bool{id: true}
	var walk func(string) int
	walk = func(parent string) int {
		n := 0
		for _, kid := range children[parent] {
			if seen[kid.session.ID] {
				continue
			}
			seen[kid.session.ID] = true
			n += 1 + walk(kid.session.ID)
		}
		return n
	}
	return walk(id)
}
