// Package coordinator links a session's to-do items to the child sessions
// that work on them, and closes an item when its child's PR lands.
package coordinator

import "strings"

// childRefPrefix marks a to-do Ref that points at a child session.
const childRefPrefix = "child:"

// ChildBranch is the branch a child session cuts for the parent's to-do item.
func ChildBranch(parentBranch, todoID string) string {
	return parentBranch + "--" + todoID
}

// ChildRef is the to-do Ref that links an item to the child session id.
func ChildRef(id string) string {
	return childRefPrefix + id
}

// ChildID returns the child session id of a child: Ref. Any other Ref, or a
// child: Ref with no id, reports false.
func ChildID(ref string) (string, bool) {
	id, ok := strings.CutPrefix(ref, childRefPrefix)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}
