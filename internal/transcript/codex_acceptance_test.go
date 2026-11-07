package transcript_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/transcript"
)

// replayStep is what one fed line produced.
type replayStep struct {
	pos    int64
	line   []byte
	deltas []transcript.Delta
}

// replay feeds lines to n, applying deltas to an ordered item list the way
// the index does: Add appends, Update replaces by id, Remove drops.
type replay struct {
	t     *testing.T
	n     transcript.Normaliser
	items []transcript.Item
	steps []replayStep
}

func newReplay(t *testing.T, n transcript.Normaliser) *replay {
	return &replay{t: t, n: n}
}

func (r *replay) feed(pos int64, line []byte) []transcript.Delta {
	r.t.Helper()
	ds := r.n.Feed(pos, line)
	r.steps = append(r.steps, replayStep{pos: pos, line: line, deltas: ds})
	r.apply(ds)
	return ds
}

func (r *replay) flush() {
	r.t.Helper()
	ds := r.n.Flush()
	if len(r.steps) > 0 {
		last := &r.steps[len(r.steps)-1]
		last.deltas = append(last.deltas, ds...)
	}
	r.apply(ds)
}

func (r *replay) apply(ds []transcript.Delta) {
	r.t.Helper()
	for _, d := range ds {
		set := 0
		if d.Add != nil {
			set++
		}
		if d.Update != nil {
			set++
		}
		if d.Remove != "" {
			set++
		}
		if set != 1 {
			r.t.Fatalf("delta sets %d fields, want exactly one: %+v", set, d)
		}
		switch {
		case d.Add != nil:
			if r.index(d.Add.ID) >= 0 {
				r.t.Fatalf("Add of an id already listed: %q", d.Add.ID)
			}
			r.items = append(r.items, *d.Add)
		case d.Update != nil:
			i := r.index(d.Update.ID)
			if i < 0 {
				r.t.Fatalf("Update of an unknown id: %q", d.Update.ID)
			}
			r.items[i] = *d.Update
		default:
			i := r.index(d.Remove)
			if i < 0 {
				r.t.Fatalf("Remove of an unknown id: %q", d.Remove)
			}
			r.items = append(r.items[:i], r.items[i+1:]...)
		}
	}
}

func (r *replay) index(id string) int {
	for i, it := range r.items {
		if it.ID == id {
			return i
		}
	}
	return -1
}

func (r *replay) has(id string) bool {
	return r.index(id) >= 0
}

// kinds lists the kinds of items, skipping the given kinds.
func kinds(items []transcript.Item, skip ...transcript.ItemKind) string {
	var out []string
	for _, it := range items {
		skipped := false
		for _, s := range skip {
			skipped = skipped || it.Kind == s
		}
		if !skipped {
			out = append(out, string(it.Kind))
		}
	}
	return strings.Join(out, ",")
}

func byKind(items []transcript.Item, k transcript.ItemKind) []transcript.Item {
	var out []transcript.Item
	for _, it := range items {
		if it.Kind == k {
			out = append(out, it)
		}
	}
	return out
}

func fixtureLines(t *testing.T, path string) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	for _, l := range bytes.SplitAfter(raw, []byte("\n")) {
		if len(bytes.TrimSpace(l)) > 0 {
			out = append(out, l)
		}
	}
	return out
}

// codexLine is the part of a rollout line the tests read expectations from.
type codexLine struct {
	Ordinal int64  `json:"ordinal"`
	Type    string `json:"type"`
	Payload struct {
		Type      string          `json:"type"`
		ID        string          `json:"id"`
		Role      string          `json:"role"`
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Input     string          `json:"input"`
		Arguments string          `json:"arguments"`
		Output    json.RawMessage `json:"output"`
		Item      struct {
			Type             string   `json:"type"`
			ID               string   `json:"id"`
			ProcessID        string   `json:"process_id"`
			Command          []string `json:"command"`
			AggregatedOutput string   `json:"aggregated_output"`
			ExitCode         int      `json:"exit_code"`
			Duration         struct {
				Secs  int64 `json:"secs"`
				Nanos int64 `json:"nanos"`
			} `json:"duration"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			SummaryText []string `json:"summary_text"`
		} `json:"item"`
	} `json:"payload"`
}

func parseCodex(t *testing.T, line []byte) codexLine {
	t.Helper()
	var c codexLine
	if err := json.Unmarshal(line, &c); err != nil {
		t.Fatalf("fixture line is not JSON: %v", err)
	}
	return c
}

// firstOutputText is a tool output's text: the string itself, or the first
// text part of a list.
func firstOutputText(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil || len(parts) == 0 {
		t.Fatalf("tool output has no text: %s", raw)
	}
	return parts[0].Text
}

func replayCodex(t *testing.T, path string) (*replay, []codexLine) {
	t.Helper()
	r := newReplay(t, transcript.NewCodex())
	var parsed []codexLine
	for _, l := range fixtureLines(t, path) {
		c := parseCodex(t, l)
		parsed = append(parsed, c)
		r.feed(c.Ordinal, l)
	}
	r.flush()
	return r, parsed
}

var codexFixture = filepath.Join("testdata", "codex", "rollout.jsonl")

func TestCodexCapabilities(t *testing.T) {
	n := transcript.NewCodex()
	want := transcript.Capabilities{
		Agent:       "codex",
		Scrollback:  transcript.ScrollbackTranscript,
		ToolResults: true,
		Thinking:    transcript.ThinkingSummary,
		Subagents:   true,
		Processes:   transcript.ProcessesInline,
	}
	if n.Kind() != "codex" || n.PositionKind() != transcript.PositionOrdinal || n.Capabilities() != want {
		t.Errorf("codex = %q %q %+v, want codex ordinal %+v", n.Kind(), n.PositionKind(), n.Capabilities(), want)
	}
}

func TestCodexItems(t *testing.T) {
	r, lines := replayCodex(t, codexFixture)

	// One user item per UserMessage (injected response_item user and
	// developer messages are not turns), one assistant item per AgentMessage
	// (its response_item twin shares the id), one thinking item for the only
	// reasoning with a summary (encrypted-only reasoning gives none), three
	// tool calls and one system item for ContextCompaction.
	want := "user,assistant,user,thinking,assistant,tool_call,user,tool_call,assistant,tool_call,system"
	if got := kinds(r.items); got != want {
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

	var userTexts, agentTexts []string
	for _, c := range lines {
		switch c.Payload.Item.Type {
		case "UserMessage":
			userTexts = append(userTexts, c.Payload.Item.Content[0].Text)
		case "AgentMessage":
			var parts []string
			for _, p := range c.Payload.Item.Content {
				parts = append(parts, p.Text)
			}
			agentTexts = append(agentTexts, strings.Join(parts, ""))
		}
	}
	for i, it := range byKind(r.items, transcript.KindUser) {
		if it.Text != userTexts[i] {
			t.Errorf("user %d text = %q, want %q", i, it.Text, userTexts[i])
		}
	}
	for i, it := range byKind(r.items, transcript.KindAssistant) {
		if it.Text != agentTexts[i] {
			t.Errorf("assistant %d text = %q, want %q", i, it.Text, agentTexts[i])
		}
	}
	if th := byKind(r.items, transcript.KindThinking); len(th) != 1 || th[0].Text != "vqzr kplm rrtxo wnuf geb" || th[0].Hidden {
		t.Errorf("thinking = %+v, want the reasoning summary", th)
	}
	if sys := byKind(r.items, transcript.KindSystem); len(sys) != 1 || r.items[len(r.items)-1].Kind != transcript.KindSystem {
		t.Errorf("system items = %+v, want one, last (ContextCompaction)", sys)
	}
}

func TestCodexToolCalls(t *testing.T) {
	r, lines := replayCodex(t, codexFixture)

	type call struct {
		id, name, input, result string
		outputPos               int64
		process                 *codexLine
	}
	var calls []*call
	byID := map[string]*call{}
	var open *call
	for i := range lines {
		c := lines[i]
		p := c.Payload
		switch p.Type {
		case "custom_tool_call", "function_call":
			in := p.Input
			if p.Type == "function_call" {
				in = p.Arguments
			}
			cl := &call{id: p.CallID, name: p.Name, input: in}
			calls = append(calls, cl)
			byID[p.CallID] = cl
			open = cl
		case "custom_tool_call_output", "function_call_output":
			cl := byID[p.CallID]
			cl.result = firstOutputText(t, p.Output)
			cl.outputPos = c.Ordinal
			open = nil
		case "item_completed":
			if p.Item.Type == "CommandExecution" {
				open.process = &lines[i]
			}
		}
	}
	got := byKind(r.items, transcript.KindToolCall)
	if len(got) != len(calls) || len(calls) != 3 {
		t.Fatalf("tool calls = %d, want %d (fixture has 3)", len(got), len(calls))
	}
	sawCustom, sawFunction := false, false
	for i, cl := range calls {
		it := got[i]
		tc := it.ToolCall
		if tc == nil {
			t.Fatalf("tool_call item %s has no ToolCall", it.ID)
		}
		sawCustom = sawCustom || cl.name == "exec"
		sawFunction = sawFunction || cl.name == "wait"
		if tc.ToolID != cl.id || tc.Name != cl.name || tc.Input != cl.input {
			t.Errorf("call %d = %q %q %q, want %q %q %q", i, tc.ToolID, tc.Name, tc.Input, cl.id, cl.name, cl.input)
		}
		if tc.Status != transcript.ToolOK || !strings.Contains(tc.Result, cl.result) {
			t.Errorf("call %d status %q result %q, want ok containing %q", i, tc.Status, tc.Result, cl.result)
		}
		if it.Rev != cl.outputPos {
			t.Errorf("call %d rev = %d, want its output's ordinal %d", i, it.Rev, cl.outputPos)
		}
		if cl.process == nil {
			if it.Process != nil || tc.ProcessID != "" {
				t.Errorf("call %d without CommandExecution has a process: %+v", i, it.Process)
			}
			continue
		}
		ce := cl.process.Payload.Item
		wantMS := ce.Duration.Secs*1000 + ce.Duration.Nanos/1e6
		if tc.ProcessID != ce.ProcessID || tc.DurationMS != wantMS {
			t.Errorf("call %d process id %q duration %d, want %q %d", i, tc.ProcessID, tc.DurationMS, ce.ProcessID, wantMS)
		}
		pr := it.Process
		if pr == nil {
			t.Fatalf("call %d has no Process after CommandExecution", i)
		}
		if pr.ID != ce.ProcessID || pr.Output != transcript.OutputInline || pr.State != transcript.ProcessExited ||
			pr.ExitCode == nil || *pr.ExitCode != ce.ExitCode || !strings.Contains(pr.Command, ce.Command[len(ce.Command)-1]) {
			t.Errorf("call %d process = %+v, want id %q inline exited exit %d command with %q", i, pr, ce.ProcessID, ce.ExitCode, ce.Command[len(ce.Command)-1])
		}
		if it.Text != ce.AggregatedOutput {
			t.Errorf("call %d text = %q, want aggregated_output %q", i, it.Text, ce.AggregatedOutput)
		}
	}
	if !sawCustom || !sawFunction {
		t.Error("fixture lost custom_tool_call or function_call")
	}
}

func TestCodexCallRunningThenUpdated(t *testing.T) {
	r := newReplay(t, transcript.NewCodex())
	var callID string
	for _, l := range fixtureLines(t, codexFixture) {
		c := parseCodex(t, l)
		ds := r.feed(c.Ordinal, l)
		switch c.Payload.Type {
		case "custom_tool_call", "function_call":
			callID = c.Payload.CallID
			var added *transcript.Item
			for _, d := range ds {
				if d.Add != nil && d.Add.Kind == transcript.KindToolCall {
					added = d.Add
				}
			}
			if added == nil || added.ToolCall == nil || added.ToolCall.Status != transcript.ToolRunning || added.Rev != c.Ordinal {
				t.Fatalf("ordinal %d: call not added running at its ordinal: %+v", c.Ordinal, added)
			}
		case "custom_tool_call_output", "function_call_output":
			var upd *transcript.Item
			for _, d := range ds {
				if d.Update != nil && d.Update.ToolCall != nil && d.Update.ToolCall.ToolID == callID {
					upd = d.Update
				}
			}
			if upd == nil || upd.Rev != c.Ordinal || upd.ToolCall.Status != transcript.ToolOK {
				t.Fatalf("ordinal %d: output did not update call %s in place: %+v", c.Ordinal, callID, upd)
			}
		}
	}
}

func TestCodexReset(t *testing.T) {
	r, _ := replayCodex(t, codexFixture)
	first := kinds(r.items)
	if len(r.items) == 0 {
		t.Fatal("fixture produced no items")
	}
	r.n.Reset()
	again := newReplay(t, r.n)
	for _, l := range fixtureLines(t, codexFixture) {
		again.feed(parseCodex(t, l).Ordinal, l)
	}
	again.flush()
	if got := kinds(again.items); got != first || len(again.items) != len(r.items) {
		t.Errorf("after Reset kinds = %s, want %s", got, first)
	}
}

func TestCodexChildHead(t *testing.T) {
	type meta struct {
		Timestamp string `json:"timestamp"`
		Payload   struct {
			ID             string `json:"id"`
			ParentThreadID string `json:"parent_thread_id"`
			Source         struct {
				Subagent json.RawMessage `json:"subagent"`
			} `json:"source"`
		} `json:"payload"`
	}
	head := func(path string) meta {
		var m meta
		if err := json.Unmarshal(fixtureLines(t, path)[0], &m); err != nil {
			t.Fatal(err)
		}
		return m
	}

	review := filepath.Join("testdata", "codex", "child_review.jsonl")
	m := head(review)
	parent, sa, err := transcript.CodexChildHead(review)
	if err != nil {
		t.Fatalf("CodexChildHead(review): %v", err)
	}
	if parent != m.Payload.ParentThreadID || sa.ID != m.Payload.ID || sa.Name != "review" {
		t.Errorf("review child = parent %q id %q name %q, want %q %q review", parent, sa.ID, sa.Name, m.Payload.ParentThreadID, m.Payload.ID)
	}
	if sa.State != transcript.SubagentDone || sa.StartedAt == nil || sa.EndedAt == nil || !sa.EndedAt.After(*sa.StartedAt) {
		t.Errorf("review child with task_complete = %+v, want done with start before end", sa)
	}

	spawn := filepath.Join("testdata", "codex", "child_spawn.jsonl")
	m = head(spawn)
	var ts struct {
		ThreadSpawn struct {
			AgentNickname string `json:"agent_nickname"`
		} `json:"thread_spawn"`
	}
	if err := json.Unmarshal(m.Payload.Source.Subagent, &ts); err != nil || ts.ThreadSpawn.AgentNickname == "" {
		t.Fatalf("spawn fixture has no nickname: %v", err)
	}
	parent, sa, err = transcript.CodexChildHead(spawn)
	if err != nil {
		t.Fatalf("CodexChildHead(spawn): %v", err)
	}
	if parent != m.Payload.ParentThreadID || sa.ID != m.Payload.ID || sa.Name != ts.ThreadSpawn.AgentNickname {
		t.Errorf("spawn child = parent %q id %q name %q, want %q %q %q", parent, sa.ID, sa.Name, m.Payload.ParentThreadID, m.Payload.ID, ts.ThreadSpawn.AgentNickname)
	}
	if sa.State != transcript.SubagentRunning || sa.StartedAt == nil || sa.EndedAt != nil {
		t.Errorf("spawn child with no end = %+v, want running, started, not ended", sa)
	}

	if _, _, err := transcript.CodexChildHead(codexFixture); err == nil {
		t.Error("a parent rollout (no parent_thread_id) read as a child")
	}
	if _, _, err := transcript.CodexChildHead(filepath.Join(t.TempDir(), "missing.jsonl")); err == nil {
		t.Error("missing file gave no error")
	}
}

const plantedToken = "sk-aiman-PLANTED-7f3c9e1d4b"

func TestCodexNoSecretLeak(t *testing.T) {
	lines := []string{
		`{"timestamp":"2026-11-07T10:00:00.000Z","ordinal":0,"type":"session_meta","payload":{"id":"t-1","timestamp":"2026-11-07T10:00:00.000Z","cwd":"/w","base_instructions":{"text":"` + plantedToken + `"}}}`,
		`{"timestamp":"2026-11-07T10:00:01.000Z","ordinal":1,"type":"response_item","payload":{"type":"message","id":"m-dev","role":"developer","content":[{"type":"input_text","text":"key ` + plantedToken + `"}]}}`,
		`{"timestamp":"2026-11-07T10:00:02.000Z","ordinal":2,"type":"turn_context","payload":{"turn_id":"tu-1","cwd":"/w","user_instructions":"` + plantedToken + `"}}`,
		`{"timestamp":"2026-11-07T10:00:03.000Z","ordinal":3,"type":"world_state","payload":{"full":true,"state":{"agents_md":{"text":"` + plantedToken + `"}}}}`,
		`{"timestamp":"2026-11-07T10:00:04.000Z","ordinal":4,"type":"response_item","payload":{"type":"message","id":"m-inj","role":"user","content":[{"type":"input_text","text":"<environment_context>` + plantedToken + `</environment_context>"}]}}`,
		`{"timestamp":"2026-11-07T10:00:05.000Z","ordinal":5,"type":"event_msg","payload":{"type":"item_completed","thread_id":"t-1","turn_id":"tu-1","item":{"type":"UserMessage","id":"u-1","content":[{"type":"Text","text":"run the tests"}]}}}`,
		`{"timestamp":"2026-11-07T10:00:06.000Z","ordinal":6,"type":"response_item","payload":{"type":"reasoning","id":"rs-1","summary":[{"type":"summary_text","text":"thinking about tests"}],"encrypted_content":"` + plantedToken + `"}}`,
		`{"timestamp":"2026-11-07T10:00:07.000Z","ordinal":7,"type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"plan":"` + plantedToken + `"}}}`,
	}
	r := newReplay(t, transcript.NewCodex())
	for _, l := range lines {
		r.feed(parseCodex(t, []byte(l)).Ordinal, []byte(l+"\n"))
	}
	r.flush()
	if got := kinds(r.items); got != "user,thinking" {
		t.Errorf("kinds = %q, want user,thinking", got)
	}
	for _, s := range r.steps {
		raw, err := json.Marshal(s.deltas)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), plantedToken) {
			t.Errorf("ordinal %d: planted token reached a delta: %s", s.pos, raw)
		}
	}
}
