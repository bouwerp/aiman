package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func codexEventLine(ord int, item string) string {
	return fmt.Sprintf(`{"timestamp":"2026-11-07T10:00:%02d.000Z","ordinal":%d,"type":"event_msg","payload":{"type":"item_completed","item":%s}}`, ord%60, ord, item)
}

func codexResponseLine(ord int, payload string) string {
	return fmt.Sprintf(`{"timestamp":"2026-11-07T10:00:%02d.000Z","ordinal":%d,"type":"response_item","payload":%s}`, ord%60, ord, payload)
}

func codexCommandItem(id, pid, output string, exit int) string {
	return fmt.Sprintf(`{"type":"CommandExecution","id":%q,"process_id":%q,"command":["bash","-lc","go test"],"status":"completed","aggregated_output":%q,"exit_code":%d,"duration":{"secs":1,"nanos":500000000}}`, id, pid, output, exit)
}

const (
	codexCallA = `{"type":"custom_tool_call","call_id":"call-a","name":"exec","input":"run a"}`
	codexOutA  = `{"type":"custom_tool_call_output","call_id":"call-a","output":"done a"}`
)

// codexTestFeed feeds lines in order, using each line's index as its ordinal,
// and returns the items listed at the end. It fails on an Add of a listed id
// and on an Update of an unlisted one, as the index would mis-handle both.
func codexTestFeed(t *testing.T, n Normaliser, lines ...string) []Item {
	t.Helper()
	var items []Item
	for i, l := range lines {
		for _, d := range n.Feed(int64(i), []byte(l)) {
			switch {
			case d.Add != nil:
				for _, it := range items {
					if it.ID == d.Add.ID {
						t.Fatalf("line %d: Add of listed id %q", i, it.ID)
					}
				}
				items = append(items, *d.Add)
			case d.Update != nil:
				found := false
				for j := range items {
					if items[j].ID == d.Update.ID {
						items[j] = *d.Update
						found = true
					}
				}
				if !found {
					t.Fatalf("line %d: Update of unlisted id %q", i, d.Update.ID)
				}
			default:
				t.Fatalf("line %d: unexpected delta %+v", i, d)
			}
		}
	}
	return items
}

func codexTestKinds(items []Item) string {
	var out []string
	for _, it := range items {
		out = append(out, string(it.Kind))
	}
	return strings.Join(out, ",")
}

func TestCodexCommandWithNoOpenCallIsStandalone(t *testing.T) {
	items := codexTestFeed(t, NewCodex(),
		codexEventLine(0, codexCommandItem("ce-1", "p1", "before any call", 0)),
		codexResponseLine(1, codexCallA),
		codexResponseLine(2, codexOutA),
		codexEventLine(3, codexCommandItem("ce-2", "p2", "after the output", 3)),
	)
	if got := codexTestKinds(items); got != "process,tool_call,process" {
		t.Fatalf("kinds = %s", got)
	}
	last := items[2]
	if last.ID != "ce-2" || last.Rev != 3 || last.Text != "after the output" {
		t.Errorf("standalone = %+v", last)
	}
	pr := last.Process
	if pr == nil || pr.ID != "p2" || pr.Output != OutputInline || pr.State != ProcessExited || pr.ExitCode == nil || *pr.ExitCode != 3 {
		t.Errorf("process = %+v", pr)
	}
	if items[1].Process != nil || items[1].ToolCall.Status != ToolOK {
		t.Errorf("a closed call took a command: %+v", items[1])
	}
}

func TestCodexSecondCommandOnACallIsStandalone(t *testing.T) {
	items := codexTestFeed(t, NewCodex(),
		codexResponseLine(0, codexCallA),
		codexEventLine(1, codexCommandItem("ce-1", "p1", "first", 0)),
		codexEventLine(2, codexCommandItem("ce-2", "p2", "second", 0)),
		codexResponseLine(3, codexOutA),
	)
	if got := codexTestKinds(items); got != "tool_call,process" {
		t.Fatalf("kinds = %s", got)
	}
	call := items[0]
	if call.ToolCall.ProcessID != "p1" || call.Text != "first" || call.ToolCall.DurationMS != 1500 {
		t.Errorf("call = %+v %+v", call, call.ToolCall)
	}
	if items[1].Process.ID != "p2" || items[1].Text != "second" {
		t.Errorf("second command = %+v", items[1])
	}
}

func TestCodexFailedCommandMarksCallError(t *testing.T) {
	items := codexTestFeed(t, NewCodex(),
		codexResponseLine(0, codexCallA),
		codexEventLine(1, codexCommandItem("ce-1", "p1", "boom", 2)),
		codexResponseLine(2, codexOutA),
	)
	if tc := items[0].ToolCall; tc.Status != ToolError || tc.Result != "done a" {
		t.Errorf("call = %+v, want error with its result", tc)
	}
}

func TestCodexLaterFailedCommandMarksCallError(t *testing.T) {
	items := codexTestFeed(t, NewCodex(),
		codexResponseLine(0, codexCallA),
		codexEventLine(1, codexCommandItem("ce-1", "p1", "fine", 0)),
		codexEventLine(2, codexCommandItem("ce-2", "p2", "boom", 1)),
		codexResponseLine(3, codexOutA),
	)
	if got := codexTestKinds(items); got != "tool_call,process" {
		t.Fatalf("kinds = %s", got)
	}
	if tc := items[0].ToolCall; tc.Status != ToolError || tc.ProcessID != "p1" || tc.Result != "done a" {
		t.Errorf("call = %+v, want error, first process, its result", tc)
	}
	if items[1].Rev != 2 {
		t.Errorf("standalone rev = %d, want its own ordinal", items[1].Rev)
	}
}

func TestCodexChildNameOther(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	head := `{"timestamp":"2026-11-07T10:00:00.000Z","type":"session_meta","payload":{"id":"c","parent_thread_id":"p","source":{"subagent":{"other":"guardian"}}}}` + "\n"
	if err := os.WriteFile(path, []byte(head), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, sa, err := CodexChildHead(path); err != nil || sa.Name != "guardian" {
		t.Errorf("name = %q, err %v", sa.Name, err)
	}
}

func TestCodexCommandWithoutProcessIDUsesItemID(t *testing.T) {
	items := codexTestFeed(t, NewCodex(),
		codexEventLine(0, `{"type":"CommandExecution","id":"ce-1","process_id":null,"command":["ls"],"aggregated_output":"x","exit_code":0}`),
	)
	if len(items) != 1 || items[0].Process.ID != "ce-1" {
		t.Errorf("items = %+v", items)
	}
}

func TestCodexCompactionListedOnce(t *testing.T) {
	compacted := `{"timestamp":"2026-11-07T10:00:00.000Z","ordinal":0,"type":"compacted","payload":{"message":"SECRET SUMMARY","replacement_history":[]}}`
	cc := codexEventLine(2, `{"type":"ContextCompaction","id":"cc-1"}`)
	items := codexTestFeed(t, NewCodex(), compacted, `{"type":"token_usage_record"}`, cc)
	if got := codexTestKinds(items); got != "system" {
		t.Fatalf("kinds = %s, want one system item for one compaction", got)
	}
	if strings.Contains(items[0].Text, "SECRET") {
		t.Errorf("compaction summary surfaced: %q", items[0].Text)
	}

	// A later compaction with no compacted line still shows.
	later := codexEventLine(4, `{"type":"ContextCompaction","id":"cc-2"}`)
	user := codexEventLine(3, `{"type":"UserMessage","id":"u-1","content":[{"text":"hi"}]}`)
	items = codexTestFeed(t, NewCodex(), compacted, `{}`, cc, user, later)
	if got := codexTestKinds(items); got != "system,user,system" {
		t.Errorf("kinds = %s", got)
	}
}

func TestCodexCollabAgentToolCallIsSystem(t *testing.T) {
	items := codexTestFeed(t, NewCodex(),
		codexEventLine(0, `{"type":"CollabAgentToolCall","id":"c-1","tool":"wait","status":"completed","agents_states":{"a":"x"}}`),
	)
	if got := codexTestKinds(items); got != "system" || items[0].Text != "Sub-agent wait completed" {
		t.Errorf("items = %+v", items)
	}
}

func TestCodexTurnAbortEndsOpenCalls(t *testing.T) {
	aborted := `{"timestamp":"2026-11-07T10:00:01.000Z","ordinal":1,"type":"event_msg","payload":{"type":"turn_aborted"}}`
	items := codexTestFeed(t, NewCodex(),
		codexResponseLine(0, codexCallA),
		aborted,
		codexEventLine(2, codexCommandItem("ce-1", "p1", "late", 0)),
		codexResponseLine(3, codexOutA),
	)
	if got := codexTestKinds(items); got != "tool_call,process" {
		t.Fatalf("kinds = %s, want the late command standalone", got)
	}
	if tc := items[0].ToolCall; tc.Status != ToolError || items[0].Rev != 1 {
		t.Errorf("aborted call = %+v rev %d", tc, items[0].Rev)
	}
}

func TestCodexIgnoresBadAndUnknownLines(t *testing.T) {
	items := codexTestFeed(t, NewCodex(),
		`not json`,
		`{"type":"response_item","payload":"not an object"}`,
		codexEventLine(2, `{"type":"McpToolCall","id":"m-1","content":"a string, not a list"}`),
		codexEventLine(3, `{"type":"UserMessage","id":"u-1","content":[]}`),
		codexResponseLine(4, `{"type":"custom_tool_call_output","call_id":"nobody","output":"x"}`),
	)
	if len(items) != 0 {
		t.Errorf("items = %+v", items)
	}
}

func TestCodexOutputShapes(t *testing.T) {
	for raw, want := range map[string]string{
		`"plain"`: "plain",
		`[{"type":"input_text","text":"a"},{"type":"input_text","text":"b"}]`: "a\nb",
		`{"content":"x","success":false}`:                                     `{"content":"x","success":false}`,
	} {
		if got := codexOutputText([]byte(raw)); got != want {
			t.Errorf("output %s = %q, want %q", raw, got, want)
		}
	}
}

func codexTestChild(t *testing.T, tail ...string) string {
	t.Helper()
	head := `{"timestamp":"2026-11-07T10:00:00.000Z","type":"session_meta","payload":{"id":"child","parent_thread_id":"parent","source":{"subagent":"review"}}}`
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	body := head + "\n" + strings.Join(tail, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCodexChildState(t *testing.T) {
	complete := `{"timestamp":"2026-11-07T10:01:00.000Z","type":"event_msg","payload":{"type":"task_complete"}}`
	started := `{"timestamp":"2026-11-07T10:02:00.000Z","type":"event_msg","payload":{"type":"task_started"}}`
	final := `{"timestamp":"2026-11-07T10:03:00.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","phase":"final_answer"}}}`
	commentary := `{"timestamp":"2026-11-07T10:03:00.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","phase":"commentary"}}}`
	aborted := `{"timestamp":"2026-11-07T10:04:00.000Z","type":"event_msg","payload":{"type":"turn_aborted"}}`
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"no end", []string{commentary}, SubagentRunning},
		{"task_complete", []string{complete}, SubagentDone},
		{"new turn after complete", []string{complete, started}, SubagentRunning},
		{"final AgentMessage", []string{started, final}, SubagentDone},
		{"commentary is not an end", []string{started, commentary}, SubagentRunning},
		{"aborted", []string{started, aborted}, SubagentFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parent, sa, err := CodexChildHead(codexTestChild(t, c.lines...))
			if err != nil {
				t.Fatal(err)
			}
			if parent != "parent" || sa.State != c.want {
				t.Errorf("parent %q state %q, want %q", parent, sa.State, c.want)
			}
			if (sa.EndedAt != nil) != (c.want != SubagentRunning) {
				t.Errorf("state %q has EndedAt %v", sa.State, sa.EndedAt)
			}
		})
	}
}

// The end of a rollout larger than the tail window still decides its state,
// and the partial line the window starts in is not misread.
func TestCodexChildStateInLargeRollout(t *testing.T) {
	filler := fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"text":%q}]}}`, strings.Repeat("x", 4096))
	lines := make([]string, 0, 1+2*codexTailBytes/4096)
	for range 2 * codexTailBytes / 4096 {
		lines = append(lines, filler)
	}
	lines = append(lines, `{"timestamp":"2026-11-07T10:01:00.000Z","type":"event_msg","payload":{"type":"task_complete"}}`)
	_, sa, err := CodexChildHead(codexTestChild(t, lines...))
	if err != nil {
		t.Fatal(err)
	}
	if sa.State != SubagentDone || sa.Name != "review" || sa.StartedAt == nil {
		t.Errorf("subagent = %+v", sa)
	}
}

func TestCodexChildHeadRejectsOtherFirstLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	for _, first := range []string{
		"",
		"not json\n",
		`{"type":"event_msg","payload":{"parent_thread_id":"p"}}` + "\n",
		`{"type":"session_meta","payload":{"id":"x"}}` + "\n",
	} {
		if err := os.WriteFile(path, []byte(first), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := CodexChildHead(path); err == nil {
			t.Errorf("first line %q read as a child", first)
		}
	}
}

func TestSummariseToolInput(t *testing.T) {
	for input, want := range map[string]string{
		`{"cmd":"go test ./...","timeout":40}`: "go test ./...",
		`{"path":"a.go"}`:                      "a.go",
		`{"other":"x"}`:                        "",
		"free text\nsecond line":               "free text",
		`  {not json`:                          "{not json",
	} {
		if got := summariseToolInput(input); got != want {
			t.Errorf("summary of %q = %q, want %q", input, got, want)
		}
	}
}
