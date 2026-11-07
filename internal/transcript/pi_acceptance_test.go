package transcript_test

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/transcript"
)

var piFixture = filepath.Join("testdata", "pi", "session.jsonl")

// piLine is the part of a Pi session line the tests read expectations from.
type piLine struct {
	Type     string `json:"type"`
	ID       string `json:"id"`
	ParentID string `json:"parentId"`
	ModelID  string `json:"modelId"`
	Message  struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"toolCallId"`
		IsError    bool            `json:"isError"`
		DurationMS int64           `json:"durationMs"`
	} `json:"message"`
}

type piBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func parsePi(t *testing.T, line []byte) piLine {
	t.Helper()
	var p piLine
	if err := json.Unmarshal(line, &p); err != nil {
		t.Fatalf("fixture line is not JSON: %v", err)
	}
	return p
}

func (p piLine) blocks(t *testing.T) []piBlock {
	t.Helper()
	var bs []piBlock
	if len(p.Message.Content) == 0 || p.Message.Content[0] != '[' {
		return nil
	}
	if err := json.Unmarshal(p.Message.Content, &bs); err != nil {
		t.Fatal(err)
	}
	return bs
}

// replayPi feeds the fixture with byte offsets just past each line and
// returns the replay, the parsed lines and each line's end offset.
func replayPi(t *testing.T, lines [][]byte) (*replay, []piLine, []int64) {
	t.Helper()
	r := newReplay(t, transcript.NewPi())
	var parsed []piLine
	var ends []int64
	var off int64
	for _, l := range lines {
		off += int64(len(l))
		parsed = append(parsed, parsePi(t, l))
		ends = append(ends, off)
		r.feed(off, l)
	}
	r.flush()
	return r, parsed, ends
}

// branchAt is the index of the first fixture line whose parent already has
// a child: the start of the active branch that abandons its sibling.
func branchAt(t *testing.T, lines []piLine) int {
	t.Helper()
	children := map[string]int{}
	for i, l := range lines {
		if l.ParentID != "" {
			children[l.ParentID]++
			if children[l.ParentID] == 2 {
				return i
			}
		}
	}
	t.Fatal("fixture has no branch")
	return -1
}

func TestPiCapabilities(t *testing.T) {
	n := transcript.NewPi()
	want := transcript.Capabilities{
		Agent:       "pi",
		Scrollback:  transcript.ScrollbackTranscript,
		ToolResults: true,
		Thinking:    transcript.ThinkingFull,
		Subagents:   false,
		Processes:   transcript.ProcessesNone,
	}
	if n.Kind() != "pi" || n.PositionKind() != transcript.PositionOffset || n.Capabilities() != want {
		t.Errorf("pi = %q %q %+v, want pi offset %+v", n.Kind(), n.PositionKind(), n.Capabilities(), want)
	}
}

func TestPiItems(t *testing.T) {
	r, lines, _ := replayPi(t, fixtureLines(t, piFixture))

	// Active branch only: the session header and the system prompt give
	// nothing, the abandoned branch (entries after the tool result's reply)
	// is gone, and each assistant entry yields thinking before its text or
	// tool call.
	want := "user,assistant,user,thinking,assistant,user,thinking,tool_call,assistant," +
		"user,thinking,tool_call,assistant"
	if got := kinds(r.items, transcript.KindSystem); got != want {
		t.Fatalf("kinds = %s\nwant    %s", got, want)
	}
	seen := map[string]bool{}
	for _, it := range r.items {
		if it.ID == "" || seen[it.ID] {
			t.Errorf("item id %q empty or repeated", it.ID)
		}
		seen[it.ID] = true
		if it.At.IsZero() {
			t.Errorf("item %s has no time", it.ID)
		}
	}

	var modelID, systemPrompt string
	var thinking []string
	for _, l := range lines {
		switch {
		case l.Type == "model_change":
			modelID = l.ModelID
		case l.Message.Role == "system":
			_ = json.Unmarshal(l.Message.Content, &systemPrompt)
		}
	}
	b := branchAt(t, lines)
	for i, l := range lines {
		if i >= b-4 && i < b { // the abandoned branch: four entries before the fork
			continue
		}
		for _, bl := range l.blocks(t) {
			if bl.Type == "thinking" {
				thinking = append(thinking, bl.Thinking)
			}
		}
	}
	sys := byKind(r.items, transcript.KindSystem)
	found := false
	for _, s := range sys {
		found = found || strings.Contains(s.Text, modelID)
	}
	if !found || len(sys) > 2 {
		t.Errorf("system items = %+v, want model_change (%q) and at most a thinking level change", sys, modelID)
	}
	for _, it := range r.items {
		if len(systemPrompt) > 40 && strings.Contains(it.Text, systemPrompt[:40]) {
			t.Errorf("system prompt surfaced in item %s", it.ID)
		}
	}
	th := byKind(r.items, transcript.KindThinking)
	if len(th) != len(thinking) {
		t.Fatalf("thinking items = %d, want %d", len(th), len(thinking))
	}
	for i := range th {
		if th[i].Text != thinking[i] || th[i].Hidden {
			t.Errorf("thinking %d = %q hidden=%v, want %q", i, th[i].Text, th[i].Hidden, thinking[i])
		}
	}
}

func TestPiBranch(t *testing.T) {
	raw := fixtureLines(t, piFixture)
	r := newReplay(t, transcript.NewPi())
	var lines []piLine
	for _, l := range raw {
		lines = append(lines, parsePi(t, l))
	}
	b := branchAt(t, lines)

	addedBy := map[int][]string{}
	var off int64
	var removedAtFork []string
	var removedElsewhere []string
	for i, l := range raw {
		off += int64(len(l))
		ds := r.feed(off, l)
		for _, d := range ds {
			if d.Add != nil {
				addedBy[i] = append(addedBy[i], d.Add.ID)
			}
			if d.Remove != "" {
				if i == b {
					removedAtFork = append(removedAtFork, d.Remove)
				} else {
					removedElsewhere = append(removedElsewhere, d.Remove)
				}
			}
		}
		if i == b-1 && len(addedBy[b-4]) == 0 {
			t.Fatal("abandoned branch produced no items before the fork")
		}
	}
	r.flush()

	// The abandoned branch is the chain from the fork's sibling to the old
	// leaf: the four entries just before the fork line.
	var abandoned []string
	for i := b - 4; i < b; i++ {
		abandoned = append(abandoned, addedBy[i]...)
	}
	sort.Strings(abandoned)
	sort.Strings(removedAtFork)
	if strings.Join(removedAtFork, ",") != strings.Join(abandoned, ",") || len(abandoned) == 0 {
		t.Errorf("removed at fork = %v, want the abandoned branch's items %v", removedAtFork, abandoned)
	}
	if len(removedElsewhere) > 0 {
		t.Errorf("removes outside the fork: %v", removedElsewhere)
	}
	for _, id := range abandoned {
		if r.has(id) {
			t.Errorf("abandoned item %s still listed", id)
		}
	}
	for _, id := range addedBy[b] {
		if !r.has(id) {
			t.Errorf("fork item %s not listed", id)
		}
	}
}

func TestPiToolCalls(t *testing.T) {
	r, lines, ends := replayPi(t, fixtureLines(t, piFixture))

	type call struct {
		id, name, args, result string
		isError                bool
		durationMS, rev        int64
	}
	byID := map[string]*call{}
	var order []*call
	for i, l := range lines {
		for _, bl := range l.blocks(t) {
			if bl.Type == "toolCall" {
				c := &call{id: bl.ID, name: bl.Name, args: string(bl.Arguments)}
				byID[bl.ID] = c
				order = append(order, c)
			}
		}
		if l.Message.Role == "toolResult" {
			c := byID[l.Message.ToolCallID]
			if bs := l.blocks(t); len(bs) > 0 {
				c.result = bs[0].Text
			}
			c.isError, c.durationMS, c.rev = l.Message.IsError, l.Message.DurationMS, ends[i]
		}
	}
	got := byKind(r.items, transcript.KindToolCall)
	if len(got) != len(order) || len(order) != 2 {
		t.Fatalf("tool calls = %d, want %d (fixture has 2)", len(got), len(order))
	}
	sawError := false
	for i, c := range order {
		it := got[i]
		tc := it.ToolCall
		if tc == nil {
			t.Fatalf("tool_call item %s has no ToolCall", it.ID)
		}
		wantStatus := transcript.ToolOK
		if c.isError {
			wantStatus = transcript.ToolError
			sawError = true
		}
		if tc.ToolID != c.id || tc.Name != c.name || tc.Status != wantStatus || tc.DurationMS != c.durationMS {
			t.Errorf("call %d = %q %q %q %d, want %q %q %q %d", i, tc.ToolID, tc.Name, tc.Status, tc.DurationMS, c.id, c.name, wantStatus, c.durationMS)
		}
		if !strings.Contains(tc.Result, c.result) || c.result == "" {
			t.Errorf("call %d result = %q, want it to hold %q", i, tc.Result, c.result)
		}
		var args map[string]string
		_ = json.Unmarshal([]byte(c.args), &args)
		for _, v := range args {
			if !strings.Contains(tc.Input, v) {
				t.Errorf("call %d input %q lacks argument %q", i, tc.Input, v)
			}
		}
		if it.Rev != c.rev {
			t.Errorf("call %d rev = %d, want the result line's end offset %d", i, it.Rev, c.rev)
		}
		if it.Process != nil || tc.SubagentID != "" {
			t.Errorf("pi call %d has a process or sub-agent", i)
		}
	}
	if !sawError {
		t.Error("fixture lost its isError result")
	}
}

func TestPiNoSecretLeak(t *testing.T) {
	lines := []string{
		`{"type":"session","version":3,"id":"s-1","timestamp":"2026-11-07T10:00:00.000Z","cwd":"/w"}`,
		`{"type":"message","id":"e-1","parentId":null,"timestamp":"2026-11-07T10:00:01.000Z","message":{"role":"system","content":"api key ` + plantedToken + `","timestamp":1793959201000}}`,
		`{"type":"message","id":"e-2","parentId":"e-1","timestamp":"2026-11-07T10:00:02.000Z","message":{"role":"user","content":[{"type":"text","text":"run the tests"}],"timestamp":1793959202000}}`,
		`{"type":"message","id":"e-3","parentId":"e-2","timestamp":"2026-11-07T10:00:03.000Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"plan the run","thinkingSignature":"` + plantedToken + `"},{"type":"text","text":"running"}],"usage":{"input":1,"output":1},"stopReason":"stop","timestamp":1793959203000}}`,
	}
	r := newReplay(t, transcript.NewPi())
	var off int64
	for _, l := range lines {
		b := []byte(l + "\n")
		off += int64(len(b))
		r.feed(off, b)
	}
	r.flush()
	if got := kinds(r.items, transcript.KindSystem); got != "user,thinking,assistant" {
		t.Errorf("kinds = %q, want user,thinking,assistant", got)
	}
	for _, s := range r.steps {
		raw, err := json.Marshal(s.deltas)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), plantedToken) {
			t.Errorf("offset %d: planted token reached a delta: %s", s.pos, raw)
		}
	}
}
