package server

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/todo"
)

// Only a running record whose PID is still a holder roots the ancestry walk,
// so a stale record whose PID was reused by an unrelated process (an
// operator's shell) does not turn that process into a session.
func TestHolderRoots(t *testing.T) {
	infos := []ptyruntime.SessionInfo{
		{ID: "live", PID: 10, Status: ptyruntime.StatusRunning},
		{ID: "exited", PID: 20, Status: ptyruntime.StatusExited},
		{ID: "reused", PID: 30, Status: ptyruntime.StatusRunning},
		{ID: "nopid", PID: 0, Status: ptyruntime.StatusRunning},
	}
	isHolder := func(pid int) bool { return pid == 10 || pid == 20 }
	roots := holderRoots(infos, isHolder)
	if len(roots) != 1 || !roots[10] {
		t.Fatalf("roots = %v, want only 10", roots)
	}
}

func TestIsHolderCmdline(t *testing.T) {
	for cmdline, want := range map[string]bool{
		"aiman\x00pty\x00hold\x00--root\x00/r\x00--id\x00s1\x00": true,
		"/opt/aiman-dev\x00pty\x00hold\x00--id\x00s1\x00":        true,
		"aiman\x00pty\x00hold\x00":                               false,
		"bash\x00-l\x00":                                         false,
		"aiman\x00pty\x00list\x00--id\x00s1\x00":                 false,
		"":                                                       false,
	} {
		if got := isHolderCmdline([]byte(cmdline)); got != want {
			t.Errorf("isHolderCmdline(%q) = %v, want %v", cmdline, got, want)
		}
	}
}

func TestHeldByHolder(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	mgr := ptyruntime.NewManagerWithRoot(shortTempDir(t), append([]string(nil), holderBin...))
	t.Cleanup(mgr.CloseAll)
	info, err := mgr.Create(ptyruntime.Spec{ID: "peer-holder", Command: "sleep 30", Dir: os.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !heldByHolder(info.PID) {
		t.Fatalf("session process %d not recognised as held", info.PID)
	}
	other := exec.Command("sleep", "30")
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = other.Process.Kill(); _ = other.Wait() })
	if heldByHolder(other.Process.Pid) || heldByHolder(os.Getpid()) {
		t.Fatal("a process outside any holder counted as held")
	}
}

// TestSessionPromptOutsideTreeIsPeerNote: with peer notes on, a prompt from a
// session to an unrelated session or to its parent is queued, not refused.
func TestSessionPromptOutsideTreeIsPeerNote(t *testing.T) {
	for _, c := range []struct{ name, caller, target string }{
		{"unrelated", scopeA, scopeB},
		{"child to parent", scopeC, scopeA},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv, pty := scopeServer(t)
			resp := scopeCall(t, srv, c.caller, scopeNoPID, "session.prompt", map[string]any{"id": c.target, "text": "hi"})
			if resp.Error != nil {
				t.Fatalf("error = %+v, want a queued peer note", resp.Error)
			}
			b, _ := json.Marshal(resp.Result)
			var out struct {
				Sent        bool   `json:"sent"`
				Queued      bool   `json:"queued"`
				DeliveredAs string `json:"delivered_as"`
			}
			_ = json.Unmarshal(b, &out)
			if out.Sent || !out.Queued || out.DeliveredAs != "note" {
				t.Fatalf("result = %s, want queued as a note", b)
			}
			if len(pty.Writes()) != 0 {
				t.Fatalf("typed into the terminal: %v", pty.Writes())
			}
		})
	}
}

// peerTargetServer holds sess-a and the given target row, with steering on.
func peerTargetServer(t *testing.T, target domain.Session, info *ptyruntime.SessionInfo) *Server {
	t.Helper()
	srv, pty, _ := ptyServer(t, scopeRow(scopeA, ""), target)
	pty.Add(ptyruntime.SessionInfo{ID: scopeA, Name: scopeA})
	if info != nil {
		pty.Add(*info)
	}
	srv.target = &fakeSteerTarget{held: steerHeldWorking}
	srv.EnableTodos(todo.NewFiles(t.TempDir()), &fakeSteerStore{})
	return srv
}

func TestPeerNoteToLegacyTmuxIsLegacyTerminal(t *testing.T) {
	legacy := legacyRow()
	for _, m := range []struct {
		method string
		params map[string]any
	}{
		{"session.prompt", map[string]any{"id": legacy.ID, "text": "hi"}},
		{"steer.send", map[string]any{"session": legacy.ID, "text": "hi"}},
	} {
		t.Run(m.method, func(t *testing.T) {
			srv := peerTargetServer(t, legacy, nil)
			resp := scopeCall(t, srv, scopeA, scopeNoPID, m.method, m.params)
			if resp.Error == nil || resp.Error.Code != CodeLegacyTerminal {
				t.Fatalf("error = %+v, want %s", resp.Error, CodeLegacyTerminal)
			}
		})
	}
}

func TestPeerNoteToExitedHolderIsEnded(t *testing.T) {
	const id = "sess-x"
	srv := peerTargetServer(t, scopeRow(id, ""), &ptyruntime.SessionInfo{ID: id, Name: id, Status: ptyruntime.StatusExited})
	resp := scopeCall(t, srv, scopeA, scopeNoPID, "session.prompt", map[string]any{"id": id, "text": "hi"})
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams || !strings.Contains(resp.Error.Message, "target agent has ended") {
		t.Fatalf("error = %+v, want invalid_params target agent has ended", resp.Error)
	}
}

func TestPeerNoteSeen(t *testing.T) {
	old := peerWaitQuiet
	peerWaitQuiet = time.Second
	t.Cleanup(func() { peerWaitQuiet = old })
	at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		hook time.Time
		now  time.Time
		want bool
	}{
		{"no hook, inside the quiet window", time.Time{}, at.Add(500 * time.Millisecond), false},
		{"no hook, quiet window passed", time.Time{}, at.Add(2 * time.Second), true},
		{"hook before the submit", at.Add(-time.Second), at.Add(time.Minute), false},
		{"hook after the submit", at.Add(time.Second), at.Add(time.Second), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := peerNoteSeen(domain.Session{HookStateAt: c.hook}, at, c.now); got != c.want {
				t.Fatalf("peerNoteSeen = %v, want %v", got, c.want)
			}
		})
	}
}

// TestKickAfterRunNeverDelivers: once Run has returned, a Kick types nothing.
func TestKickAfterRunNeverDelivers(t *testing.T) {
	target := &fakeSteerTarget{ready: true}
	s := newSteerer(&fakeSteerStore{}, target, nil, testSteerWindow)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.Run(ctx)
	if _, err := s.EnqueuePeer(SteerNote{SessionID: "b", From: "a", Author: domain.AuthorPeer, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	s.Kick("b")
	time.Sleep(100 * time.Millisecond)
	if d := target.deliveries(); len(d) != 0 {
		t.Fatalf("delivered after Run returned: %v", d)
	}
}

// TestPeerPromptWaitNotSettled: the note is typed but the target never shows
// it picked it up, so the wait names that instead of a context error.
func TestPeerPromptWaitNotSettled(t *testing.T) {
	srv, _, _, target := peerServer(t)
	target.mu.Lock()
	target.ready = true
	target.submitAt = time.Now().Add(time.Hour) // the quiet window never passes
	target.mu.Unlock()
	resp := scopeCall(t, srv, scopeA, pidA, "session.prompt", map[string]any{"id": scopeB, "text": "hi", "wait": true, "timeout_ms": 500})
	if resp.Error == nil || resp.Error.Code != CodeTimeout || resp.Error.Message != "peer note delivered, target not settled" {
		t.Fatalf("error = %+v, want timeout: peer note delivered, target not settled", resp.Error)
	}
}
