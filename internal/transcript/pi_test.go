package transcript

import (
	"fmt"
	"strings"
	"testing"
)

func piUser(id, parent, text string) string {
	return fmt.Sprintf(`{"type":"message","id":%q,"parentId":%s,"timestamp":"2026-11-07T10:00:00.000Z","message":{"role":"user","content":[{"type":"text","text":%q}]}}`, id, piParent(parent), text)
}

func piAssistantCall(id, parent, call string) string {
	return fmt.Sprintf(`{"type":"message","id":%q,"parentId":%s,"timestamp":"2026-11-07T10:00:00.000Z","message":{"role":"assistant","content":[{"type":"toolCall","id":%q,"name":"bash","arguments":{"command":"ls"}}]}}`, id, piParent(parent), call)
}

func piToolResult(id, text string) string {
	return fmt.Sprintf(`{"type":"message","id":%q,"parentId":"a","timestamp":"2026-11-07T10:00:00.000Z","message":{"role":"toolResult","toolCallId":"c1","content":[{"type":"text","text":%q}],"durationMs":7}}`, id, text)
}

func piParent(parent string) string {
	if parent == "" {
		return "null"
	}
	return fmt.Sprintf("%q", parent)
}

// piTestList applies deltas the way the index does, with each line's index as
// its position, and fails on a delta the index could not apply.
type piTestList struct {
	t     *testing.T
	n     Normaliser
	pos   int64
	items []Item
}

func newPiTestList(t *testing.T) *piTestList {
	return &piTestList{t: t, n: NewPi()}
}

func (l *piTestList) feed(lines ...string) {
	l.t.Helper()
	for _, line := range lines {
		l.pos++
		for _, d := range l.n.Feed(l.pos, []byte(line)) {
			l.apply(d)
		}
	}
}

func (l *piTestList) apply(d Delta) {
	l.t.Helper()
	i := -1
	id := d.Remove
	if d.Add != nil {
		id = d.Add.ID
	}
	if d.Update != nil {
		id = d.Update.ID
	}
	for j, it := range l.items {
		if it.ID == id {
			i = j
		}
	}
	switch {
	case d.Add != nil:
		if i >= 0 {
			l.t.Fatalf("Add of listed id %q", id)
		}
		l.items = append(l.items, *d.Add)
	case i < 0:
		l.t.Fatalf("delta on unlisted id %q: %+v", id, d)
	case d.Update != nil:
		l.items[i] = *d.Update
	default:
		l.items = append(l.items[:i], l.items[i+1:]...)
	}
}

func (l *piTestList) ids() string {
	var out []string
	for _, it := range l.items {
		out = append(out, it.ID)
	}
	return strings.Join(out, ",")
}

func TestPiMoveBackToAbandonedBranchRelists(t *testing.T) {
	l := newPiTestList(t)
	l.feed(piUser("a", "", "a"), piUser("b", "a", "b"), piUser("c", "b", "c"))
	l.feed(piUser("x", "a", "x")) // abandons b, c
	if got := l.ids(); got != "a,x" {
		t.Fatalf("after fork = %s", got)
	}
	l.feed(piUser("d", "c", "d")) // back onto b, c
	if got := l.ids(); got != "a,b,c,d" {
		t.Fatalf("after moving back = %s, want chain order", got)
	}
	if l.items[1].Rev != l.pos {
		t.Errorf("relisted item rev = %d, want %d so an after reader sees it", l.items[1].Rev, l.pos)
	}
}

func TestPiForkToRootRemovesEverything(t *testing.T) {
	l := newPiTestList(t)
	l.feed(piUser("a", "", "a"), piUser("b", "a", "b"))
	l.feed(piUser("r", "", "r"))
	if got := l.ids(); got != "r" {
		t.Errorf("after fork to root = %s", got)
	}
}

func TestPiUnknownParentContinuesBranch(t *testing.T) {
	l := newPiTestList(t)
	l.feed(piUser("a", "", "a"), piUser("b", "missing", "b"), piUser("b", "a", "dup"))
	if got := l.ids(); got != "a,b" {
		t.Errorf("ids = %s", got)
	}
	if l.items[1].Text != "b" {
		t.Errorf("a repeated id replaced the first entry: %q", l.items[1].Text)
	}
}

func TestPiForkPastResultRevertsCall(t *testing.T) {
	l := newPiTestList(t)
	l.feed(piAssistantCall("a", "", "c1"), piToolResult("r1", "first"))
	if tc := l.items[0].ToolCall; tc.Status != ToolOK || tc.Result != "first" || tc.DurationMS != 7 {
		t.Fatalf("call = %+v", tc)
	}
	// A fork from the call abandons its result, then answers it again.
	l.feed(piToolResult("r2", "second"))
	if tc := l.items[0].ToolCall; tc.Status != ToolOK || tc.Result != "second" {
		t.Errorf("after refork call = %+v", tc)
	}
	// A fork to the call with no new result leaves it running.
	l.feed(piUser("u", "a", "again"))
	tc := l.items[0].ToolCall
	if tc.Status != ToolRunning || tc.Result != "" || tc.DurationMS != 0 {
		t.Errorf("call with its result abandoned = %+v, want running", tc)
	}
	if l.items[0].Rev != l.pos {
		t.Errorf("rev = %d, want %d", l.items[0].Rev, l.pos)
	}
}

func TestPiMoveBackRestoresResult(t *testing.T) {
	l := newPiTestList(t)
	l.feed(
		piUser("u", "", "u"),
		piAssistantCall("a", "u", "c1"),
		piToolResult("r", "out"),
		piUser("x", "u", "x"), // abandons a and r
		piUser("back", "r", "back"),
	)
	if got := l.ids(); got != "u,a:0,back" {
		t.Fatalf("ids = %s", got)
	}
	if tc := l.items[1].ToolCall; tc.Status != ToolOK || tc.Result != "out" {
		t.Errorf("relisted call = %+v, want its result back", tc)
	}
}

func TestPiDeltasDoNotAliasState(t *testing.T) {
	n := NewPi()
	ds := n.Feed(1, []byte(piAssistantCall("a", "", "c1")))
	added := ds[0].Add
	n.Feed(2, []byte(piToolResult("r", "out")))
	if added.ToolCall.Status != ToolRunning || added.Rev != 1 {
		t.Errorf("an earlier delta changed: %+v rev %d", added.ToolCall, added.Rev)
	}
}

func TestPiContentShapes(t *testing.T) {
	stringUser := `{"type":"message","id":"a","parentId":null,"timestamp":"2026-11-07T10:00:00.000Z","message":{"role":"user","content":"plain"}}`
	imageOnly := `{"type":"message","id":"b","parentId":"a","timestamp":"2026-11-07T10:00:00.000Z","message":{"role":"user","content":[{"type":"image","data":"zz"}]}}`
	otherRole := `{"type":"message","id":"c","parentId":"b","timestamp":"2026-11-07T10:00:00.000Z","message":{"role":"custom","content":"hidden"}}`
	l := newPiTestList(t)
	l.feed(`not json`, stringUser, imageOnly, otherRole, piUser("d", "c", "after"))
	if got := l.ids(); got != "a,d" {
		t.Fatalf("ids = %s", got)
	}
	if l.items[0].Text != "plain" {
		t.Errorf("text = %q", l.items[0].Text)
	}
}

func TestPiResetClearsTree(t *testing.T) {
	l := newPiTestList(t)
	l.feed(piUser("a", "", "a"))
	l.n.Reset()
	l.items = nil
	l.feed(piUser("a", "", "a"))
	if got := l.ids(); got != "a" {
		t.Errorf("after Reset ids = %s", got)
	}
}
