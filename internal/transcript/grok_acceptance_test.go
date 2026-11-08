package transcript_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/transcript"
)

// The fixture is real Grok updates.jsonl lines passed through a redactor:
// string values are hashes, ids are rehashed consistently, enum-like values,
// numbers and booleans are kept. Expected texts are computed from the
// fixture, never hard-coded.
const grokRoot = "testdata/grok/sessions/work-repo"

type grokLine struct {
	pos    int64 // byte offset just past the line, newline included
	raw    []byte
	method string
	kind   string // update.sessionUpdate
	upd    map[string]any
	meta   map[string]any
}

func (l grokLine) str(key string) string { s, _ := l.upd[key].(string); return s }
func (l grokLine) promptID() string      { s, _ := l.meta["promptId"].(string); return s }
func (l grokLine) ms() int64             { f, _ := l.meta["agentTimestampMs"].(float64); return int64(f) }

func (l grokLine) text() string {
	c, _ := l.upd["content"].(map[string]any)
	s, _ := c["text"].(string)
	return s
}

func parseGrokLines(t *testing.T, data []byte) []grokLine {
	t.Helper()
	var out []grokLine
	var pos int64
	for _, raw := range bytes.SplitAfter(data, []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		pos += int64(len(raw))
		var env struct {
			Method string `json:"method"`
			Params struct {
				Update map[string]any `json:"update"`
				Meta   map[string]any `json:"_meta"`
			} `json:"params"`
		}
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("fixture line at %d: %v", pos, err)
		}
		kind, _ := env.Params.Update["sessionUpdate"].(string)
		out = append(out, grokLine{pos: pos, raw: bytes.TrimSuffix(raw, []byte("\n")), method: env.Method,
			kind: kind, upd: env.Params.Update, meta: env.Params.Meta})
	}
	return out
}

// grokFixture returns the parent session folder and its parsed lines.
func grokFixture(t *testing.T) (string, []grokLine) {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(grokRoot, "*", "subagents"))
	if err != nil || len(dirs) != 1 {
		t.Fatalf("want one parent session under %s, got %v %v", grokRoot, dirs, err)
	}
	dir := filepath.Dir(dirs[0])
	data, err := os.ReadFile(filepath.Join(dir, "updates.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return dir, parseGrokLines(t, data)
}

func grokFind(t *testing.T, lines []grokLine, ok func(grokLine) bool) []grokLine {
	t.Helper()
	var out []grokLine
	for _, l := range lines {
		if ok(l) {
			out = append(out, l)
		}
	}
	if len(out) == 0 {
		t.Fatal("fixture is missing a required line shape")
	}
	return out
}

func grokOfKind(kind string) func(grokLine) bool {
	return func(l grokLine) bool { return l.kind == kind }
}

func grokHookFailed(l grokLine) bool {
	if l.kind != "hook_execution" {
		return false
	}
	runs, _ := l.upd["runs"].([]any)
	for _, r := range runs {
		st, _ := r.(map[string]any)["status"].(map[string]any)
		if st["status"] != "success" {
			return true
		}
	}
	return false
}

// grokState applies deltas the way the index does and checks the contract:
// one field per delta, no second Add of an id, no Update of an unknown id.
type grokState struct {
	t     *testing.T
	items map[string]transcript.Item
	order []string
	fed   int64 // position of the last line fed
}

func newGrokState(t *testing.T) *grokState {
	return &grokState{t: t, items: map[string]transcript.Item{}}
}

func (s *grokState) apply(ds []transcript.Delta) {
	s.t.Helper()
	for _, d := range ds {
		set := 0
		for _, b := range []bool{d.Add != nil, d.Update != nil, d.Remove != ""} {
			if b {
				set++
			}
		}
		if set != 1 {
			s.t.Fatalf("delta sets %d fields, want 1: %+v", set, d)
		}
		switch {
		case d.Add != nil:
			if _, ok := s.items[d.Add.ID]; ok {
				s.t.Fatalf("second Add of id %q (kind %s)", d.Add.ID, d.Add.Kind)
			}
			s.items[d.Add.ID] = *d.Add
			s.order = append(s.order, d.Add.ID)
		case d.Update != nil:
			if _, ok := s.items[d.Update.ID]; !ok {
				s.t.Fatalf("Update of unknown id %q (kind %s)", d.Update.ID, d.Update.Kind)
			}
			s.items[d.Update.ID] = *d.Update
		default:
			delete(s.items, d.Remove)
		}
	}
}

func (s *grokState) feed(n transcript.Normaliser, lines []grokLine) {
	s.t.Helper()
	for _, l := range lines {
		s.apply(n.Feed(l.pos, l.raw))
		s.fed = l.pos
	}
	s.apply(n.Flush())
}

func (s *grokState) ofKind(k transcript.ItemKind) []transcript.Item {
	var out []transcript.Item
	for _, id := range s.order {
		if it, ok := s.items[id]; ok && it.Kind == k {
			out = append(out, it)
		}
	}
	return out
}

func feedGrok(t *testing.T, n transcript.Normaliser, lines []grokLine) *grokState {
	t.Helper()
	s := newGrokState(t)
	s.feed(n, lines)
	return s
}

// upTo returns the lines up to and including the one at pos.
func upTo(lines []grokLine, pos int64) []grokLine {
	for i, l := range lines {
		if l.pos == pos {
			return lines[:i+1]
		}
	}
	return lines
}

func between(lines []grokLine, after, through int64) []grokLine {
	var out []grokLine
	for _, l := range lines {
		if l.pos > after && l.pos <= through {
			out = append(out, l)
		}
	}
	return out
}

func TestGrokCapabilities(t *testing.T) {
	n := transcript.NewGrok()
	if n.Kind() != "grok" || n.PositionKind() != transcript.PositionOffset {
		t.Errorf("Kind, PositionKind = %q, %q; want grok, offset", n.Kind(), n.PositionKind())
	}
	want := transcript.Capabilities{
		Agent:       "grok",
		Scrollback:  transcript.ScrollbackTranscript,
		ToolResults: true,
		Thinking:    transcript.ThinkingFull,
		Subagents:   true,
		Processes:   transcript.ProcessesFile,
	}
	if got := n.Capabilities(); got != want {
		t.Errorf("Capabilities = %+v, want %+v", got, want)
	}
}

func TestGrokMethods(t *testing.T) {
	_, lines := grokFixture(t)
	user := grokFind(t, lines, grokOfKind("user_message_chunk"))[0]
	spawn := grokFind(t, lines, grokOfKind("subagent_spawned"))[0]
	if user.method != "session/update" || spawn.method != "_x.ai/session/update" {
		t.Fatalf("fixture methods = %q, %q", user.method, spawn.method)
	}

	s := feedGrok(t, transcript.NewGrok(), []grokLine{user})
	users := s.ofKind(transcript.KindUser)
	if len(users) != 1 || users[0].Text != user.text() || !users[0].At.Equal(time.UnixMilli(user.ms())) || users[0].Rev != user.pos {
		t.Fatalf("session/update user chunk gave %+v, want one user item %q at %d", users, user.text(), user.ms())
	}

	alt := user
	alt.raw = bytes.Replace(user.raw, []byte(`"session/update"`), []byte(`"_x.ai/session/update"`), 1)
	s = feedGrok(t, transcript.NewGrok(), []grokLine{alt})
	if got := s.ofKind(transcript.KindUser); len(got) != 1 || got[0].Text != user.text() {
		t.Errorf("_x.ai/session/update user chunk gave %+v", got)
	}
	s = feedGrok(t, transcript.NewGrok(), []grokLine{spawn})
	if got := s.ofKind(transcript.KindSubagent); len(got) != 1 {
		t.Errorf("_x.ai/session/update subagent_spawned gave %d subagent items", len(got))
	}

	other := user
	other.raw = bytes.Replace(user.raw, []byte(`"session/update"`), []byte(`"session/request_permission"`), 1)
	for _, l := range []grokLine{other, {pos: user.pos, raw: []byte(`{"method":"session/update","params":`)}} {
		if ds := transcript.NewGrok().Feed(l.pos, l.raw); len(ds) != 0 {
			t.Errorf("line %.40q gave %d deltas, want none", l.raw, len(ds))
		}
	}
}

// grokChunks returns the joined chunk text per promptId, the prompt order,
// and the last line of each group.
func grokChunks(lines []grokLine, kind string) (map[string]string, []string, map[string]grokLine) {
	text, last := map[string]string{}, map[string]grokLine{}
	var order []string
	for _, l := range lines {
		if l.kind != kind {
			continue
		}
		p := l.promptID()
		if _, ok := text[p]; !ok {
			order = append(order, p)
		}
		text[p] += l.text()
		last[p] = l
	}
	return text, order, last
}

func TestGrokChunksJoinByPromptID(t *testing.T) {
	_, lines := grokFixture(t)
	for _, c := range []struct {
		chunk string
		kind  transcript.ItemKind
	}{{"agent_message_chunk", transcript.KindAssistant}, {"agent_thought_chunk", transcript.KindThinking}} {
		text, order, last := grokChunks(lines, c.chunk)
		first := grokFind(t, lines, grokOfKind(c.chunk))[0]
		if len(grokFind(t, between(lines, first.pos, last[first.promptID()].pos), grokOfKind("tool_call_update"))) == 0 {
			t.Fatalf("fixture: no tool_call_update inside the %s group", c.chunk)
		}

		s := feedGrok(t, transcript.NewGrok(), lines)
		items := s.ofKind(c.kind)
		if len(items) != len(order) {
			t.Fatalf("%s: %d items for %d promptIds; chunks must join by promptId across interleaved tool updates", c.kind, len(items), len(order))
		}
		for i, p := range order {
			it := items[i]
			if !strings.Contains(it.ID, p) {
				t.Errorf("%s item id %q is not derived from promptId %q", c.kind, it.ID, p)
			}
			if it.Text != text[p] {
				t.Errorf("%s text = %d bytes, want the %d joined chunk bytes", c.kind, len(it.Text), len(text[p]))
			}
			if it.Rev != last[p].pos {
				t.Errorf("%s rev = %d, want %d (last chunk)", c.kind, it.Rev, last[p].pos)
			}
		}
	}
	s := feedGrok(t, transcript.NewGrok(), lines)
	a, th := s.ofKind(transcript.KindAssistant), s.ofKind(transcript.KindThinking)
	if len(a) > 0 && len(th) > 0 && a[0].ID == th[0].ID {
		t.Errorf("assistant and thinking items share id %q", a[0].ID)
	}
}

func TestGrokOpenGroupIsLive(t *testing.T) {
	_, lines := grokFixture(t)
	for _, c := range []struct {
		chunk string
		kind  transcript.ItemKind
	}{{"agent_message_chunk", transcript.KindAssistant}, {"agent_thought_chunk", transcript.KindThinking}} {
		chunks := grokFind(t, lines, grokOfKind(c.chunk))
		p := chunks[0].promptID()
		var group []grokLine
		for _, l := range chunks {
			if l.promptID() == p {
				group = append(group, l)
			}
		}
		if len(group) < 2 {
			t.Fatalf("fixture: %s group %q has %d chunks, want 2+", c.chunk, p, len(group))
		}

		n, s := transcript.NewGrok(), newGrokState(t)
		s.feed(n, upTo(lines, group[0].pos))
		open := s.ofKind(c.kind)
		if len(open) != 1 || !strings.Contains(open[0].ID, p) || open[0].Text != group[0].text() {
			t.Fatalf("%s: after the first chunk and Flush, items = %+v; want one open item for promptId %q", c.kind, open, p)
		}
		id, want := open[0].ID, group[0].text()
		for _, g := range group[1:] {
			s.feed(n, between(lines, s.fed, g.pos))
			want += g.text()
			got := s.ofKind(c.kind)
			if len(got) != 1 || got[0].ID != id || got[0].Text != want || got[0].Rev != g.pos {
				t.Fatalf("%s: open item after chunk at %d = %d items (id %v), want one updated item %q with rev %d",
					c.kind, g.pos, len(got), ids(got), id, g.pos)
			}
		}
		s.feed(n, between(lines, group[len(group)-1].pos, lines[len(lines)-1].pos))
		if got := s.ofKind(c.kind); len(got) != 1 || got[0].ID != id || got[0].Text != want {
			t.Errorf("%s: after turn_completed items = %v, want only %q", c.kind, ids(got), id)
		}
	}
}

func TestGrokToolCall(t *testing.T) {
	_, lines := grokFixture(t)
	var call, done grokLine
	for _, c := range grokFind(t, lines, grokOfKind("tool_call")) {
		if _, ok := c.upd["rawInput"]; !ok {
			continue
		}
		for _, u := range lines {
			_, hasOut := u.upd["rawOutput"]
			_, hasContent := u.upd["content"]
			if u.kind == "tool_call_update" && u.str("toolCallId") == c.str("toolCallId") && u.str("status") == "completed" && hasOut && hasContent {
				call, done = c, u
				break
			}
		}
		if done.raw != nil {
			break
		}
	}
	if done.raw == nil {
		t.Fatal("fixture: no tool_call with rawInput and a completed update with content and rawOutput")
	}
	toolID := call.str("toolCallId")
	find := func(s *grokState) []transcript.Item {
		var out []transcript.Item
		for _, it := range s.ofKind(transcript.KindToolCall) {
			if it.ToolCall != nil && it.ToolCall.ToolID == toolID {
				out = append(out, it)
			}
		}
		return out
	}

	n, s := transcript.NewGrok(), newGrokState(t)
	s.feed(n, upTo(lines, call.pos))
	got := find(s)
	if len(got) != 1 {
		t.Fatalf("after tool_call: %d tool_call items for %q, want 1", len(got), toolID)
	}
	it := got[0]
	if it.ToolCall.Status != transcript.ToolRunning || it.ToolCall.Name == "" || it.ToolCall.Input == "" || it.Rev != call.pos {
		t.Errorf("tool_call item = %+v rev %d, want running with name and input, rev %d", it.ToolCall, it.Rev, call.pos)
	}
	s.feed(n, between(lines, call.pos, done.pos-int64(len(done.raw))-1))
	if got := find(s); len(got) != 1 || got[0].ToolCall.Status != transcript.ToolRunning {
		t.Errorf("before the completed update: %+v, want still running", got)
	}
	s.feed(n, []grokLine{done})
	got = find(s)
	if len(got) != 1 || got[0].ID != it.ID {
		t.Fatalf("after completed update: %v, want the same item %q", ids(got), it.ID)
	}
	if tc := got[0].ToolCall; tc.Status != transcript.ToolOK || tc.Result == "" || got[0].Rev != done.pos {
		t.Errorf("completed tool call = %+v rev %d, want ok with a result, rev %d", tc, got[0].Rev, done.pos)
	}

	calls := map[string]bool{}
	for _, c := range grokFind(t, lines, grokOfKind("tool_call")) {
		calls[c.str("toolCallId")] = true
	}
	all := feedGrok(t, transcript.NewGrok(), lines).ofKind(transcript.KindToolCall)
	if len(all) != len(calls) {
		t.Errorf("%d tool_call items for %d toolCallIds", len(all), len(calls))
	}
}

func TestGrokSubagents(t *testing.T) {
	dir, lines := grokFixture(t)
	spawns := grokFind(t, lines, grokOfKind("subagent_spawned"))
	finished := map[string]grokLine{}
	for _, f := range grokFind(t, lines, grokOfKind("subagent_finished")) {
		finished[f.str("subagent_id")] = f
	}
	childExists := map[string]bool{}
	for _, sp := range spawns {
		_, err := os.Stat(filepath.Join(filepath.Dir(dir), sp.str("child_session_id"), "updates.jsonl"))
		childExists[sp.str("subagent_id")] = err == nil
	}
	var present, missing int
	for _, ok := range childExists {
		if ok {
			present++
		} else {
			missing++
		}
	}
	if present == 0 || missing == 0 {
		t.Fatalf("fixture: want sub-agents with and without a child updates.jsonl, got %d and %d", present, missing)
	}

	byID := func(s *grokState, id string) *transcript.Item {
		for _, it := range s.ofKind(transcript.KindSubagent) {
			if it.Subagent != nil && it.Subagent.ID == id {
				return &it
			}
		}
		return nil
	}

	n, s := transcript.NewGrokWith(transcript.GrokOptions{SessionDir: dir}), newGrokState(t)
	s.feed(n, upTo(lines, spawns[0].pos))
	if it := byID(s, spawns[0].str("subagent_id")); it == nil || it.Subagent.State != transcript.SubagentRunning ||
		it.Subagent.Description != spawns[0].str("description") {
		t.Fatalf("after subagent_spawned: %+v, want a running subagent item with its description", it)
	}

	for name, opts := range map[string]transcript.GrokOptions{
		"session dir":   {SessionDir: dir},
		"no lookups":    {},
		"empty session": {SessionDir: t.TempDir()},
	} {
		s := feedGrok(t, transcript.NewGrokWith(opts), lines)
		if got := len(s.ofKind(transcript.KindSubagent)); got != len(spawns) {
			t.Errorf("%s: %d subagent items for %d spawns", name, got, len(spawns))
		}
		for _, sp := range spawns {
			id := sp.str("subagent_id")
			it := byID(s, id)
			if it == nil {
				t.Errorf("%s: no subagent item for %q (child file present: %v)", name, id, childExists[id])
				continue
			}
			sa, fin := it.Subagent, finished[id]
			if sa.Description != sp.str("description") || sa.State != transcript.SubagentDone {
				t.Errorf("%s: subagent %q = %+v, want description and state done", name, id, sa)
			}
			want := time.Duration(fin.upd["duration_ms"].(float64)) * time.Millisecond
			if sa.StartedAt == nil || sa.EndedAt == nil {
				t.Errorf("%s: subagent %q has no start or end time", name, id)
			} else if d := sa.EndedAt.Sub(*sa.StartedAt) - want; d < -time.Second || d > time.Second {
				t.Errorf("%s: subagent %q ran %v, want %v (duration_ms)", name, id, sa.EndedAt.Sub(*sa.StartedAt), want)
			}
			if it.Rev < fin.pos {
				t.Errorf("%s: subagent %q rev %d, want at least %d (subagent_finished)", name, id, it.Rev, fin.pos)
			}
			switch {
			case name == "session dir" && childExists[id] && sa.ItemCount == 0:
				t.Errorf("subagent %q: child updates.jsonl exists but item_count is 0", id)
			case (name != "session dir" || !childExists[id]) && sa.ItemCount != 0:
				t.Errorf("%s: meta-only subagent %q has item_count %d, want 0", name, id, sa.ItemCount)
			}
		}
	}
}

func TestGrokProcesses(t *testing.T) {
	dir, lines := grokFixture(t)
	bg := grokFind(t, lines, grokOfKind("task_backgrounded"))[0]
	taskID, outFile := bg.str("task_id"), bg.str("output_file")
	var done grokLine
	for _, l := range grokFind(t, lines, grokOfKind("task_completed")) {
		if snap, _ := l.upd["task_snapshot"].(map[string]any); snap["task_id"] == taskID {
			done = l
		}
	}
	if done.raw == nil || !regexp.MustCompile(`/terminal/call-[0-9a-f-]+-\d+\.log$`).MatchString(outFile) {
		t.Fatalf("fixture: task_completed for %q missing or output_file %q not a terminal call log", taskID, outFile)
	}
	if _, err := os.Stat(filepath.Join(dir, "terminal", filepath.Base(outFile))); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	snap := done.upd["task_snapshot"].(map[string]any)

	n, s := transcript.NewGrok(), newGrokState(t)
	s.feed(n, upTo(lines, bg.pos))
	procs := s.ofKind(transcript.KindProcess)
	if len(procs) != 1 || procs[0].Process == nil {
		t.Fatalf("after task_backgrounded: %d process items, want 1", len(procs))
	}
	p := procs[0]
	if p.Process.ID != taskID || p.Process.Command != bg.str("command") || p.Process.State != transcript.ProcessRunning ||
		p.Process.Output != transcript.OutputFile || p.Rev != bg.pos {
		t.Errorf("process = %+v rev %d, want id %q, its command, running, output file, rev %d", p.Process, p.Rev, taskID, bg.pos)
	}
	pp, ok := n.(transcript.ProcessPather)
	if !ok {
		t.Fatal("Grok normaliser does not implement ProcessPather")
	}
	if got, ok := pp.ProcessPath(taskID); !ok || got != outFile {
		t.Errorf("ProcessPath(%q) = %q %v, want %q", taskID, got, ok, outFile)
	}
	if _, ok := pp.ProcessPath("no-such-task"); ok {
		t.Error("ProcessPath found an unknown id")
	}

	s.feed(n, between(lines, bg.pos, done.pos))
	procs = s.ofKind(transcript.KindProcess)
	if len(procs) != 1 || procs[0].ID != p.ID {
		t.Fatalf("after task_completed: process items %v, want only %q", ids(procs), p.ID)
	}
	if pr := procs[0].Process; pr.State != transcript.ProcessExited || pr.ExitCode == nil ||
		*pr.ExitCode != int(snap["exit_code"].(float64)) || procs[0].Rev < done.pos {
		t.Errorf("completed process = %+v rev %d, want exited with code %v, rev at least %d", pr, procs[0].Rev, snap["exit_code"], done.pos)
	}

	if got := feedGrok(t, transcript.NewGrok(), lines).ofKind(transcript.KindProcess); len(got) != len(grokFind(t, lines, grokOfKind("task_backgrounded"))) {
		t.Errorf("%d process items for the whole file; background_tasks must not add processes", len(got))
	}
}

func TestGrokSystemItems(t *testing.T) {
	_, lines := grokFixture(t)
	compaction := grokFind(t, lines, func(l grokLine) bool {
		return strings.HasPrefix(l.kind, "auto_compact_") || l.kind == "compaction_checkpoint"
	})
	systemPos := map[int64]bool{}
	adds := 0
	for _, l := range compaction {
		for _, d := range transcript.NewGrok().Feed(l.pos, l.raw) {
			it := d.Add
			if it == nil {
				it = d.Update
			}
			if it == nil || it.Kind != transcript.KindSystem {
				t.Errorf("%s gave %+v, want only system items", l.kind, d)
				continue
			}
			adds++
		}
		systemPos[l.pos] = true
	}
	if adds == 0 {
		t.Error("compaction lines gave no system item")
	}

	failed := grokFind(t, lines, grokHookFailed)
	for _, l := range failed {
		s := feedGrok(t, transcript.NewGrok(), []grokLine{l})
		if sys := s.ofKind(transcript.KindSystem); len(s.items) != 1 || len(sys) != 1 || sys[0].Text == "" || sys[0].Rev != l.pos {
			t.Errorf("failed hook_execution gave %+v, want one system item with text", s.items)
		}
		systemPos[l.pos] = true
	}

	quiet := grokFind(t, lines, func(l grokLine) bool {
		return (l.kind == "hook_execution" && !grokHookFailed(l)) || l.kind == "retry_state" || l.kind == "zz_future_update"
	})
	for _, l := range quiet {
		if ds := transcript.NewGrok().Feed(l.pos, l.raw); len(ds) != 0 {
			t.Errorf("%s at %d gave %d deltas, want none (successful hooks and unknown updates are dropped)", l.kind, l.pos, len(ds))
		}
	}

	s := feedGrok(t, transcript.NewGrok(), lines)
	for _, it := range s.ofKind(transcript.KindSystem) {
		if !systemPos[it.Rev] {
			t.Errorf("system item %q at rev %d does not come from a compaction or failed hook line", it.ID, it.Rev)
		}
	}
	quietPos := map[int64]bool{}
	for _, l := range quiet {
		quietPos[l.pos] = true
	}
	for _, it := range s.items {
		if quietPos[it.Rev] {
			t.Errorf("item %q (%s) was changed by a dropped line at %d", it.ID, it.Kind, it.Rev)
		}
	}
}

func TestGrokNoSecretLeak(t *testing.T) {
	dir, lines := grokFixture(t)
	leak := regexp.MustCompile(`xai-[A-Za-z0-9]{16,}|sk-[A-Za-z0-9_-]{16,}|AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{20,}|-----BEGIN|/home/code|Bearer [A-Za-z0-9]`)
	err := filepath.WalkDir(filepath.Dir(filepath.Dir(dir)), func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err == nil && leak.Match(b) {
			t.Errorf("fixture %s holds a secret-shaped or host-specific string", p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// The planted token sits only in fields no item shows: a successful hook
	// run, an unknown update, chunk _meta and turn_completed.
	const token = "xai-PLANTEDtok3n0123456789abcdefZZ"
	plant := func(l grokLine, edit func(env map[string]any)) grokLine {
		var env map[string]any
		if err := json.Unmarshal(l.raw, &env); err != nil {
			t.Fatal(err)
		}
		edit(env)
		raw, err := json.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		l.raw = raw
		return l
	}
	upd := func(env map[string]any) map[string]any {
		return env["params"].(map[string]any)["update"].(map[string]any)
	}
	planted := 0
	var fed []grokLine
	for _, l := range lines {
		switch {
		case l.kind == "hook_execution" && !grokHookFailed(l):
			l = plant(l, func(env map[string]any) {
				run := upd(env)["runs"].([]any)[0].(map[string]any)
				run["stdout"] = token
			})
			planted++
		case l.kind == "zz_future_update" || l.kind == "turn_completed":
			l = plant(l, func(env map[string]any) { upd(env)["note"] = token })
			planted++
		case l.kind == "agent_message_chunk" || l.kind == "agent_thought_chunk":
			l = plant(l, func(env map[string]any) { env["params"].(map[string]any)["_meta"].(map[string]any)["auth"] = token })
			planted++
		}
		fed = append(fed, l)
	}
	if planted < 4 {
		t.Fatalf("planted the token in %d lines, want 4+", planted)
	}
	s := feedGrok(t, transcript.NewGrok(), fed)
	if len(s.items) == 0 {
		t.Fatal("no items; the leak check would pass vacuously")
	}
	out, err := json.Marshal(struct {
		Items map[string]transcript.Item
		Caps  transcript.Capabilities
	}{s.items, transcript.NewGrok().Capabilities()})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte(token)) || bytes.Contains(out, []byte("PLANTED")) {
		t.Error("planted token reached a normalised item")
	}
}
