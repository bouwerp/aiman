package server

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/transcript"
)

// TestTranscriptSourceSessionMatch: a stored AgentSessionPath inside the
// vendor root is used only when it names this session.
func TestTranscriptSourceSessionMatch(t *testing.T) {
	home := t.TempDir()
	claude := filepath.Join(home, ".claude", "projects", "-w-a", "s1.jsonl")
	writeTestFile(t, claude, claudeLine)
	codex := filepath.Join(home, ".codex", "sessions", "2026", "11", "07", "rollout-2026-11-07T10-00-00-t1.jsonl")
	writeTestFile(t, codex, codexMeta("t1", ""))
	grok := filepath.Join(home, ".grok", "sessions", "%2Fw", "g1", "updates.jsonl")
	writeTestFile(t, grok, "")

	cases := []struct {
		name string
		sess domain.Session
		ok   bool
	}{
		{"claude id and cwd", domain.Session{AgentName: "claude", AgentSessionID: "s1", WorkingDirectory: "/w/a", AgentSessionPath: claude}, true},
		{"claude cwd only", domain.Session{AgentName: "claude", WorkingDirectory: "/w/a", AgentSessionPath: claude}, true},
		{"claude other cwd", domain.Session{AgentName: "claude", AgentSessionID: "s1", WorkingDirectory: "/w/b", AgentSessionPath: claude}, false},
		{"claude other id", domain.Session{AgentName: "claude", AgentSessionID: "s2", WorkingDirectory: "/w/a", AgentSessionPath: claude}, false},
		{"claude neither", domain.Session{AgentName: "claude", AgentSessionPath: claude}, false},
		{"codex id", domain.Session{AgentName: "codex", AgentSessionID: "t1", AgentSessionPath: codex}, true},
		{"codex other id", domain.Session{AgentName: "codex", AgentSessionID: "t2", AgentSessionPath: codex}, false},
		{"codex no id", domain.Session{AgentName: "codex", AgentSessionPath: codex}, false},
		{"grok folder", domain.Session{AgentName: "grok", AgentSessionID: "g1", WorkingDirectory: "/w", AgentSessionPath: grok}, true},
		{"grok other folder", domain.Session{AgentName: "grok", AgentSessionID: "g2", WorkingDirectory: "/w", AgentSessionPath: grok}, false},
	}
	for i, c := range cases {
		c.sess.ID = string(rune('a' + i))
		_, err := NewTranscriptSource(home).Open(c.sess, "")
		if c.ok && err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if !c.ok && !errors.Is(err, transcript.ErrNoTranscript) {
			t.Errorf("%s: err = %v, want ErrNoTranscript", c.name, err)
		}
		_, watched := NewTranscriptSource(home).(*fileTranscripts).WatchPath(c.sess)
		if watched != c.ok {
			t.Errorf("%s: WatchPath = %v, want %v", c.name, watched, c.ok)
		}
	}
}

// TestTranscriptSourceCachesLocate: the located path is reused until the file
// is gone, and a failed sub-agent lookup is remembered.
func TestTranscriptSourceCachesLocate(t *testing.T) {
	home := t.TempDir()
	parent := filepath.Join(home, ".claude", "projects", "-w", "s1.jsonl")
	writeTestFile(t, parent, claudeLine)
	src := NewTranscriptSource(home).(*fileTranscripts)
	now := time.Unix(1000, 0)
	src.now = func() time.Time { return now }
	sess := domain.Session{ID: "a", AgentName: "claude", AgentSessionID: "s1", WorkingDirectory: "/w"}
	r1, err := src.Open(sess, "")
	if err != nil {
		t.Fatal(err)
	}
	if r2, _ := src.Open(sess, ""); r2 != r1 {
		t.Fatal("second open built a new reader")
	}
	if _, err := src.Open(sess, "x1"); !errors.Is(err, transcript.ErrNoTranscript) {
		t.Fatalf("missing sub-agent: %v", err)
	}
	writeTestFile(t, filepath.Join(home, ".claude", "projects", "-w", "s1", "subagents", "agent-x1.jsonl"), claudeLine)
	if _, err := src.Open(sess, "x1"); !errors.Is(err, transcript.ErrNoTranscript) {
		t.Fatalf("within the miss window: %v, want the cached miss", err)
	}
	now = now.Add(subagentMissTTL)
	if _, err := src.Open(sess, "x1"); err != nil {
		t.Fatalf("after the miss window: %v", err)
	}
	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if _, err := src.Open(sess, ""); !errors.Is(err, transcript.ErrNoTranscript) {
		t.Fatalf("deleted parent: %v, want located again and not found", err)
	}
}

func codexMeta(id, parent string) string {
	return `{"timestamp":"2026-11-07T10:00:00.000Z","type":"session_meta","payload":{"id":"` + id +
		`","parent_thread_id":"` + parent + `","source":{"subagent":"review"}}}` + "\n"
}

const (
	codexParentID = "11111111-1111-1111-1111-111111111111"
	codexChildID  = "22222222-2222-2222-2222-222222222222"
	codexOtherID  = "33333333-3333-3333-3333-333333333333"
)

// TestTranscriptSourceCodexChildren: a Codex parent's newest page lists the
// rollouts whose head names its thread as subagent items, and the scan is
// reused for codexChildRescan.
func TestTranscriptSourceCodexChildren(t *testing.T) {
	home := t.TempDir()
	day := filepath.Join(home, ".codex", "sessions", "2026", "11", "07")
	writeTestFile(t, filepath.Join(day, "rollout-2026-11-07T10-00-00-"+codexParentID+".jsonl"), codexMeta(codexParentID, ""))
	writeTestFile(t, filepath.Join(day, "rollout-2026-11-07T10-01-00-"+codexChildID+".jsonl"), codexMeta(codexChildID, codexParentID)+
		`{"timestamp":"2026-11-07T10:02:00.000Z","type":"event_msg","payload":{"type":"task_complete"}}`+"\n")
	writeTestFile(t, filepath.Join(day, "rollout-2026-11-07T10-03-00-"+codexOtherID+".jsonl"), codexMeta(codexOtherID, "someone-else"))
	src := NewTranscriptSource(home).(*fileTranscripts)
	now := time.Unix(1000, 0)
	src.now = func() time.Time { return now }
	sess := domain.Session{ID: "a", AgentName: "codex", AgentSessionID: codexParentID}

	r, err := src.Open(sess, "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := r.Page(nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	var kids []transcript.Item
	for _, it := range p.Items {
		if it.Kind == transcript.KindSubagent {
			kids = append(kids, it)
		}
	}
	if len(kids) != 1 || kids[0].Subagent.ID != codexChildID || kids[0].Subagent.State != transcript.SubagentDone {
		t.Fatalf("subagent items = %+v, want the done child", kids)
	}
	if _, err := src.Open(sess, codexChildID); err != nil {
		t.Fatalf("open child: %v", err)
	}
	late := codexMeta("44444444-4444-4444-4444-444444444444", codexParentID)
	writeTestFile(t, filepath.Join(day, "rollout-2026-11-07T10-04-00-44444444-4444-4444-4444-444444444444.jsonl"), late)
	if got := len(src.codexChildren(src.entries["a"])); got != 1 {
		t.Fatalf("within the rescan window: %d children, want the cached 1", got)
	}
	now = now.Add(codexChildRescan)
	if got := len(src.codexChildren(src.entries["a"])); got != 2 {
		t.Fatalf("after the rescan window: %d children, want 2", got)
	}
}

// TestTranscriptUnknownSubagentNotFound: session.transcript for a sub-agent
// with no transcript is not_found, never the parent's pane.
func TestTranscriptUnknownSubagentNotFound(t *testing.T) {
	home := t.TempDir()
	writeTestFile(t, filepath.Join(home, ".claude", "projects", "-w", "s1.jsonl"), claudeLine)
	srv, pty, _ := ptyServer(t, domain.Session{
		ID: "tr-sub", Name: "tr-sub", Group: "g", TerminalName: "tr-sub", Backend: domain.BackendPTY,
		AgentName: "claude", AgentSessionID: "s1", WorkingDirectory: "/w",
	})
	pty.Add(ptyruntime.SessionInfo{ID: "tr-sub", Name: "tr-sub", Dir: "/w"})
	srv.SetTranscripts(NewTranscriptSource(home))

	resp := scopeCall(t, srv, "", scopeNoPID, "session.transcript", map[string]any{"session": "tr-sub", "subagent": "nope"})
	wantCode(t, resp, CodeNotFound)
	resp = scopeCall(t, srv, "", scopeNoPID, "session.transcript", map[string]any{"session": "tr-sub"})
	if resp.Error != nil {
		t.Fatalf("parent: %+v", resp.Error)
	}
	if page, ok := resp.Result.(transcript.Page); !ok || page.Source != transcript.SourceTranscript {
		t.Fatalf("parent page = %+v, want the transcript", resp.Result)
	}
}
