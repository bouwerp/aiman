// Package todo stores per-session to-do lists and describes changes to them.
package todo

import "github.com/bouwerp/aiman/internal/domain"

// OpKind is the kind of one list operation.
type OpKind string

const (
	OpAdd    OpKind = "add"
	OpEdit   OpKind = "edit"
	OpState  OpKind = "state"
	OpRemove OpKind = "remove"
	OpMove   OpKind = "move"
	// OpRef records a changed Ref link. It is stored but never steers the agent.
	OpRef OpKind = "ref"
)

// Op is one operation of a Change.
type Op struct {
	Kind    OpKind
	ID      string
	Text    string
	State   domain.TodoState
	ToIndex int
	Ref     string
}

// Change describes what a mutation did.
type Change struct {
	Author domain.TodoAuthor
	Ops    []Op
	Rev    int64
}

// Empty reports whether the change has no operations.
func (c Change) Empty() bool { return len(c.Ops) == 0 }

// SteersAgent reports whether the change should be relayed to the agent:
// not when the agent made it, and not when it only changed Ref links.
func (c Change) SteersAgent() bool {
	if c.Author == domain.AuthorAgent {
		return false
	}
	for _, op := range c.Ops {
		if op.Kind != OpRef {
			return true
		}
	}
	return false
}

// Diff returns the operations that turn before into after, matching items by id.
// Moves are the surviving items outside the longest increasing subsequence of
// their before-positions; ToIndex is the item's 0-based index in after.
func Diff(before, after domain.TodoList) []Op {
	prev := make(map[string]int, len(before.Items))
	for i, it := range before.Items {
		prev[it.ID] = i
	}
	cur := make(map[string]bool, len(after.Items))
	for _, it := range after.Items {
		cur[it.ID] = true
	}

	var ops []Op
	for _, it := range before.Items {
		if !cur[it.ID] {
			ops = append(ops, Op{Kind: OpRemove, ID: it.ID})
		}
	}
	for _, it := range after.Items {
		if _, ok := prev[it.ID]; !ok {
			ops = append(ops, Op{Kind: OpAdd, ID: it.ID, Text: it.Text, State: it.State, Ref: it.Ref})
		}
	}
	for _, it := range after.Items {
		i, ok := prev[it.ID]
		if !ok {
			continue
		}
		old := before.Items[i]
		if old.Text != it.Text {
			ops = append(ops, Op{Kind: OpEdit, ID: it.ID, Text: it.Text})
		}
		if old.State != it.State {
			ops = append(ops, Op{Kind: OpState, ID: it.ID, State: it.State})
		}
		if old.Ref != it.Ref {
			ops = append(ops, Op{Kind: OpRef, ID: it.ID, Ref: it.Ref})
		}
	}
	return append(ops, moveOps(prev, after)...)
}

func moveOps(prev map[string]int, after domain.TodoList) []Op {
	var ids []string
	var idx, pos []int
	for i, it := range after.Items {
		if p, ok := prev[it.ID]; ok {
			ids = append(ids, it.ID)
			idx = append(idx, i)
			pos = append(pos, p)
		}
	}
	stay := lisMembers(pos)
	var ops []Op
	for k, id := range ids {
		if !stay[k] {
			ops = append(ops, Op{Kind: OpMove, ID: id, ToIndex: idx[k]})
		}
	}
	return ops
}

// lisMembers returns the indices of one longest strictly increasing subsequence of seq.
func lisMembers(seq []int) map[int]bool {
	n := len(seq)
	length := make([]int, n)
	from := make([]int, n)
	best := -1
	for i := range seq {
		length[i], from[i] = 1, -1
		for j := 0; j < i; j++ {
			if seq[j] < seq[i] && length[j]+1 > length[i] {
				length[i], from[i] = length[j]+1, j
			}
		}
		if best < 0 || length[i] > length[best] {
			best = i
		}
	}
	out := map[int]bool{}
	for k := best; k >= 0; k = from[k] {
		out[k] = true
	}
	return out
}
