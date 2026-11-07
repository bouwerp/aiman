//go:build acceptance

package server

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/ptyruntime/ptyfake"
	"github.com/bouwerp/aiman/internal/transcript"
)

// fakeReader is one scripted transcript. It records what the handler asked.
type fakeReader struct {
	mu    sync.Mutex
	page  transcript.Page
	after transcript.Page
	items map[string]transcript.Item
	paths map[string]string
	dirs  []string
	files []string

	pageCalls  int
	lastBefore *transcript.Cursor
	lastAfter  *transcript.Cursor
	lastLimit  int
}

func (r *fakeReader) Page(before *transcript.Cursor, limit int) (transcript.Page, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pageCalls++
	r.lastBefore, r.lastLimit = before, limit
	return r.page, nil
}

func (r *fakeReader) After(c transcript.Cursor, limit int) (transcript.Page, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastAfter, r.lastLimit = &c, limit
	return r.after, nil
}

func (r *fakeReader) Item(id string) (transcript.Item, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := r.items[id]
	return it, ok
}

func (r *fakeReader) ProcessPath(id string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.paths[id]
	return p, ok
}

func (r *fakeReader) SessionDirs() []string { return r.dirs }

func (r *fakeReader) Files() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.files...)
}

// fakeTranscripts hands out readers by session id and sub-agent id. A session
// with no reader has no transcript.
type fakeTranscripts struct {
	mu      sync.Mutex
	readers map[string]*fakeReader
	opens   []string
}

func trKey(session, subagent string) string { return session + "\x00" + subagent }

func (f *fakeTranscripts) add(session, subagent string, r *fakeReader) *fakeReader {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.readers == nil {
		f.readers = map[string]*fakeReader{}
	}
	f.readers[trKey(session, subagent)] = r
	return r
}

func (f *fakeTranscripts) Open(sess domain.Session, subagent string) (TranscriptReader, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens = append(f.opens, trKey(sess.ID, subagent))
	r, ok := f.readers[trKey(sess.ID, subagent)]
	if !ok {
		return nil, transcript.ErrNoTranscript
	}
	return r, nil
}

func (f *fakeTranscripts) opened(session, subagent string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range f.opens {
		if k == trKey(session, subagent) {
			return true
		}
	}
	return false
}

const trSession = "tr-sess"

// transcriptServer is serve over a fake runtime with one session row and the
// given transcript source (none when src is nil).
func transcriptServer(t *testing.T, src TranscriptSource) (*Server, *ptyfake.FakePTY) {
	t.Helper()
	srv, pty, _ := ptyServer(t, domain.Session{
		ID: trSession, Name: trSession, Group: "g", TerminalName: trSession,
		Backend: domain.BackendPTY, AgentName: "claude", WorkingDirectory: "/w/" + trSession,
	})
	pty.Add(ptyruntime.SessionInfo{ID: trSession, Name: trSession, Dir: "/w/" + trSession})
	if src != nil {
		srv.SetTranscripts(src)
	}
	return srv, pty
}

func trCall(t *testing.T, srv *Server, method string, params map[string]any) Response {
	t.Helper()
	return scopeCall(t, srv, "", scopeNoPID, method, params)
}

// decodeResult round-trips a result through JSON into v and returns the bytes.
func decodeTrResult(t *testing.T, resp Response, v any) []byte {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("error: %+v", resp.Error)
	}
	b, err := json.Marshal(resp.Result)
	if err != nil {
		t.Fatalf("encode result: %v", err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("decode result %s: %v", b, err)
	}
	return b
}

type trItemReply struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
	ToolCall  *struct {
		Input  string `json:"input"`
		Result string `json:"result"`
	} `json:"tool_call"`
}

type trPageReply struct {
	Type         string `json:"type"`
	Session      string `json:"session"`
	Subagent     string `json:"subagent"`
	Source       string `json:"source"`
	Capabilities struct {
		Scrollback string `json:"scrollback"`
	} `json:"capabilities"`
	Items   []trItemReply   `json:"items"`
	Changed []trItemReply   `json:"changed"`
	Removed []string        `json:"removed"`
	Before  json.RawMessage `json:"before"`
	After   json.RawMessage `json:"after"`
	Reset   bool            `json:"reset"`
}

func ids(items []trItemReply) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

var (
	trCursorTop   = transcript.Cursor{FileID: "f1", Kind: transcript.PositionOffset, Position: 100}
	trCursorAfter = transcript.Cursor{FileID: "f1", Kind: transcript.PositionOffset, Position: 900}
)

func trItem(id string, kind transcript.ItemKind, text string) transcript.Item {
	return transcript.Item{ID: id, Kind: kind, Rev: 1, Text: text}
}

// TestSessionTranscriptPages covers session.transcript: the first page,
// before and after passed through with their lists, limit clamps, both
// cursors refused, the pane fallback and the 2 MiB reply cap.
func TestSessionTranscriptPages(t *testing.T) {
	newSource := func() (*fakeTranscripts, *fakeReader) {
		src := &fakeTranscripts{}
		r := src.add(trSession, "", &fakeReader{
			page: transcript.Page{
				Type: transcript.PageType, Source: transcript.SourceTranscript,
				Capabilities: transcript.Capabilities{Agent: "claude", Scrollback: transcript.ScrollbackTranscript},
				Items: []transcript.Item{
					trItem("u1", transcript.KindUser, "hello"),
					trItem("a1", transcript.KindAssistant, "hi"),
				},
				Before: &trCursorTop,
				After:  trCursorAfter,
			},
			after: transcript.Page{
				Type: transcript.PageType, Source: transcript.SourceTranscript,
				Items:   []transcript.Item{trItem("s9", transcript.KindSystem, "done")},
				Changed: []transcript.Item{trItem("t1", transcript.KindToolCall, "")},
				Removed: []string{"a0"},
				After:   trCursorAfter,
				Reset:   true,
			},
		})
		return src, r
	}

	t.Run("first page", func(t *testing.T) {
		src, r := newSource()
		srv, _ := transcriptServer(t, src)
		var got trPageReply
		decodeTrResult(t, trCall(t, srv, "session.transcript", map[string]any{"session": trSession}), &got)
		if got.Type != "transcript" || got.Session != trSession || got.Source != transcript.SourceTranscript {
			t.Fatalf("page header: %+v", got)
		}
		if strings.Join(ids(got.Items), ",") != "u1,a1" {
			t.Fatalf("items %v, want u1,a1 newest last", ids(got.Items))
		}
		if r.lastBefore != nil || r.lastLimit != transcript.DefaultLimit {
			t.Fatalf("reader got before=%v limit=%d, want nil and %d", r.lastBefore, r.lastLimit, transcript.DefaultLimit)
		}
		if string(got.Before) != `"`+transcript.EncodeCursor(trCursorTop)+`"` {
			t.Fatalf("before = %s, want the encoded cursor", got.Before)
		}
		if string(got.After) != `"`+transcript.EncodeCursor(trCursorAfter)+`"` {
			t.Fatalf("after = %s, want the encoded cursor", got.After)
		}
	})

	t.Run("before", func(t *testing.T) {
		src, r := newSource()
		srv, _ := transcriptServer(t, src)
		var got trPageReply
		decodeTrResult(t, trCall(t, srv, "session.transcript", map[string]any{
			"session": trSession, "before": transcript.EncodeCursor(trCursorTop), "limit": 10,
		}), &got)
		if r.lastBefore == nil || *r.lastBefore != trCursorTop || r.lastLimit != 10 {
			t.Fatalf("reader got before=%v limit=%d, want %v and 10", r.lastBefore, r.lastLimit, trCursorTop)
		}
	})

	t.Run("after passes items, changed, removed and reset", func(t *testing.T) {
		src, r := newSource()
		srv, _ := transcriptServer(t, src)
		var got trPageReply
		decodeTrResult(t, trCall(t, srv, "session.transcript", map[string]any{
			"session": trSession, "after": transcript.EncodeCursor(trCursorAfter),
		}), &got)
		if r.lastAfter == nil || *r.lastAfter != trCursorAfter {
			t.Fatalf("reader got after=%v, want %v", r.lastAfter, trCursorAfter)
		}
		if strings.Join(ids(got.Items), ",") != "s9" || strings.Join(ids(got.Changed), ",") != "t1" {
			t.Fatalf("items %v changed %v, want s9 and t1", ids(got.Items), ids(got.Changed))
		}
		if strings.Join(got.Removed, ",") != "a0" || !got.Reset {
			t.Fatalf("removed %v reset %v, want a0 and true", got.Removed, got.Reset)
		}
	})

	t.Run("limit clamps", func(t *testing.T) {
		src, r := newSource()
		srv, _ := transcriptServer(t, src)
		var got trPageReply
		decodeTrResult(t, trCall(t, srv, "session.transcript", map[string]any{"session": trSession, "limit": 5000}), &got)
		if r.lastLimit != transcript.MaxLimit {
			t.Fatalf("limit 5000 reached the reader as %d, want %d", r.lastLimit, transcript.MaxLimit)
		}
	})

	t.Run("before and after together", func(t *testing.T) {
		src, r := newSource()
		srv, _ := transcriptServer(t, src)
		resp := trCall(t, srv, "session.transcript", map[string]any{
			"session": trSession,
			"before":  transcript.EncodeCursor(trCursorTop),
			"after":   transcript.EncodeCursor(trCursorAfter),
		})
		wantCode(t, resp, CodeInvalidParams)
		if r.pageCalls != 0 || r.lastAfter != nil {
			t.Fatal("a refused call reached the reader")
		}
	})

	t.Run("sub-agent", func(t *testing.T) {
		src, _ := newSource()
		src.add(trSession, "a3f0", &fakeReader{page: transcript.Page{
			Type: transcript.PageType, Source: transcript.SourceTranscript,
			Items: []transcript.Item{trItem("x1", transcript.KindAssistant, "child")},
		}})
		srv, _ := transcriptServer(t, src)
		var got trPageReply
		decodeTrResult(t, trCall(t, srv, "session.transcript", map[string]any{"session": trSession, "subagent": "a3f0"}), &got)
		if got.Subagent != "a3f0" || strings.Join(ids(got.Items), ",") != "x1" {
			t.Fatalf("sub-agent page: subagent %q items %v", got.Subagent, ids(got.Items))
		}
	})

	t.Run("pane fallback", func(t *testing.T) {
		srv, pty := transcriptServer(t, &fakeTranscripts{})
		pty.SetScreen(trSession, "❯ the pane says hello")
		var got trPageReply
		decodeTrResult(t, trCall(t, srv, "session.transcript", map[string]any{"session": trSession}), &got)
		if got.Type != "transcript" || got.Source != transcript.SourcePane || got.Capabilities.Scrollback != transcript.ScrollbackPane {
			t.Fatalf("pane page header: %+v", got)
		}
		if len(got.Items) != 1 || got.Items[0].Kind != string(transcript.KindPane) ||
			!strings.Contains(got.Items[0].Text, "the pane says hello") {
			t.Fatalf("pane items: %+v", got.Items)
		}
	})

	t.Run("reply capped at 2 MiB", func(t *testing.T) {
		src := &fakeTranscripts{}
		big := strings.Repeat("x", 600<<10)
		var items []transcript.Item
		for _, id := range []string{"i1", "i2", "i3", "i4", "i5"} {
			items = append(items, trItem(id, transcript.KindAssistant, big))
		}
		src.add(trSession, "", &fakeReader{page: transcript.Page{
			Type: transcript.PageType, Source: transcript.SourceTranscript,
			Items: items, After: trCursorAfter,
		}})
		srv, _ := transcriptServer(t, src)
		var got trPageReply
		raw := decodeTrResult(t, trCall(t, srv, "session.transcript", map[string]any{"session": trSession}), &got)
		if len(raw) > TranscriptMaxReplyBytes {
			t.Fatalf("reply is %d bytes, cap %d", len(raw), TranscriptMaxReplyBytes)
		}
		if len(got.Items) == 0 || len(got.Items) == 5 {
			t.Fatalf("got %d items, want a shortened, non-empty page", len(got.Items))
		}
		if got.Items[len(got.Items)-1].ID != "i5" {
			t.Fatalf("items %v: the newest item must stay", ids(got.Items))
		}
		if len(got.Before) == 0 || string(got.Before) == "null" {
			t.Fatal("a shortened page must carry a before cursor for the items it dropped")
		}
	})

	t.Run("no source is unavailable", func(t *testing.T) {
		srv, _ := transcriptServer(t, nil)
		wantCode(t, trCall(t, srv, "session.transcript", map[string]any{"session": trSession}), CodeUnavailable)
	})

	t.Run("unknown session", func(t *testing.T) {
		src, _ := newSource()
		srv, _ := transcriptServer(t, src)
		wantCode(t, trCall(t, srv, "session.transcript", map[string]any{"session": "nope"}), CodeNotFound)
	})
}

// TestSessionTranscriptItem covers session.transcript_item: one item with a
// 1 MiB cap per field, the sub-agent, not_found and unavailable. The item id
// rides in "id" while "session" names the session.
func TestSessionTranscriptItem(t *testing.T) {
	huge := strings.Repeat("y", 1536<<10)
	newSource := func() *fakeTranscripts {
		src := &fakeTranscripts{}
		src.add(trSession, "", &fakeReader{items: map[string]transcript.Item{
			"t1": {
				ID: "t1", Kind: transcript.KindToolCall, Rev: 4, Text: huge,
				ToolCall: &transcript.ToolCall{ToolID: "t1", Name: "Bash", Input: huge, Result: "ok", Status: transcript.ToolOK},
			},
		}})
		src.add(trSession, "a3f0", &fakeReader{items: map[string]transcript.Item{
			"c1": trItem("c1", transcript.KindAssistant, "from the child"),
		}})
		return src
	}
	type reply struct {
		Type string      `json:"type"`
		Item trItemReply `json:"item"`
	}

	t.Run("capped at 1 MiB per field", func(t *testing.T) {
		srv, _ := transcriptServer(t, newSource())
		var got reply
		decodeTrResult(t, trCall(t, srv, "session.transcript_item", map[string]any{"session": trSession, "id": "t1"}), &got)
		if got.Type != "transcript_item" || got.Item.ID != "t1" {
			t.Fatalf("reply: type %q item %q", got.Type, got.Item.ID)
		}
		if len(got.Item.Text) > TranscriptItemMaxFieldBytes || got.Item.Text == "" || !got.Item.Truncated {
			t.Fatalf("text %d bytes truncated %v, want at most %d and truncated", len(got.Item.Text), got.Item.Truncated, TranscriptItemMaxFieldBytes)
		}
		if got.Item.ToolCall == nil || len(got.Item.ToolCall.Input) > TranscriptItemMaxFieldBytes || got.Item.ToolCall.Result != "ok" {
			t.Fatalf("tool call: %+v", got.Item.ToolCall)
		}
	})

	t.Run("sub-agent", func(t *testing.T) {
		srv, _ := transcriptServer(t, newSource())
		var got reply
		decodeTrResult(t, trCall(t, srv, "session.transcript_item", map[string]any{"session": trSession, "subagent": "a3f0", "id": "c1"}), &got)
		if got.Item.Text != "from the child" {
			t.Fatalf("sub-agent item: %+v", got.Item)
		}
	})

	t.Run("not found", func(t *testing.T) {
		srv, _ := transcriptServer(t, newSource())
		wantCode(t, trCall(t, srv, "session.transcript_item", map[string]any{"session": trSession, "id": "missing"}), CodeNotFound)
	})

	t.Run("no source is unavailable", func(t *testing.T) {
		srv, _ := transcriptServer(t, nil)
		wantCode(t, trCall(t, srv, "session.transcript_item", map[string]any{"session": trSession, "id": "t1"}), CodeUnavailable)
	})
}

type processReply struct {
	Type      string `json:"type"`
	Data      string `json:"data"`
	Offset    int64  `json:"offset"`
	Size      int64  `json:"size"`
	State     string `json:"state"`
	ExitCode  *int   `json:"exit_code"`
	Truncated bool   `json:"truncated"`
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// TestProcessOutput covers session.process_output: the tail, since, the
// confinement refusals, a not-yet-created output and Codex inline output.
func TestProcessOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	projDir := filepath.Join(home, ".claude", "projects", "-w-tr-sess", "uuid-1")
	taskDir := filepath.Join(home, "tmp", "claude-1004", "-w-tr-sess", "uuid-1")

	const size = 300 << 10
	content := make([]byte, size)
	for i := range content {
		content[i] = byte('a' + i%26)
	}
	writeFile(t, filepath.Join(taskDir, "tasks", "b7k2.output"), content)
	writeFile(t, filepath.Join(projDir, "subagents", "agent-a3f0.jsonl"), []byte("sub\n"))
	symlink(t, filepath.Join(projDir, "subagents", "agent-a3f0.jsonl"), filepath.Join(taskDir, "tasks", "agent.output"))
	writeFile(t, filepath.Join(home, ".grok", "auth.json"), []byte(`{"token":"redacted"}`))
	writeFile(t, filepath.Join(home, "outside.txt"), []byte("outside\n"))
	symlink(t, filepath.Join(home, "outside.txt"), filepath.Join(taskDir, "tasks", "out.output"))
	writeFile(t, filepath.Join(home, ".claude", "projects", "-other", "abc.jsonl"), []byte("{}\n"))

	exit1, exit0 := 1, 0
	src := &fakeTranscripts{}
	src.add(trSession, "", &fakeReader{
		dirs: []string{projDir, taskDir},
		paths: map[string]string{
			"b7k2":     filepath.Join(taskDir, "tasks", "b7k2.output"),
			"agent":    filepath.Join(taskDir, "tasks", "agent.output"),
			"new":      filepath.Join(taskDir, "tasks", "new.output"),
			"auth":     filepath.Join(home, ".grok", "auth.json"),
			"linkout":  filepath.Join(taskDir, "tasks", "out.output"),
			"other":    filepath.Join(home, ".claude", "projects", "-other", "abc.jsonl"),
			"dotdot":   filepath.Join(taskDir, "tasks") + "/../../../../../.grok/auth.json",
			"newoutly": filepath.Join(home, "elsewhere", "new.output"),
		},
		items: map[string]transcript.Item{
			"b7k2": {ID: "b7k2", Kind: transcript.KindProcess, Process: &transcript.Process{
				ID: "b7k2", Command: "go test ./...", State: transcript.ProcessExited, ExitCode: &exit1, Output: transcript.OutputFile,
			}},
			"cx1": {ID: "cx1", Kind: transcript.KindProcess, Text: "ok\n", Process: &transcript.Process{
				ID: "cx1", Command: "ls", State: transcript.ProcessExited, ExitCode: &exit0, Output: transcript.OutputInline,
			}},
		},
	})
	srv, _ := transcriptServer(t, src)
	call := func(params map[string]any) Response {
		params["session"] = trSession
		return trCall(t, srv, "session.process_output", params)
	}

	t.Run("tail", func(t *testing.T) {
		var got processReply
		decodeTrResult(t, call(map[string]any{"id": "b7k2"}), &got)
		start := size - ProcessOutputMaxBytes
		if got.Type != "process_output" || got.Size != size || got.Offset != int64(start) {
			t.Fatalf("type %q size %d offset %d, want process_output, %d, %d", got.Type, got.Size, got.Offset, size, start)
		}
		if got.Data != string(content[start:]) || !got.Truncated {
			t.Fatalf("tail: %d bytes truncated %v, want the last %d and truncated", len(got.Data), got.Truncated, ProcessOutputMaxBytes)
		}
		if got.State != transcript.ProcessExited || got.ExitCode == nil || *got.ExitCode != 1 {
			t.Fatalf("state %q exit %v, want exited 1", got.State, got.ExitCode)
		}
	})

	t.Run("since", func(t *testing.T) {
		var got processReply
		decodeTrResult(t, call(map[string]any{"id": "b7k2", "since": 1000}), &got)
		if got.Offset != 1000 || got.Data != string(content[1000:1000+ProcessOutputMaxBytes]) || !got.Truncated {
			t.Fatalf("since 1000: offset %d, %d bytes, truncated %v", got.Offset, len(got.Data), got.Truncated)
		}
		decodeTrResult(t, call(map[string]any{"id": "b7k2", "since": 290 << 10}), &got)
		if got.Data != string(content[290<<10:]) || got.Truncated {
			t.Fatalf("since near the end: %d bytes truncated %v", len(got.Data), got.Truncated)
		}
	})

	t.Run("symlink inside the session directory is served", func(t *testing.T) {
		var got processReply
		decodeTrResult(t, call(map[string]any{"id": "agent"}), &got)
		if got.Data != "sub\n" {
			t.Fatalf("data %q, want the sub-agent file", got.Data)
		}
	})

	for _, id := range []string{"auth", "linkout", "other", "dotdot", "newoutly"} {
		t.Run("forbidden "+id, func(t *testing.T) {
			resp := call(map[string]any{"id": id})
			wantCode(t, resp, CodeForbidden)
			if b, _ := json.Marshal(resp); bytes.Contains(b, []byte("redacted")) || bytes.Contains(b, []byte("outside")) {
				t.Fatalf("refusal leaked file content: %s", b)
			}
		})
	}

	t.Run("not yet created", func(t *testing.T) {
		var got processReply
		decodeTrResult(t, call(map[string]any{"id": "new"}), &got)
		if got.Data != "" || got.State != transcript.ProcessRunning {
			t.Fatalf("data %q state %q, want empty and running", got.Data, got.State)
		}
	})

	t.Run("codex inline", func(t *testing.T) {
		var got processReply
		decodeTrResult(t, call(map[string]any{"id": "cx1"}), &got)
		if got.Data != "ok\n" || got.State != transcript.ProcessExited || got.ExitCode == nil || *got.ExitCode != 0 {
			t.Fatalf("inline: %+v", got)
		}
	})

	t.Run("unknown process", func(t *testing.T) {
		wantCode(t, call(map[string]any{"id": "nope"}), CodeNotFound)
	})

	t.Run("no source is unavailable", func(t *testing.T) {
		bare, _ := transcriptServer(t, nil)
		wantCode(t, trCall(t, bare, "session.process_output", map[string]any{"session": trSession, "id": "b7k2"}), CodeUnavailable)
	})
}

// TestTranscriptScope: a session caller reads only its own tree; operator and
// device calls (no caller) pass.
func TestTranscriptScope(t *testing.T) {
	methods := []struct {
		method string
		params func(target string) map[string]any
	}{
		{"session.transcript", func(id string) map[string]any { return map[string]any{"session": id} }},
		{"session.transcript_item", func(id string) map[string]any { return map[string]any{"session": id, "id": "i1"} }},
		{"session.process_output", func(id string) map[string]any { return map[string]any{"session": id, "id": "p1"} }},
	}
	for _, m := range methods {
		for _, c := range scopeTargetCases(false) {
			t.Run(m.method+"/"+c.name, func(t *testing.T) {
				srv, pty := scopeServer(t)
				src := &fakeTranscripts{}
				for _, id := range []string{scopeA, scopeB, scopeC, scopeD} {
					src.add(id, "", &fakeReader{
						page:  transcript.Page{Type: transcript.PageType, Source: transcript.SourceTranscript},
						items: map[string]transcript.Item{"i1": trItem("i1", transcript.KindUser, "x")},
					})
				}
				srv.SetTranscripts(src)
				resp := scopeCall(t, srv, c.caller, c.pid, m.method, m.params(c.target))
				if c.allowed && resp.Error != nil &&
					(resp.Error.Code == CodeUnavailable || resp.Error.Code == CodeInvalidParams) {
					t.Fatalf("allowed call failed: %+v", resp.Error)
				}
				checkScope(t, srv, pty, resp, c.allowed)
				if !c.allowed && src.opened(c.target, "") {
					t.Fatal("a refused call opened the target's transcript")
				}
			})
		}
	}
}
