// Package coordinator links a session's to-do items to the child sessions
// that work on them, and closes an item when its child's PR lands.
package coordinator

import "strings"

// childRefPrefix marks a to-do Ref that points at a child session.
const childRefPrefix = "child:"

// fixerRefPrefix marks a to-do Ref that points at a review's fixer session.
const fixerRefPrefix = "fixer:"

// ChildBranch is the branch a child session cuts for the parent's to-do item.
func ChildBranch(parentBranch, todoID string) string {
	return parentBranch + "--" + todoID
}

// ChildRef is the to-do Ref that links an item to the child session id.
func ChildRef(id string) string {
	return childRefPrefix + id
}

// FixerRef is the to-do Ref linking an item to a review's fixer session.
func FixerRef(id string) string {
	return fixerRefPrefix + id
}

// FixerID returns the fixer session id of a fixer: Ref. Any other Ref, or a
// fixer: Ref with no id, reports false.
func FixerID(ref string) (string, bool) {
	id, ok := strings.CutPrefix(ref, fixerRefPrefix)
	if !ok || id == "" {
		return "", false
	}
	return id, true
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
