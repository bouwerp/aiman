package review

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "reviews")
	want := State{Ref: "o/r#7", HeadSHA: "abc123", Revealed: true, Single: true}
	if err := SaveState(dir, "sess-1", want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadState(dir, "sess-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("state %+v, want %+v", got, want)
	}
}

func TestLoadStateMissing(t *testing.T) {
	if _, err := LoadState(t.TempDir(), "nope"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err %v, want not exist", err)
	}
}

func TestStateRefusesPathIDs(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"", "..", "a/b", `a\b`} {
		if err := SaveState(dir, id, State{}); err == nil {
			t.Errorf("SaveState accepted id %q", id)
		}
		if _, err := LoadState(dir, id); err == nil {
			t.Errorf("LoadState accepted id %q", id)
		}
	}
}
