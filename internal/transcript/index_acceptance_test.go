//go:build acceptance

package transcript_test

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/transcript"
)

func TestIndex(t *testing.T) {
	path, ix, _ := newIndex(t, "A 1 one\nA 2 two\n", transcript.IndexOptions{})
	p := mustPage(t, ix, nil, 0)
	if got := ids(p.Items); got != "1,2" {
		t.Fatalf("items = %q, want 1,2", got)
	}
	if p.Items[0].Text != "one" || p.Items[0].Rev != 8 || p.Items[1].Rev != 16 {
		t.Errorf("first items = %+v", p.Items)
	}
	if p.Type != transcript.PageType || p.Source != transcript.SourceTranscript || p.Capabilities != fakeCaps {
		t.Errorf("page header = %q %q %+v", p.Type, p.Source, p.Capabilities)
	}
	if ix.FileID() == "" || p.After.FileID != ix.FileID() {
		t.Errorf("after file id %q, index %q", p.After.FileID, ix.FileID())
	}
	if p.After.Kind != transcript.PositionOffset || p.After.Position != fileSize(t, path) {
		t.Errorf("after = %+v, want offset %d", p.After, fileSize(t, path))
	}
	if p.Before != nil || p.Reset {
		t.Errorf("before = %v reset = %v, want nil false", p.Before, p.Reset)
	}
	if it, ok := ix.Item("2"); !ok || it.Text != "two" {
		t.Errorf("Item(2) = %+v %v", it, ok)
	}

	id := ix.FileID()
	appendFile(t, path, "A 3 three\n")
	reset, err := ix.Refresh()
	if err != nil || reset {
		t.Fatalf("Refresh after append = %v %v, want false nil", reset, err)
	}
	if got := ids(mustPage(t, ix, nil, 0).Items); got != "1,2,3" {
		t.Errorf("after append items = %q", got)
	}
	if ix.FileID() != id {
		t.Errorf("file id moved on append: %q -> %q", id, ix.FileID())
	}

	writeFile(t, path, "A 1 one\n")
	reset, err = ix.Refresh()
	if err != nil || !reset {
		t.Fatalf("Refresh after shrink = %v %v, want true nil", reset, err)
	}
	if got := ids(mustPage(t, ix, nil, 0).Items); got != "1" {
		t.Errorf("after shrink items = %q", got)
	}
}

func TestRewriteKeepsHead(t *testing.T) {
	for name, rewrite := range map[string]string{
		"same size": "A 1 one\nA 2 TWO\nA 3 xyz\n",
		"grown":     "A 1 one\nA 5 fiv\nA 6 six\nA 7 sev\n",
	} {
		t.Run(name, func(t *testing.T) {
			path, ix, fake := newIndex(t, "A 1 one\nA 2 two\nA 3 abc\n", transcript.IndexOptions{})
			old := mustPage(t, ix, nil, 0).After
			writeFile(t, path, rewrite)
			reset, err := ix.Refresh()
			if err != nil || !reset {
				t.Fatalf("Refresh after rewrite = %v %v, want true nil", reset, err)
			}
			if fake.resets == 0 || len(fake.dups) > 0 {
				t.Errorf("normaliser not reset before reread: resets %d, dup adds %v", fake.resets, fake.dups)
			}
			want := "1,2,3"
			if name == "grown" {
				want = "1,5,6,7"
			}
			if got := ids(mustPage(t, ix, nil, 0).Items); got != want {
				t.Errorf("items = %q, want %q", got, want)
			}
			a := mustAfter(t, ix, old)
			if !a.Reset || ids(a.Items) != want {
				t.Errorf("After(pre-rewrite cursor) reset=%v items=%q, want true %q", a.Reset, ids(a.Items), want)
			}
			if it, _ := ix.Item("3"); name == "same size" && it.Text != "xyz" {
				t.Errorf("Item(3) text = %q, want xyz", it.Text)
			}
		})
	}
}

func TestPartialLine(t *testing.T) {
	path, ix, _ := newIndex(t, "A 1 one\nA 2 tw", transcript.IndexOptions{})
	p := mustPage(t, ix, nil, 0)
	if ids(p.Items) != "1" || p.After.Position != 8 {
		t.Fatalf("items %q after %d, want 1 and 8", ids(p.Items), p.After.Position)
	}
	if reset, err := ix.Refresh(); err != nil || reset {
		t.Fatalf("second Refresh = %v %v", reset, err)
	}
	if ids(mustPage(t, ix, nil, 0).Items) != "1" {
		t.Fatal("partial line consumed on a second Refresh")
	}
	appendFile(t, path, "o\n")
	a := mustAfter(t, ix, p.After)
	if a.Reset || ids(a.Items) != "2" || a.Items[0].Text != "two" {
		t.Errorf("After completion: reset=%v items=%+v", a.Reset, a.Items)
	}
}

func TestPage(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 250; i++ {
		fmt.Fprintf(&b, "A %d text-%d\n", i, i)
	}
	_, ix, _ := newIndex(t, b.String(), transcript.IndexOptions{})

	for limit, want := range map[int]int{0: transcript.DefaultLimit, -1: transcript.DefaultLimit, 10: 10, 500: transcript.MaxLimit} {
		if got := len(mustPage(t, ix, nil, limit).Items); got != want {
			t.Errorf("limit %d: %d items, want %d", limit, got, want)
		}
	}

	var seen []transcript.Item
	var before *transcript.Cursor
	for pages := 0; ; pages++ {
		if pages > 10 {
			t.Fatal("paging never reached the top")
		}
		p := mustPage(t, ix, before, 0)
		if pages == 0 && p.Items[len(p.Items)-1].ID != "249" {
			t.Errorf("newest page ends at %s, want 249", p.Items[len(p.Items)-1].ID)
		}
		seen = append(append([]transcript.Item(nil), p.Items...), seen...)
		if p.Before == nil {
			break
		}
		before = p.Before
	}
	if len(seen) != 250 {
		t.Fatalf("paged %d items, want 250", len(seen))
	}
	for i, it := range seen {
		if it.ID != fmt.Sprint(i) {
			t.Fatalf("item %d is %s: not newest-last without gaps", i, it.ID)
		}
	}
}

func TestPageByteCap(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 20; i++ {
		fmt.Fprintf(&b, "A %d %s\n", i, strings.Repeat("x", 1000))
	}
	const capBytes = 4500
	_, ix, _ := newIndex(t, b.String(), transcript.IndexOptions{MaxPageBytes: capBytes})
	var before *transcript.Cursor
	total := 0
	for pages := 0; ; pages++ {
		if pages > 25 {
			t.Fatal("paging never reached the top")
		}
		p := mustPage(t, ix, before, 50)
		if len(p.Items) == 0 || len(p.Items) >= 20 {
			t.Fatalf("page %d holds %d items, want 1..19", pages, len(p.Items))
		}
		raw, _ := json.Marshal(p.Items)
		if len(raw) > capBytes {
			t.Errorf("page %d items encode to %d bytes, cap %d", pages, len(raw), capBytes)
		}
		total += len(p.Items)
		if p.Before == nil {
			break
		}
		before = p.Before
	}
	if total != 20 {
		t.Errorf("paged %d items, want 20", total)
	}
}

func TestAfterOffscreenChange(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "A %d t%d\n", i, i)
	}
	path, ix, _ := newIndex(t, b.String(), transcript.IndexOptions{})
	p := mustPage(t, ix, nil, 10)
	if ids(p.Items) != "90,91,92,93,94,95,96,97,98,99" {
		t.Fatalf("window = %q", ids(p.Items))
	}
	appendFile(t, path, "U 5 done\nU 95 seen\nA 100 new\nR 7\n")
	a := mustAfter(t, ix, p.After)
	if a.Reset {
		t.Fatal("unexpected reset")
	}
	if ids(a.Items) != "100" {
		t.Errorf("items = %q, want 100 only", ids(a.Items))
	}
	changed := map[string]transcript.Item{}
	for _, it := range a.Changed {
		changed[it.ID] = it
	}
	if len(changed) != 2 || changed["5"].Text != "done" || changed["95"].Text != "seen" {
		t.Errorf("changed = %+v, want 5 done and 95 seen", a.Changed)
	}
	if changed["5"].Rev <= p.After.Position {
		t.Errorf("changed rev %d not past cursor %d", changed["5"].Rev, p.After.Position)
	}
	if strings.Join(a.Removed, ",") != "7" {
		t.Errorf("removed = %v, want [7]", a.Removed)
	}
	if a.After.Position != fileSize(t, path) {
		t.Errorf("after = %d, want %d", a.After.Position, fileSize(t, path))
	}
	again := mustAfter(t, ix, a.After)
	if len(again.Items)+len(again.Changed)+len(again.Removed) != 0 || again.Reset {
		t.Errorf("idle After = %+v, want empty", again)
	}
}

func TestRewind(t *testing.T) {
	path, ix, _ := newIndex(t, "A 1 one\nA 2 two\nA 3 three\n", transcript.IndexOptions{})
	c := mustPage(t, ix, nil, 0).After
	appendFile(t, path, "R 3\nR 2\nA 4 four\n")
	a := mustAfter(t, ix, c)
	removed := strings.Join(a.Removed, ",")
	if removed != "3,2" && removed != "2,3" {
		t.Errorf("removed = %q, want 2 and 3", removed)
	}
	if ids(a.Items) != "4" {
		t.Errorf("items = %q, want 4", ids(a.Items))
	}
	if got := ids(mustPage(t, ix, nil, 0).Items); got != "1,4" {
		t.Errorf("page = %q, want 1,4", got)
	}
	if _, ok := ix.Item("2"); ok {
		t.Error("removed item 2 still returned by Item")
	}
}

func TestFileSwitch(t *testing.T) {
	path, ix, _ := newIndex(t, "A 1 one\nA 2 two\n", transcript.IndexOptions{})
	c := mustPage(t, ix, nil, 0).After
	for name, bad := range map[string]transcript.Cursor{
		"other file": {FileID: "other", Kind: transcript.PositionOffset, Position: c.Position},
		"other kind": {FileID: c.FileID, Kind: transcript.PositionOrdinal, Position: c.Position},
	} {
		a := mustAfter(t, ix, bad)
		if !a.Reset || ids(a.Items) != "1,2" {
			t.Errorf("%s: After reset=%v items=%q, want true 1,2", name, a.Reset, ids(a.Items))
		}
		p := mustPage(t, ix, &bad, 0)
		if !p.Reset || ids(p.Items) != "1,2" {
			t.Errorf("%s: Page reset=%v items=%q, want true 1,2", name, p.Reset, ids(p.Items))
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeFile(t, path, "A 9 a new conversation\n")
	a := mustAfter(t, ix, c)
	if !a.Reset || ids(a.Items) != "9" {
		t.Errorf("replaced file: reset=%v items=%q, want true 9", a.Reset, ids(a.Items))
	}
	if ix.FileID() == c.FileID {
		t.Error("file id unchanged after the file was replaced")
	}
}
