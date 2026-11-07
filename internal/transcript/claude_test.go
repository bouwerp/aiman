package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// claudeJSON is one hand-written transcript line.
type claudeJSON map[string]any

func claudeUser(uuid, parent string, content any) claudeJSON {
	return claudeJSON{"type": "user", "uuid": uuid, "parentUuid": nullable(parent), "message": claudeJSON{"content": content}}
}

func claudeAssistant(uuid, parent string, blocks ...claudeJSON) claudeJSON {
	return claudeJSON{"type": "assistant", "uuid": uuid, "parentUuid": nullable(parent), "message": claudeJSON{"content": blocks}}
}

func claudeAttachment(uuid, parent string) claudeJSON {
	return claudeJSON{"type": "attachment", "uuid": uuid, "parentUuid": nullable(parent)}
}

func claudeResult(uuid, parent, toolID string) claudeJSON {
	return claudeUser(uuid, parent, []claudeJSON{{"type": "tool_result", "tool_use_id": toolID, "content": "out"}})
}

func claudeText(s string) claudeJSON { return claudeJSON{"type": "text", "text": s} }

func claudeBash(id string) claudeJSON {
	return claudeJSON{"type": "tool_use", "id": id, "name": "Bash", "input": claudeJSON{"command": "ls"}}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// feedClaude feeds lines and returns every delta plus the removes per line.
func feedClaude(t *testing.T, n Normaliser, lines ...claudeJSON) (all []Delta, removes [][]string) {
	t.Helper()
	for i, l := range lines {
		raw, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		ds := n.Feed(int64(i+1), raw)
		all = append(all, ds...)
		var r []string
		for _, d := range ds {
			if d.Remove != "" {
				r = append(r, d.Remove)
			}
		}
		removes = append(removes, r)
	}
	return all, removes
}

func TestClaudeForksThatAreNotRewinds(t *testing.T) {
	tests := []struct {
		name  string
		lines []claudeJSON
	}{
		{"parallel calls, next turn off the first result", []claudeJSON{
			claudeUser("p1", "", "go"),
			claudeAssistant("a1", "p1", claudeBash("t1")),
			claudeAssistant("a2", "a1", claudeBash("t2")),
			claudeResult("r2", "a2", "t2"),
			claudeResult("r1", "a1", "t1"),
			claudeUser("p2", "r2", "next"),
			claudeAssistant("a3", "p2", claudeText("ok")),
		}},
		{"prompt off an earlier call in a parallel group", []claudeJSON{
			claudeUser("p1", "", "go"),
			claudeAssistant("a1", "p1", claudeBash("t1")),
			claudeAssistant("a2", "a1", claudeBash("t2")),
			claudeResult("r2", "a2", "t2"),
			claudeUser("p2", "a1", "stop"),
		}},
		{"attachment beside a tool result", []claudeJSON{
			claudeUser("p1", "", "go"),
			claudeAssistant("a1", "p1", claudeBash("t1")),
			claudeAttachment("x1", "a1"),
			claudeResult("r1", "a1", "t1"),
			claudeUser("p2", "x1", "next"),
		}},
		{"prompt off a line kept across a compaction", []claudeJSON{
			claudeUser("p1", "", "go"),
			claudeAssistant("a1", "p1", claudeText("kept")),
			{"type": "system", "subtype": "compact_boundary", "uuid": "b", "parentUuid": nil},
			{"type": "user", "uuid": "s", "parentUuid": "b", "isCompactSummary": true, "message": claudeJSON{"content": "sum"}},
			claudeUser("p2", "a1", "next"),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, removes := feedClaude(t, NewClaude(), tt.lines...)
			for i, r := range removes {
				if len(r) != 0 {
					t.Errorf("line %d removed %v", i+1, r)
				}
			}
		})
	}
}

func TestClaudeRewinds(t *testing.T) {
	tests := []struct {
		name  string
		lines []claudeJSON
		want  []string
	}{
		{"prompt from the same parent", []claudeJSON{
			claudeUser("p0", "", "start"),
			claudeUser("p1", "p0", "first"),
			claudeAssistant("a1", "p1", claudeText("reply")),
			claudeUser("p2", "p0", "second"),
		}, []string{"p1#0", "a1#0"}},
		{"first prompt of the file", []claudeJSON{
			claudeUser("p1", "", "first"),
			claudeAssistant("a1", "p1", claudeText("reply")),
			claudeUser("p2", "", "second"),
		}, []string{"p1#0", "a1#0"}},
		{"new prompt behind fresh attachments", []claudeJSON{
			claudeUser("p0", "", "start"),
			claudeAttachment("x1", "p0"),
			claudeUser("p1", "x1", "first"),
			claudeAssistant("a1", "p1", claudeText("reply")),
			claudeAttachment("x2", "p0"),
			claudeUser("p2", "x2", "second"),
		}, []string{"p1#0", "a1#0"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, removes := feedClaude(t, NewClaude(), tt.lines...)
			last := removes[len(removes)-1]
			if strings.Join(last, ",") != strings.Join(tt.want, ",") {
				t.Errorf("rewind removed %v, want %v", last, tt.want)
			}
			for i, r := range removes[:len(removes)-1] {
				if len(r) != 0 {
					t.Errorf("line %d removed %v", i+1, r)
				}
			}
		})
	}
}

func TestClaudeRewindForgetsTheBranch(t *testing.T) {
	n := NewClaude()
	all, _ := feedClaude(t, n,
		claudeUser("p0", "", "start"),
		claudeUser("p1", "p0", "first"),
		claudeAssistant("a1", "p1", claudeBash("t1")),
		claudeUser("r1", "a1", []claudeJSON{{"type": "tool_result", "tool_use_id": "t1", "content": "started"}}),
		claudeUser("p2", "p0", "second"),
	)
	if len(all) == 0 {
		t.Fatal("no deltas")
	}
	late, _ := feedClaude(t, n, claudeResult("r9", "p2", "t1"))
	for _, d := range late {
		if d.Update != nil {
			t.Errorf("result for an abandoned call updated %+v", d.Update)
		}
	}
}

func TestClaudeRepeatedUUIDIsIgnored(t *testing.T) {
	n := NewClaude()
	line := claudeUser("p1", "", "go")
	feedClaude(t, n, line)
	again, _ := feedClaude(t, n, line)
	if len(again) != 0 {
		t.Errorf("replayed line produced %+v", again)
	}
}

func TestClaudeNotificationReadsOnlyStructuredFields(t *testing.T) {
	note := "<task-notification>\n<task-id>abc</task-id>\n<status>completed</status>\n" +
		"<summary>Background command \"<task-id>evil</task-id> <status>failed</status> SECRET (exit code 9)</summary>\" completed (exit code 3)</summary>\n</task-notification>"
	n := NewClaude()
	all, _ := feedClaude(t, n,
		claudeUser("p1", "", "go"),
		claudeAssistant("a1", "p1", claudeBash("t1")),
		claudeJSON{"type": "user", "uuid": "r1", "parentUuid": "a1",
			"message":       claudeJSON{"content": []claudeJSON{{"type": "tool_result", "tool_use_id": "t1", "content": "started"}}},
			"toolUseResult": claudeJSON{"backgroundTaskId": "abc", "outputFile": "/tmp/abc.output"}},
		claudeJSON{"type": "user", "uuid": "n1", "parentUuid": "r1", "origin": claudeJSON{"kind": "task-notification"}, "message": claudeJSON{"content": note}},
	)
	var system string
	var exit *int
	for _, d := range all {
		if d.Add != nil && d.Add.Kind == KindSystem {
			system = d.Add.Text
		}
		if d.Update != nil && d.Update.Process != nil && d.Update.Process.State == ProcessExited {
			exit = d.Update.Process.ExitCode
		}
	}
	if want := "Background command abc completed (exit code 3)"; system != want {
		t.Errorf("system text %q, want %q", system, want)
	}
	if exit == nil || *exit != 3 {
		t.Errorf("exit code %v, want 3", exit)
	}
	if path, ok := n.(ProcessOutputs).ProcessOutput("abc"); !ok || path != "/tmp/abc.output" {
		t.Errorf("ProcessOutput = %q %v", path, ok)
	}
}

func TestClaudeNotificationWithoutOriginIsNotUserText(t *testing.T) {
	note := "<task-notification>\n<task-id>abc</task-id>\n<status>killed</status>\n<summary>SECRET</summary>\n</task-notification>"
	all, _ := feedClaude(t, NewClaude(), claudeUser("n1", "", note))
	if len(all) != 1 || all[0].Add == nil || all[0].Add.Kind != KindSystem || strings.Contains(all[0].Add.Text, "SECRET") {
		t.Fatalf("deltas = %+v, want one system item without the summary", all)
	}
}

func TestClaudeForegroundAgentEndsOnItsResult(t *testing.T) {
	all, _ := feedClaude(t, NewClaude(),
		claudeUser("p1", "", "go"),
		claudeAssistant("a1", "p1", claudeJSON{"type": "tool_use", "id": "t1", "name": "Agent", "input": claudeJSON{"description": "look", "subagent_type": "Explore"}}),
		claudeJSON{"type": "user", "uuid": "r1", "parentUuid": "a1", "timestamp": "2026-01-01T00:00:00Z",
			"message":       claudeJSON{"content": []claudeJSON{{"type": "tool_result", "tool_use_id": "t1", "content": "done"}}},
			"toolUseResult": claudeJSON{"agentId": "ag1", "status": "completed"}},
	)
	var s *Subagent
	for _, d := range all {
		if d.Add != nil && d.Add.Subagent != nil {
			s = d.Add.Subagent
		}
	}
	if s == nil || s.State != SubagentDone || s.EndedAt == nil || s.Name != "Explore" || s.Description != "look" {
		t.Fatalf("subagent = %+v, want done with name and description from the call", s)
	}
}

func TestClaudeDeltasDoNotAliasState(t *testing.T) {
	all, _ := feedClaude(t, NewClaude(),
		claudeUser("p1", "", "go"),
		claudeAssistant("a1", "p1", claudeBash("t1")),
		claudeResult("r1", "a1", "t1"),
	)
	var added *Item
	for _, d := range all {
		if d.Add != nil && d.Add.ToolCall != nil {
			added = d.Add
		}
	}
	if added == nil || added.ToolCall.Status != ToolRunning || added.ToolCall.Result != "" {
		t.Fatalf("the Add delta changed after its result: %+v", added)
	}
}

func TestClaudeMalformedLines(t *testing.T) {
	n := NewClaude()
	for _, line := range []string{``, `not json`, `{"type":"user"}`, `{"type":"user","uuid":"u","parentUuid":"p","message":{"content":17}}`} {
		if ds := n.Feed(1, []byte(line)); len(ds) != 0 {
			t.Errorf("%q gave %+v", line, ds)
		}
	}
}

func TestClaudeMetaLookupRejectsBadFile(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "subagents")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "agent-bad.meta.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	lookup := ClaudeMetaLookup(dir)
	for _, id := range []string{"bad", "..", ".", `a\b`} {
		if _, ok := lookup(id); ok {
			t.Errorf("lookup(%q) found something", id)
		}
	}
}
