//go:build acceptance

package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/ptyruntime/ptyfake"
	"github.com/bouwerp/aiman/internal/transcript"
)

const trEventSession = "trev-sess"

// transcriptEventServer serves over a socket with one session whose parent
// transcript and sub-agent a3f0 are the given files.
func transcriptEventServer(t *testing.T, parentFile, subFile string) string {
	t.Helper()
	t.Setenv("AIMAN_ID", "")
	repo := testRepo(t)
	row := domain.Session{
		ID: trEventSession, Name: trEventSession, Group: "g", TerminalName: trEventSession,
		Backend: domain.BackendPTY, AgentName: "claude", AgentSessionPath: parentFile,
		WorkingDirectory: "/w/" + trEventSession, Status: domain.SessionStatusActive, CreatedAt: time.Now(),
	}
	if err := repo.Save(context.Background(), &row); err != nil {
		t.Fatal(err)
	}
	pty := ptyfake.New()
	pty.Add(ptyruntime.SessionInfo{ID: trEventSession, Name: trEventSession, Dir: row.WorkingDirectory})

	page := transcript.Page{Type: transcript.PageType, Source: transcript.SourceTranscript}
	src := &fakeTranscripts{}
	src.add(trEventSession, "", &fakeReader{page: page, files: []string{parentFile}})
	src.add(trEventSession, "a3f0", &fakeReader{page: page, files: []string{subFile}})

	dir := shortTempDir(t)
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv := New(ln, repo, &fakeRemote{}, nil, nil, pty, "test")
	srv.SetTranscripts(src)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() { defer close(served); _ = srv.Serve(ctx) }()
	t.Cleanup(func() { cancel(); <-served })
	return SocketPath(dir)
}

// nextTranscript waits up to within for a transcript event for the session
// and sub-agent, skipping every other event.
func nextTranscript(got <-chan SessionEvent, subagent string, within time.Duration) (SessionEvent, bool) {
	deadline := time.After(within)
	for {
		select {
		case ev, ok := <-got:
			if !ok {
				return SessionEvent{}, false
			}
			if ev.Type == "transcript" && ev.ID == trEventSession && ev.Subagent == subagent {
				return ev, true
			}
		case <-deadline:
			return SessionEvent{}, false
		}
	}
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestTranscriptEvent: the stream poll sends transcript {id, subagent?, rev}
// when a reader's file changes size or mtime, at most once a second per
// session and file, to session.events subscribers.
func TestTranscriptEvent(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Join(dir, "parent.jsonl")
	sub := filepath.Join(dir, "agent-a3f0.jsonl")
	writeFile(t, parent, []byte("{\"uuid\":\"1\"}\n"))
	writeFile(t, sub, []byte("{\"uuid\":\"s\"}\n"))
	sock := transcriptEventServer(t, parent, sub)

	// Reading the parent and the sub-agent pins both.
	for _, params := range []map[string]any{
		{"session": trEventSession},
		{"session": trEventSession, "subagent": "a3f0"},
	} {
		if _, err := Call(sock, "session.transcript", params); err != nil {
			t.Fatal(err)
		}
	}

	conn, err := EventsDial(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	got := readEvents(conn)
	// Let any first-observation events pass and the debounce window close.
	nextTranscript(got, "\x00none", 1500*time.Millisecond)

	var first SessionEvent
	t.Run("size change fires", func(t *testing.T) {
		appendTo(t, parent, "{\"uuid\":\"2\"}\n")
		ev, ok := nextTranscript(got, "", 3*time.Second)
		if !ok {
			t.Fatal("no transcript event after the parent grew")
		}
		if ev.Rev == 0 {
			t.Fatalf("transcript event carries no rev: %+v", ev)
		}
		first = ev
	})

	t.Run("debounced within a second", func(t *testing.T) {
		if first.Type == "" {
			t.Skip("no first event")
		}
		sent := time.Now()
		appendTo(t, parent, "{\"uuid\":\"3\"}\n")
		if ev, ok := nextTranscript(got, "", 600*time.Millisecond); ok {
			t.Fatalf("second event %v after the first: %+v", time.Since(sent), ev)
		}
		if _, ok := nextTranscript(got, "", 3*time.Second); !ok {
			t.Fatal("the change held back by the debounce was never sent")
		}
	})

	t.Run("same-size mtime change fires", func(t *testing.T) {
		nextTranscript(got, "", 1200*time.Millisecond)
		info, err := os.Stat(parent)
		if err != nil {
			t.Fatal(err)
		}
		same := make([]byte, info.Size())
		for i := range same {
			same[i] = 'z'
		}
		same[len(same)-1] = '\n'
		writeFile(t, parent, same)
		later := info.ModTime().Add(time.Hour)
		if err := os.Chtimes(parent, later, later); err != nil {
			t.Fatal(err)
		}
		if _, ok := nextTranscript(got, "", 3*time.Second); !ok {
			t.Fatal("no transcript event after a same-size rewrite with a new mtime")
		}
	})

	t.Run("sub-agent file change fires with subagent", func(t *testing.T) {
		nextTranscript(got, "a3f0", 1200*time.Millisecond)
		appendTo(t, sub, "{\"uuid\":\"s2\"}\n")
		ev, ok := nextTranscript(got, "a3f0", 3*time.Second)
		if !ok {
			t.Fatal("no transcript event with subagent a3f0 after its file grew")
		}
		if ev.Rev == 0 {
			t.Fatalf("sub-agent event carries no rev: %+v", ev)
		}
	})
}
