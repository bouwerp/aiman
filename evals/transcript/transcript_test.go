//go:build acceptance

package transcripteval

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
)

// Sessions. vendor is found by the Claude vendor rule (cwd slug and
// AgentSessionID); path only by its AgentSessionPath; pane runs bash and has
// no transcript; other is a second session used as a caller.
const (
	sessVendor = "tr-vendor"
	sessPath   = "tr-path"
	sessPane   = "tr-pane"
	sessOther  = "tr-other"
)

type itemReply struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Text     string `json:"text"`
	ToolCall *struct {
		ToolID string `json:"tool_id"`
		Status string `json:"status"`
	} `json:"tool_call"`
}

type pageReply struct {
	Type         string `json:"type"`
	Session      string `json:"session"`
	Source       string `json:"source"`
	Capabilities struct {
		Agent      string `json:"agent"`
		Scrollback string `json:"scrollback"`
	} `json:"capabilities"`
	Items   []itemReply     `json:"items"`
	Changed []itemReply     `json:"changed"`
	Removed []string        `json:"removed"`
	Before  json.RawMessage `json:"before"`
	After   json.RawMessage `json:"after"`
	Reset   bool            `json:"reset"`
}

func (p pageReply) atTop() bool { return len(p.Before) == 0 || string(p.Before) == "null" }

func ids(items []itemReply) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

// world is a rig holding the four sessions and their files.
type world struct {
	*rig
	vendorFile string // ~/.claude/projects/<slug>/<id>.jsonl, full fixture plus two background pairs
	pathFile   string // the first fixtureHead lines, named only by AgentSessionPath
	outside    string // output path of task evalout1, outside every session dir
	pending    string // output path of task evalout2, inside the vendor session dir, not yet created
}

func newWorld(t *testing.T) *world {
	t.Helper()
	r := newRig(t)
	lines := fixtureLines(t)
	projects := filepath.Join(r.home, ".claude", "projects")
	w := &world{
		rig:        r,
		vendorFile: filepath.Join(projects, fixtureSlug, fixtureID+".jsonl"),
		pathFile:   filepath.Join(projects, "-eval-path-route", fixtureID+".jsonl"),
		outside:    filepath.Join(projects, "-eval-other-project", "evalout1.output"),
		pending:    filepath.Join(projects, fixtureSlug, fixtureID, "tasks", "evalout2.output"),
	}
	writeFile(t, w.outside, "eval outside\n")
	pair1, last := backgroundPair(t, lines, lastUUID(t, lines), "evalout1", w.outside)
	pair2, _ := backgroundPair(t, lines, last, "evalout2", w.pending)
	writeFile(t, w.vendorFile, strings.Join(slices.Concat(lines, pair1, pair2), ""))
	writeFile(t, w.pathFile, strings.Join(lines[:fixtureHead], ""))

	r.addSession(domain.Session{ID: sessVendor, AgentName: "claude", AgentSessionID: fixtureID, WorkingDirectory: fixtureCwd})
	r.addSession(domain.Session{ID: sessPath, AgentName: "claude", AgentSessionPath: w.pathFile, WorkingDirectory: "/eval/path/route"})
	r.addSession(domain.Session{ID: sessPane, AgentName: "bash", WorkingDirectory: "/eval/pane"})
	r.addSession(domain.Session{ID: sessOther, AgentName: "claude", WorkingDirectory: "/eval/other"})
	r.pty.SetDefaultScreen("eval pane screen\n$ ")
	return w
}

func (w *world) page(params map[string]any) pageReply {
	w.t.Helper()
	var p pageReply
	decode(w.t, w.call("", "session.transcript", params), &p)
	if p.Type != "transcript" {
		w.t.Fatalf("type = %q, want transcript", p.Type)
	}
	return p
}

// allItems pages up from the newest with limit until before is null and
// returns every id oldest first.
func (w *world) allItems(session string, limit int) []string {
	w.t.Helper()
	p := w.page(map[string]any{"session": session, "limit": limit})
	got := ids(p.Items)
	for pages := 0; !p.atTop(); pages++ {
		if pages > 500 {
			w.t.Fatal("before never reached the top")
		}
		p = w.page(map[string]any{"session": session, "limit": limit, "before": p.Before})
		if len(p.Items) == 0 && !p.atTop() {
			w.t.Fatal("an empty before page is not at the top")
		}
		got = append(ids(p.Items), got...)
	}
	return got
}

// TestTranscriptPaging: newest-last pages from the real file the vendor rule
// finds, before paging to the top, and the limit default and maximum.
func TestTranscriptPaging(t *testing.T) {
	w := newWorld(t)
	first := w.page(map[string]any{"session": sessVendor, "limit": 3})
	if first.Source != "transcript" || first.Capabilities.Scrollback != "transcript" || first.Capabilities.Agent == "" {
		t.Fatalf("source %q scrollback %q agent %q, want the transcript", first.Source, first.Capabilities.Scrollback, first.Capabilities.Agent)
	}
	if len(first.Items) != 3 || first.atTop() || len(first.After) == 0 {
		t.Fatalf("limit 3 gave %d items, before %s, after %s", len(first.Items), first.Before, first.After)
	}

	all := w.allItems(sessVendor, 3)
	if !slices.Equal(all[len(all)-3:], ids(first.Items)) {
		t.Errorf("first page %v is not the newest three of %v", ids(first.Items), all)
	}
	seen := map[string]bool{}
	for _, id := range all {
		if seen[id] {
			t.Errorf("item %s appears on two pages", id)
		}
		seen[id] = true
	}

	def := w.page(map[string]any{"session": sessVendor})
	if want := min(len(all), 50); len(def.Items) != want || !slices.Equal(ids(def.Items), all[len(all)-want:]) {
		t.Errorf("default limit gave %d items, want the newest %d", len(def.Items), want)
	}
	big := w.page(map[string]any{"session": sessVendor, "limit": 1000})
	if want := min(len(all), 200); len(big.Items) != want {
		t.Errorf("limit 1000 gave %d items, want %d", len(big.Items), want)
	}
	if len(all) <= 200 && !big.atTop() {
		t.Errorf("a page holding all %d items has before %s, want null", len(all), big.Before)
	}
}

// TestTranscriptAfter: on the AgentSessionPath route, a result for a loaded
// call lands in changed[], and appended lines land in items[].
func TestTranscriptAfter(t *testing.T) {
	w := newWorld(t)
	lines := fixtureLines(t)
	p := w.page(map[string]any{"session": sessPath})
	if p.Source != "transcript" || !hasRunningCall(p.Items, fixtureCallID) {
		t.Fatalf("source %q, items %v: want the transcript with %s running", p.Source, ids(p.Items), fixtureCallID)
	}

	appendFile(t, w.pathFile, lines[fixtureHead])
	res := w.page(map[string]any{"session": sessPath, "after": p.After})
	if res.Reset {
		t.Fatal("an append answered reset")
	}
	if !hasCall(res.Changed, fixtureCallID, func(s string) bool { return s != "running" }) {
		t.Errorf("changed %v does not finish %s", ids(res.Changed), fixtureCallID)
	}
	if hasCall(res.Items, fixtureCallID, func(string) bool { return true }) {
		t.Errorf("the result's call was appended to items %v; it was already loaded", ids(res.Items))
	}

	appendFile(t, w.pathFile, lines[fixtureHead+1:fixtureNextCallLine]...)
	more := w.page(map[string]any{"session": sessPath, "after": res.After})
	if !hasRunningCall(more.Items, fixtureNextCall) {
		t.Fatalf("after lines up to the next call were appended, items %v lack %s running (changed %v)", ids(more.Items), fixtureNextCall, ids(more.Changed))
	}
	empty := w.page(map[string]any{"session": sessPath, "after": more.After})
	if len(empty.Items)+len(empty.Changed)+len(empty.Removed) != 0 {
		t.Errorf("after at the end returned %v %v %v", ids(empty.Items), ids(empty.Changed), empty.Removed)
	}
}

func hasCall(items []itemReply, toolID string, status func(string) bool) bool {
	return slices.ContainsFunc(items, func(it itemReply) bool {
		return it.Kind == "tool_call" && it.ToolCall != nil && it.ToolCall.ToolID == toolID && status(it.ToolCall.Status)
	})
}

func hasRunningCall(items []itemReply, toolID string) bool {
	return hasCall(items, toolID, func(s string) bool { return s == "running" })
}

// TestTranscriptItem: one item by id, not_found for an unknown id.
func TestTranscriptItem(t *testing.T) {
	w := newWorld(t)
	p := w.page(map[string]any{"session": sessVendor, "limit": 5})
	if len(p.Items) == 0 {
		t.Fatal("no items to look up")
	}
	want := p.Items[len(p.Items)-1]
	var got struct {
		Type string    `json:"type"`
		Item itemReply `json:"item"`
	}
	decode(t, w.call("", "session.transcript_item", map[string]any{"session": sessVendor, "id": want.ID}), &got)
	if got.Type != "transcript_item" || got.Item.ID != want.ID || got.Item.Kind != want.Kind {
		t.Errorf("transcript_item = %s %s %s, want transcript_item %s %s", got.Type, got.Item.ID, got.Item.Kind, want.ID, want.Kind)
	}
	wantCode(t, w.call("", "session.transcript_item", map[string]any{"session": sessVendor, "id": "eval-no-such-item"}), server.CodeNotFound)
}

type processReply struct {
	Type  string `json:"type"`
	Data  string `json:"data"`
	State string `json:"state"`
}

// TestProcessOutput: a named output outside the session directory is
// forbidden; one inside it that does not exist yet is empty and running,
// and served once it is written.
func TestProcessOutput(t *testing.T) {
	w := newWorld(t)
	w.page(map[string]any{"session": sessVendor})
	wantCode(t, w.call("", "session.process_output", map[string]any{"session": sessVendor, "id": "evalout1"}), server.CodeForbidden)

	var got processReply
	decode(t, w.call("", "session.process_output", map[string]any{"session": sessVendor, "id": "evalout2"}), &got)
	if got.Type != "process_output" || got.Data != "" || got.State != "running" {
		t.Fatalf("not-yet-created output = %+v, want process_output, no data, running", got)
	}

	writeFile(t, w.pending, "eval output line\n")
	decode(t, w.call("", "session.process_output", map[string]any{"session": sessVendor, "id": "evalout2"}), &got)
	if got.Data != "eval output line\n" {
		t.Errorf("written output data = %q, want the file's content", got.Data)
	}
}

// TestTranscriptPaneFallback: an agent with no transcript answers one pane
// item holding the capture.
func TestTranscriptPaneFallback(t *testing.T) {
	w := newWorld(t)
	p := w.page(map[string]any{"session": sessPane})
	if p.Source != "pane" || p.Capabilities.Scrollback != "pane" {
		t.Errorf("source %q scrollback %q, want pane", p.Source, p.Capabilities.Scrollback)
	}
	if len(p.Items) != 1 || p.Items[0].Kind != "pane" || !strings.Contains(p.Items[0].Text, "eval pane screen") {
		t.Errorf("items = %+v, want one pane item with the capture", p.Items)
	}
}

// TestTranscriptScope: a session caller reads its own transcript and gets
// session_scope for another session's, on all three methods.
func TestTranscriptScope(t *testing.T) {
	w := newWorld(t)
	var own pageReply
	decode(t, w.call(sessVendor, "session.transcript", map[string]any{"session": sessVendor}), &own)
	if own.Source != "transcript" {
		t.Errorf("own transcript source = %q, want transcript", own.Source)
	}
	for method, params := range map[string]map[string]any{
		"session.transcript":      {"session": sessVendor},
		"session.transcript_item": {"session": sessVendor, "id": "x"},
		"session.process_output":  {"session": sessVendor, "id": "evalout2"},
	} {
		t.Run(method, func(t *testing.T) {
			wantCode(t, w.call(sessOther, method, params), server.CodeSessionScope)
		})
	}
}
