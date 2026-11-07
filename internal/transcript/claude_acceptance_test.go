package transcript_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/transcript"
)

// Fixtures under testdata/claude are real Claude Code files from this host,
// redacted: prose is replaced by "x" runs, ids are rehashed consistently.
// session.jsonl is one parent transcript; session/subagents holds one
// sub-agent file and its meta; tasks/ mirrors the background output layout.
// leak.jsonl is the background Bash window of session.jsonl with
// claudeLeakToken planted in the command, its result and the notification.
const (
	claudeDir       = "testdata/claude"
	claudeSession   = claudeDir + "/session.jsonl"
	claudeLeak      = claudeDir + "/leak.jsonl"
	claudeLeakToken = "PLANTED-TOKEN-7f3a9c41e2"
)

// fedLine is one fixture line, its position and what the normaliser made of it.
type fedLine struct {
	pos    int64
	raw    map[string]any
	deltas []transcript.Delta
}

func readLines(t *testing.T, path string) (lines [][]byte, pos []int64) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var off int64
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			t.Fatalf("%s: trailing partial line", path)
		}
		off += int64(i + 1)
		lines = append(lines, data[:i])
		pos = append(pos, off)
		data = data[i+1:]
	}
	return lines, pos
}

// feed passes every line of path to n, with the byte offset just past the
// line as its position, and returns what each line produced.
func feed(t *testing.T, n transcript.Normaliser, path string) []fedLine {
	t.Helper()
	lines, pos := readLines(t, path)
	out := make([]fedLine, len(lines))
	for i, l := range lines {
		var raw map[string]any
		if err := json.Unmarshal(l, &raw); err != nil {
			t.Fatalf("%s line %d: %v", path, i+1, err)
		}
		out[i] = fedLine{pos: pos[i], raw: raw, deltas: n.Feed(pos[i], l)}
	}
	return out
}

func str(m map[string]any, keys ...string) string {
	var v any = m
	for _, k := range keys {
		mm, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v = mm[k]
	}
	s, _ := v.(string)
	return s
}

// blocks returns message.content as blocks; a string content is nil.
func blocks(raw map[string]any) []map[string]any {
	msg, _ := raw["message"].(map[string]any)
	list, _ := msg["content"].([]any)
	var out []map[string]any
	for _, b := range list {
		if m, ok := b.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func stringContent(raw map[string]any) (string, bool) {
	msg, _ := raw["message"].(map[string]any)
	s, ok := msg["content"].(string)
	return s, ok
}

func isNotification(raw map[string]any) bool {
	return str(raw, "origin", "kind") == "task-notification"
}

// expectedKinds is what a line should add, read from the raw JSON alone.
// Sub-agent items are checked in TestClaudeSubagents.
func expectedKinds(raw map[string]any) []transcript.ItemKind {
	if str(raw, "uuid") == "" || raw["isMeta"] == true {
		return nil
	}
	switch str(raw, "type") {
	case "system":
		if str(raw, "subtype") == "compact_boundary" {
			return []transcript.ItemKind{transcript.KindSystem}
		}
		return nil
	case "user":
		if raw["isCompactSummary"] == true || isNotification(raw) {
			return []transcript.ItemKind{transcript.KindSystem}
		}
		if _, ok := stringContent(raw); ok {
			return []transcript.ItemKind{transcript.KindUser}
		}
		var out []transcript.ItemKind
		for _, b := range blocks(raw) {
			if b["type"] == "text" {
				out = append(out, transcript.KindUser)
			}
		}
		return out
	case "assistant":
		var out []transcript.ItemKind
		for _, b := range blocks(raw) {
			switch b["type"] {
			case "thinking":
				out = append(out, transcript.KindThinking)
			case "text":
				out = append(out, transcript.KindAssistant)
			case "tool_use":
				out = append(out, transcript.KindToolCall)
			}
		}
		return out
	}
	return nil
}

func adds(ds []transcript.Delta) []transcript.Item {
	var out []transcript.Item
	for _, d := range ds {
		if d.Add != nil {
			out = append(out, *d.Add)
		}
	}
	return out
}

func updates(ds []transcript.Delta) []transcript.Item {
	var out []transcript.Item
	for _, d := range ds {
		if d.Update != nil {
			out = append(out, *d.Update)
		}
	}
	return out
}

func removes(ds []transcript.Delta) []string {
	var out []string
	for _, d := range ds {
		if d.Remove != "" {
			out = append(out, d.Remove)
		}
	}
	return out
}

// toolCallAdd finds the tool_call item added for a tool_use id.
func toolCallAdd(t *testing.T, fed []fedLine, toolID string) (transcript.Item, int) {
	t.Helper()
	for i, f := range fed {
		for _, it := range adds(f.deltas) {
			if it.ToolCall != nil && it.ToolCall.ToolID == toolID {
				return it, i
			}
		}
	}
	t.Fatalf("no tool_call item added for %s", toolID)
	return transcript.Item{}, -1
}

func updateFor(ds []transcript.Delta, id string) *transcript.Item {
	for _, it := range updates(ds) {
		if it.ID == id {
			return &it
		}
	}
	return nil
}

// toolUses lists (line, block) of every tool_use in fed.
func toolUses(fed []fedLine) (lines []int, uses []map[string]any) {
	for i, f := range fed {
		for _, b := range blocks(f.raw) {
			if b["type"] == "tool_use" {
				lines, uses = append(lines, i), append(uses, b)
			}
		}
	}
	return lines, uses
}

// resultLine finds the line carrying the tool_result for toolID.
func resultLine(t *testing.T, fed []fedLine, toolID string) (int, map[string]any) {
	t.Helper()
	for i, f := range fed {
		for _, b := range blocks(f.raw) {
			if b["type"] == "tool_result" && b["tool_use_id"] == toolID {
				return i, b
			}
		}
	}
	t.Fatalf("fixture has no tool_result for %s", toolID)
	return -1, nil
}

func TestClaudeCapabilities(t *testing.T) {
	want := transcript.Capabilities{
		Agent:       "claude",
		Scrollback:  transcript.ScrollbackTranscript,
		ToolResults: true,
		Thinking:    transcript.ThinkingFull,
		Subagents:   true,
		Processes:   transcript.ProcessesFile,
	}
	lookup := func(string) (transcript.SubagentMeta, bool) { return transcript.SubagentMeta{}, false }
	for name, n := range map[string]transcript.Normaliser{
		"NewClaude":     transcript.NewClaude(),
		"NewClaudeWith": transcript.NewClaudeWith(transcript.ClaudeOptions{SubagentMeta: lookup}),
	} {
		if got := n.Capabilities(); got != want {
			t.Errorf("%s capabilities = %+v, want %+v", name, got, want)
		}
		if n.Kind() != "claude" || n.PositionKind() != transcript.PositionOffset {
			t.Errorf("%s kind %q position %q, want claude offset", name, n.Kind(), n.PositionKind())
		}
		if _, ok := n.(transcript.ProcessOutputs); !ok {
			t.Errorf("%s does not implement ProcessOutputs", name)
		}
	}
}

func TestClaudeItems(t *testing.T) {
	n := transcript.NewClaude()
	fed := feed(t, n, claudeSession)
	seen := map[string]bool{}
	var kinds []transcript.ItemKind
	for i, f := range fed {
		var got []transcript.ItemKind
		for _, it := range adds(f.deltas) {
			if it.Kind == transcript.KindSubagent {
				continue
			}
			got = append(got, it.Kind)
			if it.ID == "" || seen[it.ID] {
				t.Errorf("line %d: item id %q empty or reused", i+1, it.ID)
			}
			seen[it.ID] = true
			if it.Rev != f.pos {
				t.Errorf("line %d: %s rev %d, want %d", i+1, it.Kind, it.Rev, f.pos)
			}
			if ts := str(f.raw, "timestamp"); ts != "" {
				want, _ := time.Parse(time.RFC3339Nano, ts)
				if !it.At.Equal(want) {
					t.Errorf("line %d: at %v, want %v", i+1, it.At, want)
				}
			}
			if (it.Kind == transcript.KindUser || it.Kind == transcript.KindAssistant) && it.Text == "" {
				t.Errorf("line %d: %s item has no text", i+1, it.Kind)
			}
		}
		want := expectedKinds(f.raw)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("line %d (%s/%s): added %v, want %v", i+1, str(f.raw, "type"), str(f.raw, "subtype"), got, want)
		}
		kinds = append(kinds, got...)
	}
	for _, k := range []transcript.ItemKind{transcript.KindUser, transcript.KindAssistant, transcript.KindThinking, transcript.KindToolCall, transcript.KindSystem} {
		found := false
		for _, g := range kinds {
			found = found || g == k
		}
		if !found {
			t.Errorf("no %s item from the fixture", k)
		}
	}

	var compact bool
	for i, f := range fed {
		if str(f.raw, "subtype") == "compact_boundary" {
			a := adds(f.deltas)
			compact = len(a) == 1 && a[0].Kind == transcript.KindSystem
			if !compact {
				t.Errorf("line %d: compact_boundary added %+v, want one system item", i+1, a)
			}
		}
	}
	if !compact {
		t.Error("compact_boundary produced no system item")
	}

	if d := n.Flush(); len(d) != 0 {
		t.Errorf("Flush after a full read = %+v, want nothing", d)
	}
	n.Reset()
	again := feed(t, n, claudeSession)
	for i := range fed {
		if !reflect.DeepEqual(fed[i].deltas, again[i].deltas) {
			t.Fatalf("line %d: deltas differ after Reset", i+1)
		}
	}
}

func TestClaudeToolCalls(t *testing.T) {
	fed := feed(t, transcript.NewClaude(), claudeSession)
	useLines, uses := toolUses(fed)
	if len(uses) < 5 {
		t.Fatalf("fixture has %d tool_use blocks", len(uses))
	}
	var sawError, sawDuration bool
	for k, use := range uses {
		id, _ := use["id"].(string)
		call, line := toolCallAdd(t, fed, id)
		if line != useLines[k] {
			t.Errorf("%s added on line %d, want %d", id, line+1, useLines[k]+1)
		}
		tc := call.ToolCall
		if call.Kind != transcript.KindToolCall || tc.Name != use["name"] || tc.Status != transcript.ToolRunning || tc.Result != "" || tc.Input == "" {
			t.Errorf("%s call = %+v %+v, want tool_call %v running with input", id, call, tc, use["name"])
		}

		rl, res := resultLine(t, fed, id)
		up := updateFor(fed[rl].deltas, call.ID)
		if up == nil || up.ToolCall == nil {
			t.Errorf("%s: no update for item %q on result line %d", id, call.ID, rl+1)
			continue
		}
		for _, a := range adds(fed[rl].deltas) {
			if a.ToolCall != nil && a.ToolCall.ToolID == id {
				t.Errorf("%s: result line added a second item", id)
			}
		}
		want := transcript.ToolOK
		if res["is_error"] == true {
			want, sawError = transcript.ToolError, true
		}
		if up.Kind != transcript.KindToolCall || up.ToolCall.ToolID != id || up.ToolCall.Status != want || up.ToolCall.Result == "" || up.Rev != fed[rl].pos {
			t.Errorf("%s update = rev %d %+v, want status %s with result at rev %d", id, up.Rev, up.ToolCall, want, fed[rl].pos)
		}
		if tur, ok := fed[rl].raw["toolUseResult"].(map[string]any); ok {
			if secs, ok := tur["durationSeconds"].(float64); ok {
				sawDuration = true
				if up.ToolCall.DurationMS != int64(secs*1000) {
					t.Errorf("%s duration %dms, want %dms", id, up.ToolCall.DurationMS, int64(secs*1000))
				}
			}
		}
	}
	if !sawError || !sawDuration {
		t.Errorf("fixture checks missed: error result %v, durationSeconds %v", sawError, sawDuration)
	}
}

var outputPathRe = regexp.MustCompile(`Output is being written to: (\S+?\.output)`)

func TestClaudeProcesses(t *testing.T) {
	n := transcript.NewClaude()
	fed := feed(t, n, claudeSession)
	var use map[string]any
	_, uses := toolUses(fed)
	for _, u := range uses {
		if in, _ := u["input"].(map[string]any); u["name"] == "Bash" && in["run_in_background"] == true {
			use = u
		}
	}
	if use == nil {
		t.Fatal("fixture has no background Bash call")
	}
	toolID, _ := use["id"].(string)
	command := str(use, "input", "command")
	call, _ := toolCallAdd(t, fed, toolID)
	rl, res := resultLine(t, fed, toolID)
	taskID := str(fed[rl].raw, "toolUseResult", "backgroundTaskId")
	content, _ := res["content"].(string)
	m := outputPathRe.FindStringSubmatch(content)
	if taskID == "" || m == nil {
		t.Fatal("fixture background result lacks backgroundTaskId or output path")
	}

	up := updateFor(fed[rl].deltas, call.ID)
	if up == nil || up.ToolCall == nil || up.ToolCall.ProcessID != taskID {
		t.Fatalf("background result update = %+v, want process_id %s", up, taskID)
	}
	p := up.Process
	if p == nil || p.ID != taskID || p.Output != transcript.OutputFile || p.State != transcript.ProcessRunning || p.Command != command {
		t.Errorf("process = %+v, want {%s running file %q}", p, taskID, command)
	}
	if path, ok := n.(transcript.ProcessOutputs).ProcessOutput(taskID); !ok || path != m[1] {
		t.Errorf("ProcessOutput(%s) = %q %v, want %q", taskID, path, ok, m[1])
	}
	if _, ok := n.(transcript.ProcessOutputs).ProcessOutput("nope"); ok {
		t.Error("ProcessOutput found an unknown id")
	}

	nl := -1
	for i, f := range fed {
		if s, _ := stringContent(f.raw); isNotification(f.raw) && strings.Contains(s, "<task-id>"+taskID+"</task-id>") {
			nl = i
		}
	}
	if nl < 0 {
		t.Fatal("fixture has no notification for the background task")
	}
	note, _ := stringContent(fed[nl].raw)
	wantExit, _ := strconv.Atoi(regexp.MustCompile(`exit code (\d+)`).FindStringSubmatch(note)[1])
	a := adds(fed[nl].deltas)
	if len(a) != 1 || a[0].Kind != transcript.KindSystem || a[0].Text == "" {
		t.Errorf("notification added %+v, want one system item with text", a)
	}
	done := updateFor(fed[nl].deltas, call.ID)
	if done == nil || done.Process == nil || done.Process.State != transcript.ProcessExited || done.Rev != fed[nl].pos {
		t.Fatalf("notification update = %+v, want process exited at rev %d", done, fed[nl].pos)
	}
	if done.Process.ExitCode == nil || *done.Process.ExitCode != wantExit || done.ToolCall == nil || done.ToolCall.ProcessID != taskID {
		t.Errorf("exited process = %+v call %+v, want exit code %d", done.Process, done.ToolCall, wantExit)
	}
}

// agentFixture finds the Agent call, its result line, the agent id and the
// line of the notification naming it.
func agentFixture(t *testing.T, fed []fedLine) (use map[string]any, resLine int, agentID string, noteLine int) {
	t.Helper()
	_, uses := toolUses(fed)
	for _, u := range uses {
		if u["name"] == "Agent" {
			use = u
		}
	}
	if use == nil {
		t.Fatal("fixture has no Agent call")
	}
	id, _ := use["id"].(string)
	resLine, _ = resultLine(t, fed, id)
	agentID = str(fed[resLine].raw, "toolUseResult", "agentId")
	noteLine = -1
	for i, f := range fed {
		if s, _ := stringContent(f.raw); isNotification(f.raw) && strings.Contains(s, "<task-id>"+agentID+"</task-id>") {
			noteLine = i
		}
	}
	if agentID == "" || noteLine < 0 {
		t.Fatal("fixture Agent result lacks agentId or a notification")
	}
	return use, resLine, agentID, noteLine
}

func subagentItem(items []transcript.Item, agentID string) *transcript.Item {
	for _, it := range items {
		if it.Kind == transcript.KindSubagent && it.Subagent != nil && it.Subagent.ID == agentID {
			return &it
		}
	}
	return nil
}

func TestClaudeSubagents(t *testing.T) {
	probe := feed(t, transcript.NewClaude(), claudeSession)
	use, resLine, agentID, noteLine := agentFixture(t, probe)
	toolID, _ := use["id"].(string)

	t.Run("meta lookup", func(t *testing.T) {
		finished := false
		var asked []string
		n := transcript.NewClaudeWith(transcript.ClaudeOptions{SubagentMeta: func(id string) (transcript.SubagentMeta, bool) {
			asked = append(asked, id)
			if id != agentID {
				return transcript.SubagentMeta{}, false
			}
			return transcript.SubagentMeta{AgentType: "Plan", Description: "audit config loader", ToolUseID: toolID, Finished: finished}, true
		}})
		lines, pos := readLines(t, claudeSession)
		var all []transcript.Delta
		var started *transcript.Item
		for i, l := range lines {
			if i == noteLine {
				if started == nil {
					t.Fatal("no subagent item before the notification")
				}
				finished = true
			}
			ds := n.Feed(pos[i], l)
			all = append(all, ds...)
			if i == resLine {
				call, _ := toolCallAdd(t, probe, toolID)
				up := updateFor(ds, call.ID)
				if up == nil || up.ToolCall == nil || up.ToolCall.SubagentID != agentID {
					t.Errorf("Agent result update = %+v, want subagent_id %s", up, agentID)
				}
				started = subagentItem(adds(all), agentID)
				if started == nil {
					t.Fatalf("no subagent item for %s by the Agent result", agentID)
				}
				s := started.Subagent
				if s.Description != "audit config loader" || s.Name != "Plan" || s.State != transcript.SubagentRunning {
					t.Errorf("subagent = %+v, want meta description and type, running", s)
				}
			}
			if i == noteLine {
				end := updateFor(ds, started.ID)
				if end == nil || end.Subagent == nil || end.Subagent.State != transcript.SubagentDone || end.Rev != pos[i] {
					t.Errorf("notification after meta.finished: update = %+v, want state done at rev %d", end, pos[i])
				}
			}
		}
		if len(asked) == 0 || asked[0] != agentID {
			t.Errorf("lookup asked %v, want %s first", asked, agentID)
		}
	})

	t.Run("no lookup", func(t *testing.T) {
		fed := feed(t, transcript.NewClaude(), claudeSession)
		var before []transcript.Item
		for _, f := range fed[:resLine+1] {
			before = append(before, adds(f.deltas)...)
		}
		it := subagentItem(before, agentID)
		if it == nil {
			t.Fatalf("no subagent item for %s", agentID)
		}
		if want := str(use, "input", "description"); it.Subagent.Description != want || it.Subagent.State != transcript.SubagentRunning {
			t.Errorf("subagent = %+v, want description %q from the call, running", it.Subagent, want)
		}
		end := updateFor(fed[noteLine].deltas, it.ID)
		if end == nil || end.Subagent == nil || end.Subagent.State != transcript.SubagentDone {
			t.Errorf("completed notification: update = %+v, want state done", end)
		}
	})

	t.Run("meta file", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(claudeDir, "session/subagents/agent-"+agentID+".meta.json"))
		if err != nil {
			t.Fatal(err)
		}
		var want transcript.SubagentMeta
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatal(err)
		}
		lookup := transcript.ClaudeMetaLookup(filepath.Join(claudeDir, "session"))
		got, ok := lookup(agentID)
		if !ok || got != want || !got.Finished || got.ToolUseID != toolID {
			t.Errorf("ClaudeMetaLookup(%s) = %+v %v, want %+v", agentID, got, ok, want)
		}
		for _, bad := range []string{"missing", "../session/subagents/agent-" + agentID, "", "a/b"} {
			if _, ok := lookup(bad); ok {
				t.Errorf("ClaudeMetaLookup(%q) found something", bad)
			}
		}
	})

	t.Run("child file", func(t *testing.T) {
		fed := feed(t, transcript.NewClaude(), filepath.Join(claudeDir, "session/subagents/agent-"+agentID+".jsonl"))
		counts := map[transcript.ItemKind]int{}
		for i, f := range fed {
			for _, it := range adds(f.deltas) {
				counts[it.Kind]++
			}
			if r := removes(f.deltas); len(r) > 0 {
				t.Errorf("child line %d removed %v", i+1, r)
			}
		}
		if counts[transcript.KindAssistant]+counts[transcript.KindThinking] == 0 || counts[transcript.KindToolCall] == 0 {
			t.Errorf("child items by kind = %v, want assistant or thinking and tool_call", counts)
		}
	})
}

func isHuman(raw map[string]any) bool {
	_, ok := stringContent(raw)
	return ok && str(raw, "type") == "user" && raw["isMeta"] != true && str(raw, "origin", "kind") == "human"
}

func TestClaudeRewind(t *testing.T) {
	fed := feed(t, transcript.NewClaude(), claudeSession)
	first := map[string]int{}
	first1, second := -1, -1
	for i, f := range fed {
		if !isHuman(f.raw) {
			continue
		}
		p := str(f.raw, "parentUuid")
		if j, ok := first[p]; ok && p != "" {
			first1, second = j, i
		}
		first[p] = i
	}
	if second < 0 {
		t.Fatal("fixture has no rewind (two human children of one parent)")
	}

	want := map[string]bool{}
	for _, f := range fed[first1:second] {
		for _, it := range adds(f.deltas) {
			want[it.ID] = true
		}
	}
	if len(want) < 2 {
		t.Fatalf("first branch added %d items, want its prompt and reply", len(want))
	}
	got := removes(fed[second].deltas)
	gotSet := map[string]bool{}
	for _, id := range got {
		gotSet[id] = true
	}
	if !reflect.DeepEqual(gotSet, want) || len(got) != len(want) {
		t.Errorf("rewind removed %v, want %v", got, keys(want))
	}
	for i, f := range fed {
		if i == second {
			continue
		}
		if r := removes(f.deltas); len(r) > 0 {
			t.Errorf("line %d (%s/%s) removed %v; only the rewind removes", i+1, str(f.raw, "type"), str(f.raw, "subtype"), r)
		}
	}
	for _, f := range fed[second:] {
		for _, it := range adds(f.deltas) {
			if want[it.ID] {
				t.Errorf("new branch reused removed id %s", it.ID)
			}
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestClaudeThinkingHidden(t *testing.T) {
	fed := feed(t, transcript.NewClaude(), claudeSession)
	var hidden, shown int
	for i, f := range fed {
		for _, b := range blocks(f.raw) {
			if b["type"] != "thinking" {
				continue
			}
			a := adds(f.deltas)
			if len(a) != 1 || a[0].Kind != transcript.KindThinking {
				t.Errorf("line %d: thinking block added %+v", i+1, a)
				continue
			}
			text, _ := b["thinking"].(string)
			sig, _ := b["signature"].(string)
			switch {
			case text == "" && sig != "":
				hidden++
				if !a[0].Hidden || a[0].Text != "" {
					t.Errorf("line %d: signature-only thinking = hidden %v text %q, want hidden and empty", i+1, a[0].Hidden, a[0].Text)
				}
			case text != "":
				shown++
				if a[0].Hidden || a[0].Text == "" {
					t.Errorf("line %d: thinking with text = hidden %v text %q", i+1, a[0].Hidden, a[0].Text)
				}
			}
		}
	}
	if hidden == 0 || shown == 0 {
		t.Errorf("fixture thinking blocks: %d hidden, %d with text; want both", hidden, shown)
	}
}

func TestClaudeNoSecretLeak(t *testing.T) {
	raw, err := os.ReadFile(claudeLeak)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(raw, []byte(claudeLeakToken)) < 3 {
		t.Fatal("leak fixture lost its planted token")
	}
	fed := feed(t, transcript.NewClaude(), claudeLeak)
	var bashID string
	for _, f := range fed {
		for _, b := range blocks(f.raw) {
			if b["type"] == "tool_use" && strings.Contains(str(b, "input", "command"), claudeLeakToken) {
				bashID, _ = b["id"].(string)
			}
		}
	}
	if bashID == "" {
		t.Fatal("leak fixture has no tool_use carrying the token")
	}

	var callSeen, systemSeen bool
	for i, f := range fed {
		for _, d := range f.deltas {
			it := d.Add
			if it == nil {
				it = d.Update
			}
			if it == nil {
				continue
			}
			if it.Kind == transcript.KindSystem {
				systemSeen = true
			}
			if it.Kind == transcript.KindToolCall && it.ToolCall != nil && it.ToolCall.ToolID == bashID {
				callSeen = true
				if !strings.Contains(it.ToolCall.Input, claudeLeakToken) {
					t.Errorf("line %d: the call's own input lost the token: %q", i+1, it.ToolCall.Input)
				}
				own := *it
				own.Text, own.ToolCall, own.Process = "", nil, nil
				if b, _ := json.Marshal(own); bytes.Contains(b, []byte(claudeLeakToken)) {
					t.Errorf("line %d: token outside the call's tool fields: %s", i+1, b)
				}
				continue
			}
			if b, _ := json.Marshal(it); bytes.Contains(b, []byte(claudeLeakToken)) {
				t.Errorf("line %d: token in a %s item: %s", i+1, it.Kind, b)
			}
		}
	}
	if !callSeen || !systemSeen {
		t.Errorf("leak fixture produced tool call %v, system item %v; want both", callSeen, systemSeen)
	}
}
