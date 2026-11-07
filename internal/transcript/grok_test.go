package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type grokJSON = map[string]any

func grokUpd(upd grokJSON, meta grokJSON) []byte {
	b, _ := json.Marshal(grokJSON{"method": "session/update", "params": grokJSON{"update": upd, "_meta": meta}})
	return b
}

func grokChunk(kind, text, prompt, event string) []byte {
	meta := grokJSON{"eventId": event, "agentTimestampMs": 1000}
	if prompt != "" {
		meta["promptId"] = prompt
	}
	return grokUpd(grokJSON{"sessionUpdate": kind, "content": grokJSON{"type": "text", "text": text}}, meta)
}

// feedGrokLines feeds lines with positions 1, 2, ... and flushes after each.
func feedGrokLines(n Normaliser, lines ...[]byte) map[string]Item {
	items := map[string]Item{}
	for i, l := range lines {
		ds := append(n.Feed(int64(i+1), l), n.Flush()...)
		for _, d := range ds {
			switch {
			case d.Add != nil:
				items[d.Add.ID] = *d.Add
			case d.Update != nil:
				items[d.Update.ID] = *d.Update
			}
		}
	}
	return items
}

func TestGrokChunksWithoutPromptIDJoinUntilTurnEnds(t *testing.T) {
	n := NewGrok()
	items := feedGrokLines(n,
		grokChunk("agent_message_chunk", "a", "", "e1"),
		grokChunk("agent_message_chunk", "b", "", "e2"),
		grokUpd(grokJSON{"sessionUpdate": "turn_completed"}, grokJSON{}),
		grokChunk("agent_message_chunk", "c", "", "e3"),
	)
	if len(items) != 2 || items["assistant:e1"].Text != "ab" || items["assistant:e3"].Text != "c" {
		t.Fatalf("items = %+v, want ab under e1 and c under e3", items)
	}
}

func TestGrokFlushEmitsOnlyChangedGroups(t *testing.T) {
	n := NewGrok()
	if ds := n.Feed(1, grokChunk("agent_thought_chunk", "x", "p", "e1")); len(ds) != 1 || ds[0].Add == nil {
		t.Fatalf("first chunk gave %+v, want one Add", ds)
	}
	if ds := n.Flush(); len(ds) != 0 {
		t.Fatalf("Flush after an Add gave %d deltas", len(ds))
	}
	n.Feed(2, grokChunk("agent_thought_chunk", "y", "p", "e2"))
	n.Feed(3, grokChunk("agent_thought_chunk", "z", "p", "e3"))
	ds := n.Flush()
	if len(ds) != 1 || ds[0].Update == nil || ds[0].Update.Text != "xyz" || ds[0].Update.Rev != 3 {
		t.Fatalf("Flush gave %+v, want one Update xyz rev 3", ds)
	}
	ds[0].Update.Text = "mutated"
	n.Feed(4, grokChunk("agent_thought_chunk", "!", "p", "e4"))
	if ds := n.Flush(); ds[0].Update.Text != "xyz!" {
		t.Fatalf("delta aliased state: %q", ds[0].Update.Text)
	}
}

func TestGrokUserChunksJoin(t *testing.T) {
	user := func(text, ev string) []byte {
		return grokUpd(grokJSON{"sessionUpdate": "user_message_chunk", "content": grokJSON{"text": text}}, grokJSON{"eventId": ev})
	}
	items := feedGrokLines(NewGrok(), user("he", "u1"), user("llo", "u2"),
		grokChunk("agent_message_chunk", "ok", "p", "a1"), user("again", "u3"))
	if items["user:u1"].Text != "hello" || items["user:u3"].Text != "again" || len(items) != 3 {
		t.Fatalf("items = %+v", items)
	}
}

func TestGrokToolStatuses(t *testing.T) {
	for status, want := range map[string]string{"completed": ToolOK, "failed": ToolError, "cancelled": ToolDenied, "in_progress": ToolRunning} {
		items := feedGrokLines(NewGrok(),
			grokUpd(grokJSON{"sessionUpdate": "tool_call", "toolCallId": "t", "title": "run", "rawInput": grokJSON{"cmd": "ls"}}, grokJSON{"agentTimestampMs": 1000}),
			grokUpd(grokJSON{"sessionUpdate": "tool_call_update", "toolCallId": "t", "status": status, "rawOutput": grokJSON{"out": 1}}, grokJSON{"agentTimestampMs": 1500}),
		)
		tc := items["tool:t"].ToolCall
		if tc == nil || tc.Status != want || tc.Result != `{"out":1}` || tc.Input != `{"cmd":"ls"}` {
			t.Errorf("%s: tool call = %+v, want %s", status, tc, want)
		}
		if want != ToolRunning && tc.DurationMS != 500 {
			t.Errorf("%s: duration %d, want 500", status, tc.DurationMS)
		}
	}
}

func TestGrokOrphanUpdatesAddItems(t *testing.T) {
	items := feedGrokLines(NewGrok(),
		grokUpd(grokJSON{"sessionUpdate": "tool_call_update", "toolCallId": "t", "status": "completed"}, grokJSON{}),
		grokUpd(grokJSON{"sessionUpdate": "subagent_finished", "subagent_id": "s", "status": "failed"}, grokJSON{}),
		grokUpd(grokJSON{"sessionUpdate": "task_completed", "task_snapshot": grokJSON{"task_id": "k", "exit_code": 2, "output_file": "/x.log"}}, grokJSON{}),
	)
	if items["tool:t"].ToolCall.Status != ToolOK || items["subagent:s"].Subagent.State != SubagentFailed {
		t.Errorf("items = %+v", items)
	}
	if p := items["process:k"].Process; p == nil || p.State != ProcessExited || p.ExitCode == nil || *p.ExitCode != 2 {
		t.Errorf("process = %+v", p)
	}
}

func TestGrokChildLookupStaysInSessionsDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "parent")
	if err := os.MkdirAll(filepath.Join(root, "child"), 0o700); err != nil {
		t.Fatal(err)
	}
	child := strings.Join([]string{
		string(grokChunk("agent_message_chunk", "a", "p", "e1")),
		string(grokChunk("agent_message_chunk", "b", "p", "e2")),
		string(grokChunk("agent_thought_chunk", "c", "p", "e3")),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(root, "child", "updates.jsonl"), []byte(child), 0o600); err != nil {
		t.Fatal(err)
	}
	spawn := func(childID string) int {
		items := feedGrokLines(NewGrokWith(GrokOptions{SessionDir: dir}),
			grokUpd(grokJSON{"sessionUpdate": "subagent_finished", "subagent_id": "s", "child_session_id": childID, "status": "completed"}, grokJSON{}))
		return items["subagent:s"].Subagent.ItemCount
	}
	if got := spawn("child"); got != 2 {
		t.Errorf("child item count = %d, want 2", got)
	}
	for _, bad := range []string{"../child", "..", "", "child/../child"} {
		if got := spawn(bad); got != 0 {
			t.Errorf("child id %q counted %d items, want 0", bad, got)
		}
	}
}

func TestGrokResetAndProcessPath(t *testing.T) {
	n := NewGrok()
	bg := grokUpd(grokJSON{"sessionUpdate": "task_backgrounded", "task_id": "k", "output_file": "/o.log"}, grokJSON{})
	feedGrokLines(n, bg)
	if p, ok := n.(ProcessPather).ProcessPath("k"); !ok || p != "/o.log" {
		t.Fatalf("ProcessPath = %q %v", p, ok)
	}
	n.Reset()
	if _, ok := n.(ProcessPather).ProcessPath("k"); ok {
		t.Error("Reset kept the output path")
	}
	if ds := n.Feed(1, bg); len(ds) != 1 || ds[0].Add == nil {
		t.Errorf("after Reset the same line gave %+v, want an Add", ds)
	}
}

func TestGrokMalformedLines(t *testing.T) {
	for _, l := range []string{``, `{}`, `[1]`, `{"method":"session/update","params":{"update":{"sessionUpdate":7}}}`,
		`{"method":"session/update","params":{"update":{"sessionUpdate":"plan","entries":[]}}}`} {
		if ds := NewGrok().Feed(1, []byte(l)); len(ds) != 0 {
			t.Errorf("%q gave %d deltas", l, len(ds))
		}
	}
}

func TestGrokTurnCompletedDropsGroups(t *testing.T) {
	n := NewGrok()
	g := n.(*grokNormaliser)
	n.Feed(1, grokChunk("agent_message_chunk", "a", "p", "e1"))
	n.Feed(2, grokChunk("agent_thought_chunk", "t", "p", "e2"))
	n.Feed(3, grokChunk("agent_message_chunk", "b", "p", "e3"))
	n.Feed(4, grokUpd(grokJSON{"sessionUpdate": "turn_completed"}, grokJSON{}))
	if len(g.groups) != 0 || len(g.turn) != 0 {
		t.Fatalf("after turn_completed: %d groups, %d turn ids held", len(g.groups), len(g.turn))
	}
	ds := n.Flush()
	if len(ds) != 1 || ds[0].Update == nil || ds[0].Update.Text != "ab" || ds[0].Update.Rev != 3 {
		t.Fatalf("Flush after turn_completed gave %+v, want the pending Update ab rev 3", ds)
	}
	if ds := append(n.Feed(5, grokChunk("agent_message_chunk", "late", "p", "e5")), n.Flush()...); len(ds) != 0 {
		t.Errorf("a chunk for a closed promptId gave %+v, want nothing", ds)
	}
	if ds := n.Feed(6, grokChunk("agent_message_chunk", "new", "q", "e6")); len(ds) != 1 || ds[0].Add == nil {
		t.Errorf("a chunk for a new promptId gave %+v, want an Add", ds)
	}
}

func TestGrokFinishedCallsAreNotRetained(t *testing.T) {
	n := NewGrok()
	g := n.(*grokNormaliser)
	items := feedGrokLines(n,
		grokUpd(grokJSON{"sessionUpdate": "tool_call", "toolCallId": "t", "title": "bash"}, grokJSON{}),
		grokUpd(grokJSON{"sessionUpdate": "task_backgrounded", "task_id": "k", "tool_call_id": "t", "command": "make"}, grokJSON{}),
		grokUpd(grokJSON{"sessionUpdate": "tool_call_update", "toolCallId": "t", "status": "completed"}, grokJSON{}),
	)
	if len(g.calls) != 0 {
		t.Fatalf("%d calls held after the terminal update", len(g.calls))
	}
	if tc := items["tool:t"].ToolCall; tc.Status != ToolOK || tc.ProcessID != "k" {
		t.Errorf("tool call = %+v, want ok linked to process k", tc)
	}
	for _, kind := range []string{"tool_call", "tool_call_update"} {
		if ds := n.Feed(9, grokUpd(grokJSON{"sessionUpdate": kind, "toolCallId": "t", "status": "failed"}, grokJSON{})); len(ds) != 0 {
			t.Errorf("%s for a finished call gave %+v, want nothing", kind, ds)
		}
	}
}

// grokChildRig is a parent session dir and the path of its child file "c".
func grokChildRig(t *testing.T, body string) (dir, child string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "c"), 0o700); err != nil {
		t.Fatal(err)
	}
	child = filepath.Join(root, "c", "updates.jsonl")
	if err := os.WriteFile(child, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "parent"), child
}

func grokSubagentCount(n Normaliser, line string) int {
	items := feedGrokLines(n, grokUpd(grokJSON{"sessionUpdate": line, "subagent_id": "s", "child_session_id": "c", "status": "completed"}, grokJSON{}))
	return items["subagent:s"].Subagent.ItemCount
}

func TestGrokChildCountIsCachedAndSkippedAtSpawn(t *testing.T) {
	body := string(grokChunk("agent_message_chunk", "a", "p", "e1")) + "\n"
	dir, child := grokChildRig(t, body)
	n := NewGrokWith(GrokOptions{SessionDir: dir})
	if got := grokSubagentCount(n, "subagent_spawned"); got != 0 {
		t.Errorf("count at spawn = %d, want 0 (not read)", got)
	}
	if got := grokSubagentCount(n, "subagent_finished"); got != 1 {
		t.Fatalf("count at finish = %d, want 1", got)
	}
	info, err := os.Stat(child)
	if err != nil {
		t.Fatal(err)
	}
	// Same size and mtime: the cache answers without reading the new bytes.
	if err := os.WriteFile(child, []byte(strings.Repeat("x", len(body)-1)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(child, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	n.Reset()
	if got := grokSubagentCount(n, "subagent_finished"); got != 1 {
		t.Errorf("count after Reset = %d, want the cached 1", got)
	}
	if err := os.Chtimes(child, info.ModTime().Add(time.Second), info.ModTime().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	n.Reset()
	if got := grokSubagentCount(n, "subagent_finished"); got != 0 {
		t.Errorf("count after a change = %d, want 0 (reread)", got)
	}
}

func TestGrokChildCountCaps(t *testing.T) {
	chunk := string(grokChunk("agent_message_chunk", "a", "p", "e1")) + "\n"
	long := string(grokChunk("agent_message_chunk", strings.Repeat("z", grokChildMaxLine), "q", "e2")) + "\n"
	dir, _ := grokChildRig(t, long+chunk)
	if got := grokSubagentCount(NewGrokWith(GrokOptions{SessionDir: dir}), "subagent_finished"); got != 1 {
		t.Errorf("count with an over-long line = %d, want 1 (long line skipped)", got)
	}
	lines := grokChildMaxBytes/len(chunk) + 10
	dir, _ = grokChildRig(t, strings.Repeat(chunk, lines))
	if got := grokSubagentCount(NewGrokWith(GrokOptions{SessionDir: dir}), "subagent_finished"); got != lines {
		t.Errorf("count of a file over the cap = %d, want its %d newlines", got, lines)
	}
}
