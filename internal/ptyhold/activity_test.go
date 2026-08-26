package ptyhold

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestActivityTrackerPublishesOutputAndTitle(t *testing.T) {
	dir := t.TempDir()
	a := newActivityTracker(dir)

	a.observe([]byte("\x1b]0;◐ doing the thing\x07some output"))

	act := readActivityFile(t, dir)
	if act.Title != "◐ doing the thing" {
		t.Errorf("title = %q", act.Title)
	}
	if act.Bytes == 0 {
		t.Error("byte count not recorded")
	}
	if _, ok := act.LastOutputAt(); !ok {
		t.Errorf("last output not usable: %q", act.LastOutput)
	}
	if _, ok := act.TitleChangedAt(); !ok {
		t.Errorf("title-changed time not usable: %q", act.TitleChanged)
	}
}

// A changed title is flushed at once: it is the edge a reader is waiting for,
// and holding it back for the flush interval would delay the signal that an
// agent has started or stopped working.
func TestActivityTrackerFlushesImmediatelyOnTitleChange(t *testing.T) {
	dir := t.TempDir()
	a := newActivityTracker(dir)

	a.observe([]byte("\x1b]0;first\x07"))
	first := readActivityFile(t, dir)

	// No sleep: a second title must land regardless of the flush interval.
	a.observe([]byte("\x1b]0;second\x07"))
	second := readActivityFile(t, dir)

	if first.Title != "first" || second.Title != "second" {
		t.Fatalf("titles not published promptly: %q then %q", first.Title, second.Title)
	}
	if first.TitleChanged == second.TitleChanged && first.Title != second.Title {
		t.Error("title-changed time should move when the title does")
	}
}

// Output without a title change is throttled — a busy agent produces output many
// times a second and this file exists to be polled from another process.
func TestActivityTrackerThrottlesPlainOutput(t *testing.T) {
	dir := t.TempDir()
	a := newActivityTracker(dir)

	a.observe([]byte("first chunk")) // first flush, nothing written yet
	before := readActivityFile(t, dir)
	a.observe([]byte("second chunk"))
	after := readActivityFile(t, dir)

	if after.Bytes != before.Bytes {
		t.Errorf("a second chunk inside the flush interval should not be written: %d -> %d",
			before.Bytes, after.Bytes)
	}
	// flush publishes whatever is pending, so nothing is lost when it goes quiet.
	a.flush()
	if final := readActivityFile(t, dir); final.Bytes <= before.Bytes {
		t.Errorf("flush should publish the pending bytes: %d", final.Bytes)
	}
}

// The same title repeated is not a change: the spinner glyph moving is, and that
// distinction is the whole signal.
func TestActivityTrackerIgnoresRepeatedTitles(t *testing.T) {
	dir := t.TempDir()
	a := newActivityTracker(dir)

	a.observe([]byte("\x1b]0;steady\x07"))
	first := readActivityFile(t, dir)
	time.Sleep(5 * time.Millisecond)
	a.observe([]byte("\x1b]0;steady\x07"))
	a.flush()
	second := readActivityFile(t, dir)

	if first.TitleChanged != second.TitleChanged {
		t.Errorf("an unchanged title must not move the timestamp: %q -> %q",
			first.TitleChanged, second.TitleChanged)
	}
}

func TestReadActivityToleratesMissingAndCorruptFiles(t *testing.T) {
	root := t.TempDir()
	if got := ReadActivity(root, "nope"); got.Title != "" || got.Bytes != 0 {
		t.Errorf("a missing file should read as nothing known: %+v", got)
	}
	dir := Dir(root, "bad")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ActivityFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ReadActivity(root, "bad"); got.Title != "" {
		t.Errorf("corrupt file should read as nothing known: %+v", got)
	}
	// And the parse helpers say so rather than returning a zero time as fact.
	var empty Activity
	if _, ok := empty.LastOutputAt(); ok {
		t.Error("an empty timestamp must report unusable")
	}
	if _, ok := (Activity{LastOutput: "not a time"}).LastOutputAt(); ok {
		t.Error("an unparseable timestamp must report unusable")
	}
}

func readActivityFile(t *testing.T, dir string) Activity {
	t.Helper()
	// The tracker writes into dir directly, so read it directly.
	raw, err := os.ReadFile(filepath.Join(dir, ActivityFile))
	if err != nil {
		t.Fatalf("read activity: %v", err)
	}
	var act Activity
	if err := json.Unmarshal(raw, &act); err != nil {
		t.Fatalf("parse activity: %v", err)
	}
	return act
}
