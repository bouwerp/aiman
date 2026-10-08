package pane

import "testing"

func TestDiffScreenUnchanged(t *testing.T) {
	same, replace, rows := DiffScreen("a\nb", "a\nb")
	if !same || replace != "" || rows != nil {
		t.Fatalf("same=%v replace=%q rows=%v", same, replace, rows)
	}
}

func TestDiffScreenPatchesOneRow(t *testing.T) {
	same, replace, rows := DiffScreen("a\nb\nc", "a\nB\nc")
	if same || replace != "" {
		t.Fatalf("same=%v replace=%q", same, replace)
	}
	if len(rows) != 1 || rows[0].Index != 1 || rows[0].Line != "B" {
		t.Fatalf("rows=%v", rows)
	}
	got, ok := ApplyScreen("a\nb\nc", "", rows)
	if !ok || got != "a\nB\nc" {
		t.Fatalf("apply ok=%v got=%q", ok, got)
	}
}

func TestDiffScreenReplacesWhenShapeChanges(t *testing.T) {
	same, replace, rows := DiffScreen("a\nb", "a\nb\nc")
	if same || replace != "a\nb\nc" || rows != nil {
		t.Fatalf("same=%v replace=%q rows=%v", same, replace, rows)
	}
	got, ok := ApplyScreen("a\nb", replace, nil)
	if !ok || got != replace {
		t.Fatalf("apply ok=%v got=%q", ok, got)
	}
}

func TestDiffScreenReplacesWhenMostRowsChange(t *testing.T) {
	same, replace, rows := DiffScreen("a\nb\nc\nd", "A\nB\nC\nd")
	if same || replace != "A\nB\nC\nd" || rows != nil {
		t.Fatalf("same=%v replace=%q rows=%v", same, replace, rows)
	}
}

func TestApplyScreenRejectsARowPastTheEnd(t *testing.T) {
	_, ok := ApplyScreen("a\nb", "", []RowPatch{{Index: 4, Line: "z"}})
	if ok {
		t.Fatal("expected a patch past the end to fail")
	}
}

func TestReplyForSkipsIdenticalText(t *testing.T) {
	reply := ReplyFor("cached", "cached", ScreenHash("cached"))
	if !reply.Unchanged || reply.Text != "" || reply.Hash != ScreenHash("cached") {
		t.Fatalf("reply=%+v", reply)
	}
}

func TestReplyForPatchesAgainstRememberedText(t *testing.T) {
	prev := "one\ntwo\nthree"
	next := "one\nTWO\nthree"
	reply := ReplyFor(prev, next, ScreenHash(prev))
	if reply.Unchanged || reply.Text != "" || len(reply.Rows) != 1 || reply.Rows[0].Line != "TWO" {
		t.Fatalf("reply=%+v", reply)
	}
	if reply.Hash != ScreenHash(next) {
		t.Fatalf("hash=%s", reply.Hash)
	}
}

func TestReplyForSendsFullTextWhenTheClientHashIsUnknown(t *testing.T) {
	reply := ReplyFor("old", "new text", "not-the-old-hash")
	if reply.Unchanged || reply.Text != "new text" || reply.Rows != nil {
		t.Fatalf("reply=%+v", reply)
	}
}
