package transcript_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/bouwerp/aiman/internal/transcript"
)

// scriptNorm reads whitespace-separated ops per line: "+id" adds, "~id" updates
// with the line's position in the text, "-id" removes, and "hold:id" holds an
// add back for Flush. Tracking of what exists is left to the index.
type scriptNorm struct {
	held []transcript.Delta
}

func (n *scriptNorm) Kind() string { return "script" }
func (n *scriptNorm) Capabilities() transcript.Capabilities {
	return transcript.Capabilities{Agent: "script"}
}
func (n *scriptNorm) PositionKind() string { return transcript.PositionOffset }
func (n *scriptNorm) Reset()               { n.held = nil }

func (n *scriptNorm) Flush() []transcript.Delta {
	held := n.held
	n.held = nil
	return held
}

func (n *scriptNorm) Feed(pos int64, line []byte) []transcript.Delta {
	var out []transcript.Delta
	for _, op := range strings.Fields(strings.TrimSpace(strings.SplitN(string(line), "#", 2)[0])) {
		switch {
		case strings.HasPrefix(op, "hold:"):
			n.held = append(n.held, transcript.Delta{Add: &transcript.Item{ID: op[5:], Kind: transcript.KindAssistant}})
		case op[0] == '+':
			out = append(out, transcript.Delta{Add: &transcript.Item{ID: op[1:], Kind: transcript.KindAssistant, Text: "v0"}})
		case op[0] == '~':
			out = append(out, transcript.Delta{Update: &transcript.Item{ID: op[1:], Text: fmt.Sprint("v", pos)}})
		case op[0] == '-':
			out = append(out, transcript.Delta{Remove: op[1:]})
		}
	}
	return out
}

func scriptIndex(t *testing.T, content string) (string, *transcript.Index) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, transcript.NewIndex(path, &scriptNorm{}, transcript.IndexOptions{})
}

func addLines(t *testing.T, path, content string) {
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

func itemIDs(items []transcript.Item) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

// One line can add several items; paging must not lose the ones a limit or the
// byte cap puts on the far side of a page boundary.
func TestPageSplitsOneLine(t *testing.T) {
	big := strings.Repeat("x", 10)
	_, ix := scriptIndex(t, "+a\n+b +c +d +e #"+big+"\n+f\n")
	var got []string
	var before *transcript.Cursor
	for pages := 0; pages < 10; pages++ {
		p, err := ix.Page(before, 2)
		if err != nil {
			t.Fatal(err)
		}
		if p.Reset {
			t.Fatal("unexpected reset")
		}
		got = append(itemIDs(p.Items), got...)
		if p.Before == nil {
			break
		}
		// Through its encoding, as a phone holds it.
		c, err := transcript.DecodeCursor(transcript.EncodeCursor(*p.Before))
		if err != nil {
			t.Fatal(err)
		}
		before = &c
	}
	if want := []string{"a", "b", "c", "d", "e", "f"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("paged %v, want %v", got, want)
	}
}

// An After cut by its limit must deliver each new item as new, and each change
// exactly when it is past the cursor, whatever line the cut falls on.
func TestAfterCutByLimit(t *testing.T) {
	path, ix := scriptIndex(t, "+old\n")
	p, err := ix.Page(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	addLines(t, path, "+n1 +n2 +n3\n~n1\n~old\n+n4\n-n2\n+n5\n")
	var added, changed, removed []string
	c := p.After
	for calls := 0; ; calls++ {
		if calls > 10 {
			t.Fatal("After never drained")
		}
		a, err := ix.After(c, 2)
		if err != nil {
			t.Fatal(err)
		}
		if a.Reset {
			t.Fatal("unexpected reset")
		}
		if len(a.Items)+len(a.Changed)+len(a.Removed) == 0 {
			break
		}
		if len(a.Items)+len(a.Changed) > 2 {
			t.Fatalf("reply over the limit: %+v", a)
		}
		added = append(added, itemIDs(a.Items)...)
		changed = append(changed, itemIDs(a.Changed)...)
		removed = append(removed, a.Removed...)
		c = a.After
	}
	if want := []string{"n1", "n3", "n4", "n5"}; !reflect.DeepEqual(added, want) {
		t.Errorf("new = %v, want %v (n2 was removed before it was seen)", added, want)
	}
	if !strings.Contains(strings.Join(changed, ","), "old") {
		t.Errorf("changed = %v, want old", changed)
	}
	if want := []string{"n2"}; !reflect.DeepEqual(removed, want) {
		t.Errorf("removed = %v, want %v", removed, want)
	}
	if st, _ := os.Stat(path); c.Position != st.Size() {
		t.Errorf("drained at %d, file is %d", c.Position, st.Size())
	}
}

// Codex ordinals start at 0, so the cursor for an empty file must sit below it,
// and they never compare with bytes.
func TestOrdinalFromLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ix := transcript.NewIndex(path, ordNorm{}, transcript.IndexOptions{})
	empty, err := ix.Page(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if empty.After.Kind != transcript.PositionOrdinal || empty.After.Position != -1 {
		t.Fatalf("empty after = %+v", empty.After)
	}
	addLines(t, path, `{"ordinal":0,"op":"a"}`+"\n"+`{"ordinal":1,"op":"b"}`+"\n")
	p, err := ix.Page(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemIDs(p.Items); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("items = %v", got)
	}
	if p.Items[0].Rev != 0 || p.Items[1].Rev != 1 || p.After.Position != 1 {
		t.Errorf("revs %d %d after %d, want 0 1 1", p.Items[0].Rev, p.Items[1].Rev, p.After.Position)
	}
	// No ordinal, then one that goes backwards: positions still move, so a
	// cursor at 1 sees both.
	addLines(t, path, `{"op":"c"}`+"\n"+`{"ordinal":1,"op":"d"}`+"\n")
	a, err := ix.After(p.After, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemIDs(a.Items); !reflect.DeepEqual(got, []string{"c", "d"}) || a.After.Position != 3 {
		t.Errorf("after = %v at %d, want c d at 3", got, a.After.Position)
	}
}

type ordNorm struct{}

func (ordNorm) Kind() string                          { return "ord" }
func (ordNorm) Capabilities() transcript.Capabilities { return transcript.Capabilities{} }
func (ordNorm) PositionKind() string                  { return transcript.PositionOrdinal }
func (ordNorm) Flush() []transcript.Delta             { return nil }
func (ordNorm) Reset()                                {}
func (ordNorm) Feed(pos int64, line []byte) []transcript.Delta {
	var v struct {
		Op string `json:"op"`
	}
	if err := json.Unmarshal(line, &v); err != nil {
		return nil
	}
	return []transcript.Delta{{Add: &transcript.Item{ID: v.Op}}}
}

func TestIndexDeltaRules(t *testing.T) {
	path, ix := scriptIndex(t, "+a ~ghost -ghost\n+a\n")
	p, err := ix.Page(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemIDs(p.Items); !reflect.DeepEqual(got, []string{"a"}) || p.Items[0].Rev != p.After.Position {
		t.Errorf("repeat add: items %v rev %d, want one a at the end", got, p.Items[0].Rev)
	}

	// Leaving the branch and coming back after the reader's cursor is in both
	// lists, and the reader applies Removed first.
	addLines(t, path, "+b\n-a\n+a\n")
	a, err := ix.After(p.After, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemIDs(a.Items); !reflect.DeepEqual(got, []string{"b", "a"}) || !reflect.DeepEqual(a.Removed, []string{"a"}) {
		t.Errorf("came back: items %v removed %v", got, a.Removed)
	}
	if got, err := ix.Page(nil, 0); err != nil || !reflect.DeepEqual(itemIDs(got.Items), []string{"b", "a"}) {
		t.Errorf("page = %v %v, want b a", itemIDs(got.Items), err)
	}
}

// A group held to Flush reaches a reader with the read that produced it, and
// its cursor is past it.
func TestFlush(t *testing.T) {
	path, ix := scriptIndex(t, "hold:g\n")
	p, err := ix.Page(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemIDs(p.Items); !reflect.DeepEqual(got, []string{"g"}) || p.Items[0].Rev != 7 {
		t.Fatalf("flushed items %v, want g at rev 7: %+v", got, p.Items)
	}
	addLines(t, path, "~g\n")
	a, err := ix.After(p.After, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Items) != 0 || !reflect.DeepEqual(itemIDs(a.Changed), []string{"g"}) {
		t.Errorf("after = %+v, want g changed", a)
	}
}

func TestToolFieldsCapped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("é", 100)
	tc := &transcript.ToolCall{Input: long, Result: long, Summary: long}
	ix := transcript.NewIndex(path, toolNorm{tc}, transcript.IndexOptions{MaxFieldBytes: 11})
	if _, err := ix.Refresh(); err != nil {
		t.Fatal(err)
	}
	it, ok := ix.Item("t")
	if !ok || !it.Truncated || it.ToolCall.Input != strings.Repeat("é", 5) || it.ToolCall.Result != strings.Repeat("é", 5) {
		t.Errorf("capped = %+v %+v", it, it.ToolCall)
	}
	if tc.Input != long {
		t.Error("the cap changed the normaliser's own value")
	}
}

type toolNorm struct{ tc *transcript.ToolCall }

func (toolNorm) Kind() string                          { return "tool" }
func (toolNorm) Capabilities() transcript.Capabilities { return transcript.Capabilities{} }
func (toolNorm) PositionKind() string                  { return transcript.PositionOffset }
func (toolNorm) Flush() []transcript.Delta             { return nil }
func (toolNorm) Reset()                                {}
func (n toolNorm) Feed(pos int64, line []byte) []transcript.Delta {
	return []transcript.Delta{{Add: &transcript.Item{ID: "t", ToolCall: n.tc}}}
}

func TestMissingFile(t *testing.T) {
	path, ix := scriptIndex(t, "+a\n")
	if _, err := ix.Refresh(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Refresh(); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Refresh on a gone file = %v, want not-exist", err)
	}
	if _, ok := ix.Item("a"); !ok {
		t.Error("items lost with the file")
	}
}

// Run with -race: a writer appending and refreshing while readers page.
func TestConcurrentReaders(t *testing.T) {
	path, ix := scriptIndex(t, "+seed\n")
	if _, err := ix.Refresh(); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				p, err := ix.Page(nil, 5)
				if err != nil {
					t.Error(err)
					return
				}
				if _, err := ix.After(p.After, 5); err != nil {
					t.Error(err)
					return
				}
				ix.Item("seed")
				ix.FileID()
			}
		}()
	}
	for i := range 200 {
		addLines(t, path, fmt.Sprintf("+i%d ~seed\n", i))
		if _, err := ix.Refresh(); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestCursorDecodeKeepsSub(t *testing.T) {
	var c transcript.Cursor
	// Position -1 is the empty ordinal index; below that is not a position.
	bad := transcript.EncodeCursor(transcript.Cursor{FileID: "f", Kind: transcript.PositionOrdinal, Position: -2})
	if _, err := transcript.DecodeCursor(bad); err == nil {
		t.Error("position -2 accepted")
	}
	good := transcript.EncodeCursor(transcript.Cursor{FileID: "f", Kind: transcript.PositionOrdinal, Position: -1})
	if err := c.UnmarshalText([]byte(good)); err != nil || c.Position != -1 {
		t.Errorf("position -1 = %+v %v", c, err)
	}
}

func TestLocateRefusesEscapes(t *testing.T) {
	home := t.TempDir()
	secret := filepath.Join(home, ".claude", "secret.jsonl")
	if err := os.MkdirAll(filepath.Dir(secret), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", ".", "..", "../../secret", `a\b`, "*"} {
		if got, _, err := transcript.Locate(home, "claude", id, "", "/x"); err == nil {
			t.Errorf("id %q located %q", id, got)
		}
	}
	// A glob character in an id is literal.
	match := filepath.Join(home, ".codex/sessions/2026/11/07/rollout-t-real.jsonl")
	if err := os.MkdirAll(filepath.Dir(match), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(match, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _, err := transcript.Locate(home, "codex", "*", "", ""); err == nil {
		t.Errorf("glob id located %q", got)
	}
	if got, _, err := transcript.Locate(home, "codex", "real", "", ""); err != nil || got != match {
		t.Errorf("codex needs no cwd: %q %v", got, err)
	}
}

// Every vendor shape in the fixture keeps its structure and loses its content.
func TestRedactFixture(t *testing.T) {
	src := filepath.Join("testdata", "redact", "vendors.jsonl")
	dst := filepath.Join(t.TempDir(), "out.jsonl")
	if err := transcript.RedactFile(src, dst); err != nil {
		t.Fatal(err)
	}
	in, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"alice", "id_rsa", "BEGIN KEY", "private reasoning", "thread-1", "toolu_1", "whoami", "2026-11-07"} {
		if strings.Contains(string(out), s) {
			t.Errorf("%q survived", s)
		}
	}
	inLines := strings.Split(strings.TrimSpace(string(in)), "\n")
	outLines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(inLines) != len(outLines) {
		t.Fatalf("%d lines became %d", len(inLines), len(outLines))
	}
	for i := range inLines {
		var a, b any
		if err := json.Unmarshal([]byte(inLines[i]), &a); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(outLines[i]), &b); err != nil {
			t.Fatal(err)
		}
		if shape(a) != shape(b) {
			t.Errorf("line %d shape\n in %s\nout %s", i, shape(a), shape(b))
		}
	}
	if !strings.Contains(string(out), `"sessionUpdate":"tool_call_update"`) {
		t.Error("enum value lost")
	}
	if transcript.Redact([]byte("not json")) != nil {
		t.Error("a line that is not JSON came through")
	}
}

func TestRedactKeepsOrderAndNumbers(t *testing.T) {
	in := `{"z":"a","a":1.50,"m":[true,null,{"y":"b","b":"","n":12345678901234567890}],"type":"t","kind":["x"],"state":{"v":"c"}}`
	got := regexp.MustCompile(`"r[0-9a-f]{16}"`).ReplaceAllString(string(transcript.Redact([]byte(in))), `"S"`)
	want := `{"z":"S","a":1.50,"m":[true,null,{"y":"S","b":"","n":12345678901234567890}],"type":"t","kind":["S"],"state":{"v":"S"}}`
	if got != want {
		t.Errorf("redacted\n got %s\nwant %s", got, want)
	}
	for _, bad := range []string{`{"a":"secret"} trailing`, `{"a":"secret"`, `["secret"`, `{"a"}`} {
		if out := transcript.Redact([]byte(bad)); out != nil {
			t.Errorf("Redact(%q) = %q, want nil", bad, out)
		}
	}
}

// shape renders keys and value types, with numbers and booleans by value.
func shape(v any) string {
	switch t := v.(type) {
	case string:
		return "s"
	case []any:
		parts := make([]string, len(t))
		for i, e := range t {
			parts[i] = shape(e)
		}
		return "[" + strings.Join(parts, ",") + "]"
	case map[string]any:
		raw, _ := json.Marshal(mapShape(t))
		return string(raw)
	default:
		return fmt.Sprint(v)
	}
}

func mapShape(m map[string]any) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = shape(v)
	}
	return out
}

// A large rewind is many Removed ids; they count toward the byte cap, so the
// whole reply stays under it and the ids drain over several calls.
func TestAfterRemovedCountsBytes(t *testing.T) {
	const capBytes, n = 4096, 300
	var adds, removes strings.Builder
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%040d", i)
		fmt.Fprintf(&adds, "+%s\n", id)
		fmt.Fprintf(&removes, "-%s\n", id)
	}
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(path, []byte(adds.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	ix := transcript.NewIndex(path, &scriptNorm{}, transcript.IndexOptions{MaxPageBytes: capBytes})
	p, err := ix.Page(nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	addLines(t, path, removes.String())
	removed := map[string]bool{}
	c := p.After
	for calls := 0; ; calls++ {
		if calls > n {
			t.Fatal("After never drained")
		}
		a, err := ix.After(c, 0)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		if len(raw) > capBytes {
			t.Fatalf("reply %d is %d bytes, cap %d", calls, len(raw), capBytes)
		}
		if len(a.Items)+len(a.Changed)+len(a.Removed) == 0 {
			break
		}
		for _, id := range a.Removed {
			removed[id] = true
		}
		c = a.After
	}
	if len(removed) != n {
		t.Errorf("removed %d ids, want %d", len(removed), n)
	}
}
