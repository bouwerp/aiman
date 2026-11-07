//go:build acceptance

package transcript_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/transcript"
)

// fakeNormaliser reads "A <id> <text>" (add a user item), "U <id> <text>"
// (update an existing item's text) and "R <id>" (remove), so index tests need
// no vendor format.
type fakeNormaliser struct {
	items  map[string]transcript.Item
	resets int
	dups   []string
}

func newFake() *fakeNormaliser {
	return &fakeNormaliser{items: map[string]transcript.Item{}}
}

var fakeCaps = transcript.Capabilities{
	Agent:       "fake",
	Scrollback:  transcript.ScrollbackTranscript,
	ToolResults: true,
	Thinking:    transcript.ThinkingNone,
	Subagents:   false,
	Processes:   transcript.ProcessesNone,
}

func (f *fakeNormaliser) Kind() string                          { return "fake" }
func (f *fakeNormaliser) Capabilities() transcript.Capabilities { return fakeCaps }
func (f *fakeNormaliser) PositionKind() string                  { return transcript.PositionOffset }
func (f *fakeNormaliser) Flush() []transcript.Delta             { return nil }

func (f *fakeNormaliser) Reset() {
	f.resets++
	f.items = map[string]transcript.Item{}
}

func (f *fakeNormaliser) Feed(pos int64, line []byte) []transcript.Delta {
	parts := strings.SplitN(string(line), " ", 3)
	if len(parts) < 2 {
		return nil
	}
	id, text := parts[1], ""
	if len(parts) == 3 {
		text = parts[2]
	}
	switch parts[0] {
	case "A":
		if _, ok := f.items[id]; ok {
			f.dups = append(f.dups, id)
		}
		it := transcript.Item{ID: id, Kind: transcript.KindUser, Rev: pos, Text: text}
		f.items[id] = it
		return []transcript.Delta{{Add: &it}}
	case "U":
		it, ok := f.items[id]
		if !ok {
			return nil
		}
		it.Text, it.Rev = text, pos
		f.items[id] = it
		return []transcript.Delta{{Update: &it}}
	case "R":
		if _, ok := f.items[id]; !ok {
			return nil
		}
		delete(f.items, id)
		return []transcript.Delta{{Remove: id}}
	}
	return nil
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
}

// newIndex writes content to a temp file and returns a refreshed index.
func newIndex(t *testing.T, content string, opts transcript.IndexOptions) (string, *transcript.Index, *fakeNormaliser) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.jsonl")
	writeFile(t, path, content)
	fake := newFake()
	ix := transcript.NewIndex(path, fake, opts)
	if _, err := ix.Refresh(); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	return path, ix, fake
}

func mustPage(t *testing.T, ix *transcript.Index, before *transcript.Cursor, limit int) transcript.Page {
	t.Helper()
	p, err := ix.Page(before, limit)
	if err != nil {
		t.Fatalf("Page: %v", err)
	}
	return p
}

func mustAfter(t *testing.T, ix *transcript.Index, c transcript.Cursor) transcript.Page {
	t.Helper()
	p, err := ix.After(c, 0)
	if err != nil {
		t.Fatalf("After: %v", err)
	}
	return p
}

func ids(items []transcript.Item) string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return strings.Join(out, ",")
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}
