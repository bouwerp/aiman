package todo

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func list(ids ...string) domain.TodoList {
	var l domain.TodoList
	for _, id := range ids {
		l.Items = append(l.Items, domain.TodoItem{ID: id, Text: id, State: domain.TodoPending})
	}
	return l
}

func TestDiffReportsMove(t *testing.T) {
	ops := Diff(list("t1", "t2", "t3"), list("t3", "t1", "t2"))
	if len(ops) != 1 || ops[0] != (Op{Kind: OpMove, ID: "t3", ToIndex: 0}) {
		t.Fatalf("ops = %+v", ops)
	}
}

func TestDiffAddRemoveEditState(t *testing.T) {
	before := list("t1", "t2")
	after := list("t2", "t3")
	after.Items[0].Text = "new"
	after.Items[0].State = domain.TodoDone
	ops := Diff(before, after)
	kinds := map[OpKind]int{}
	for _, o := range ops {
		kinds[o.Kind]++
	}
	if kinds[OpRemove] != 1 || kinds[OpAdd] != 1 || kinds[OpEdit] != 1 || kinds[OpState] != 1 || kinds[OpMove] != 0 {
		t.Fatalf("ops = %+v", ops)
	}
}

func TestSteersAgent(t *testing.T) {
	ref := []Op{{Kind: OpRef}}
	mixed := []Op{{Kind: OpRef}, {Kind: OpAdd}}
	cases := []struct {
		c    Change
		want bool
	}{
		{Change{Author: domain.AuthorUser, Ops: mixed}, true},
		{Change{Author: domain.AuthorUser, Ops: ref}, false},
		{Change{Author: domain.AuthorAgent, Ops: mixed}, false},
		{Change{Author: domain.AuthorUser}, false},
	}
	for i, c := range cases {
		if got := c.c.SteersAgent(); got != c.want {
			t.Errorf("case %d: got %v", i, got)
		}
	}
}
