package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
			grokUpd(grokJSON{"sessionUpdate": "subagent_spawned", "subagent_id": "s", "child_session_id": childID}, grokJSON{}))
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
