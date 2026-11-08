package server

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/transcript"
)

func writeTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

const claudeLine = `{"type":"user","uuid":"u1","parentUuid":null,"timestamp":"2026-11-07T10:00:00Z","message":{"role":"user","content":"hello"}}` + "\n"

func TestTranscriptSourceAgentSessionPathConfined(t *testing.T) {
	home := t.TempDir()
	inside := filepath.Join(home, ".claude", "projects", "-w", "s1.jsonl")
	writeTestFile(t, inside, claudeLine)
	outside := filepath.Join(home, "elsewhere", "s1.jsonl")
	writeTestFile(t, outside, claudeLine)
	link := filepath.Join(home, ".claude", "projects", "-w", "link.jsonl")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	src := NewTranscriptSource(home)

	r, err := src.Open(domain.Session{ID: "a", AgentName: "claude", AgentSessionPath: inside, WorkingDirectory: "/w"}, "")
	if err != nil {
		t.Fatalf("inside the vendor root: %v", err)
	}
	p, err := r.Page(nil, 10)
	if err != nil || len(p.Items) != 1 || p.Items[0].Text != "hello" {
		t.Fatalf("page = %+v, %v", p.Items, err)
	}
	for _, path := range []string{outside, link} {
		if _, err := src.Open(domain.Session{ID: "b", AgentName: "claude", AgentSessionPath: path, WorkingDirectory: "/w"}, ""); !errors.Is(err, transcript.ErrNoTranscript) {
			t.Errorf("%s: err = %v, want ErrNoTranscript", path, err)
		}
	}
}

func TestTranscriptSourceClaudeDirsAndSubagent(t *testing.T) {
	home := t.TempDir()
	parent := filepath.Join(home, ".claude", "projects", "-w", "s1.jsonl")
	writeTestFile(t, parent, claudeLine)
	writeTestFile(t, filepath.Join(home, ".claude", "projects", "-w", "s1", "subagents", "agent-x1.jsonl"), claudeLine)
	src := NewTranscriptSource(home)
	sess := domain.Session{ID: "a", AgentName: "claude", AgentSessionID: "s1", WorkingDirectory: "/w"}

	r, err := src.Open(sess, "")
	if err != nil {
		t.Fatal(err)
	}
	if dirs := r.SessionDirs(); len(dirs) < 2 || dirs[0] != filepath.Join(home, ".claude", "projects", "-w", "s1") {
		t.Fatalf("session dirs = %v", dirs)
	}
	if _, err := src.Open(sess, "x1"); err != nil {
		t.Fatalf("sub-agent: %v", err)
	}
	if files := r.Files(); len(files) != 2 {
		t.Fatalf("files = %v, want the parent and the opened sub-agent", files)
	}
	for _, bad := range []string{"missing", "..", "a/b"} {
		if _, err := src.Open(sess, bad); !errors.Is(err, transcript.ErrNoTranscript) {
			t.Errorf("sub-agent %q: err = %v, want ErrNoTranscript", bad, err)
		}
	}
	if _, err := src.Open(domain.Session{ID: "c", AgentName: "bash"}, ""); !errors.Is(err, transcript.ErrNoTranscript) {
		t.Errorf("bash: err = %v, want ErrNoTranscript", err)
	}
}

func TestConfineOutputPath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sess")
	writeTestFile(t, filepath.Join(dir, "tasks", "a.output"), "x")
	writeTestFile(t, filepath.Join(root, "other", "b.output"), "x")

	cases := []struct {
		path       string
		ok, exists bool
		dirs       []string
	}{
		{filepath.Join(dir, "tasks", "a.output"), true, true, []string{dir}},
		{filepath.Join(dir, "tasks", "new.output"), true, false, []string{dir}},
		{filepath.Join(dir, "later", "deeper", "new.output"), true, false, []string{dir}},
		{filepath.Join(root, "other", "b.output"), false, false, []string{dir}},
		{filepath.Join(dir, "tasks") + "/../../other/b.output", false, false, []string{dir}},
		{dir, false, false, []string{dir}},
		{"relative/a.output", false, false, []string{dir}},
		{filepath.Join(root, "notyet", "s", "x.output"), true, false, []string{filepath.Join(root, "notyet", "s")}},
	}
	for _, c := range cases {
		_, exists, ok := confineOutputPath(c.path, c.dirs)
		if ok != c.ok || exists != c.exists {
			t.Errorf("%s: ok %v exists %v, want %v %v", c.path, ok, exists, c.ok, c.exists)
		}
	}
}

func TestProcessOutputWindow(t *testing.T) {
	data := make([]byte, ProcessOutputMaxBytes+10)
	var o processOutputResult
	o.fill(data, nil)
	if o.Offset != 10 || !o.Truncated || len(o.Data) != ProcessOutputMaxBytes {
		t.Fatalf("tail: %+v", o.Offset)
	}
	since := int64(len(data) + 5)
	o = processOutputResult{}
	o.fill(data, &since)
	if o.Offset != int64(len(data)) || o.Truncated || o.Data != "" {
		t.Fatalf("since past the end: offset %d truncated %v", o.Offset, o.Truncated)
	}
}
