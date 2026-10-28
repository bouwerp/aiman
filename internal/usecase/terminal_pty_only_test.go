package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// legacyRow is a session an older aiman left running in tmux.
func legacyRow() domain.Session {
	return domain.Session{ID: "old", TerminalName: "WTB-1", RemoteHost: "regent0", Backend: domain.BackendLegacyTmux}
}

// requireLegacyRefusal checks err is the legacy refusal naming host and stop
// command, and that nothing ran on the remote.
func requireLegacyRefusal(t *testing.T, err error, r *terminalRemote) {
	t.Helper()
	if !errors.Is(err, domain.ErrLegacyTmuxSession) {
		t.Fatalf("err = %v, want ErrLegacyTmuxSession", err)
	}
	var le *domain.LegacyTmuxError
	if !errors.As(err, &le) || le.Host != "regent0" || le.Name != "WTB-1" {
		t.Fatalf("err = %#v, want a LegacyTmuxError for regent0/WTB-1", err)
	}
	if len(r.commands) != 0 || len(r.written) != 0 {
		t.Fatalf("a legacy row must not touch the remote: cmds=%v written=%v", r.commands, r.written)
	}
}

func requireNoTmux(t *testing.T, cmds []string) {
	t.Helper()
	for _, c := range cmds {
		if strings.Contains(c, "tmux") {
			t.Fatalf("ran a tmux command: %s", c)
		}
	}
}

// Every backend other than legacy tmux goes to the PTY runtime, including an
// empty backend: discovery and the live stream do not always carry one.
var ptyBackends = []string{domain.BackendPTY, ""}

func TestSendSessionPromptGoesToPTY(t *testing.T) {
	for _, backend := range ptyBackends {
		r := &terminalRemote{}
		s := domain.Session{ID: "pid", TerminalName: "WTB-1", Backend: backend}
		if err := SendSessionPrompt(context.Background(), r, s, "do things"); err != nil {
			t.Fatalf("backend %q: %v", backend, err)
		}
		joined := strings.Join(r.commands, "\n")
		if !strings.Contains(joined, `aiman pty input "pid" --file`) {
			t.Fatalf("backend %q: no pty input: %s", backend, joined)
		}
		requireNoTmux(t, r.commands)
	}
}

func TestSendSessionPromptRefusesLegacy(t *testing.T) {
	r := &terminalRemote{}
	requireLegacyRefusal(t, SendSessionPrompt(context.Background(), r, legacyRow(), "hi"), r)
}

func TestTerminateSessionTerminalGoesToPTY(t *testing.T) {
	for _, backend := range ptyBackends {
		r := &terminalRemote{}
		s := domain.Session{ID: "pid", TerminalName: "WTB-1", Backend: backend}
		if err := TerminateSessionTerminal(context.Background(), r, s); err != nil {
			t.Fatalf("backend %q: %v", backend, err)
		}
		joined := strings.Join(r.commands, "\n")
		if !strings.Contains(joined, "aiman pty kill") || !strings.Contains(joined, "aiman pty forget") {
			t.Fatalf("backend %q: pty teardown missing: %s", backend, joined)
		}
		requireNoTmux(t, r.commands)
	}
}

// Terminate never stops a legacy tmux session itself: the caller shows the
// stop command and lets the user run it.
func TestTerminateSessionTerminalRefusesLegacy(t *testing.T) {
	r := &terminalRemote{}
	requireLegacyRefusal(t, TerminateSessionTerminal(context.Background(), r, legacyRow()), r)
}

func TestCaptureSessionPaneGoesToPTY(t *testing.T) {
	for _, backend := range ptyBackends {
		r := &terminalRemote{output: map[string]string{"aiman pty capture": `{"text":"pane-bytes"}`}}
		out, err := CaptureSessionPane(context.Background(), r, domain.Session{ID: "pid", TerminalName: "WTB-1", Backend: backend})
		if err != nil || out != "pane-bytes" {
			t.Fatalf("backend %q: %q / %v", backend, out, err)
		}
		requireNoTmux(t, r.commands)
	}
}

func TestCaptureSessionPaneRefusesLegacy(t *testing.T) {
	r := &terminalRemote{}
	_, err := CaptureSessionPane(context.Background(), r, legacyRow())
	requireLegacyRefusal(t, err, r)
}

func TestObserveSessionGoesToPTY(t *testing.T) {
	for _, backend := range ptyBackends {
		r := &terminalRemote{output: map[string]string{"aiman pty capture": `{"text":"hello"}`}}
		obs, err := ObserveSession(context.Background(), r, domain.Session{ID: "pid", TerminalName: "WTB-1", Backend: backend})
		if err != nil || obs.Pane != "hello" {
			t.Fatalf("backend %q: %+v / %v", backend, obs, err)
		}
		requireNoTmux(t, r.commands)
	}
}

func TestObserveSessionRefusesLegacy(t *testing.T) {
	r := &terminalRemote{}
	_, err := ObserveSession(context.Background(), r, legacyRow())
	requireLegacyRefusal(t, err, r)
}

func TestRefreshPreviewGoesToPTY(t *testing.T) {
	for _, backend := range ptyBackends {
		r := &terminalRemote{output: map[string]string{"aiman pty capture": `{"text":"hello","hash":"h1"}`}}
		upd, err := RefreshPreview(context.Background(), r, domain.Session{ID: "pid", TerminalName: "WTB-1", Backend: backend}, PaneCache{})
		if err != nil || upd.Text != "hello" {
			t.Fatalf("backend %q: %+v / %v", backend, upd, err)
		}
		requireNoTmux(t, r.commands)
	}
}

func TestRefreshPreviewRefusesLegacy(t *testing.T) {
	r := &terminalRemote{}
	_, err := RefreshPreview(context.Background(), r, legacyRow(), PaneCache{})
	requireLegacyRefusal(t, err, r)
}

func TestResizeSessionTerminalGoesToPTY(t *testing.T) {
	for _, backend := range ptyBackends {
		r := &terminalRemote{output: map[string]string{"aiman pty resize": `{"applied":true}`}}
		applied, err := ResizeSessionTerminal(context.Background(), r, domain.Session{ID: "pid", TerminalName: "WTB-1", Backend: backend}, 120, 40)
		if err != nil || !applied {
			t.Fatalf("backend %q: %v / %v", backend, applied, err)
		}
		if !strings.Contains(strings.Join(r.commands, "\n"), `aiman pty resize "pid" --cols 120 --rows 40`) {
			t.Fatalf("backend %q: %v", backend, r.commands)
		}
		requireNoTmux(t, r.commands)
	}
}

func TestResizeSessionTerminalRefusesLegacy(t *testing.T) {
	r := &terminalRemote{}
	_, err := ResizeSessionTerminal(context.Background(), r, legacyRow(), 120, 40)
	requireLegacyRefusal(t, err, r)
}

// Reviving a legacy row would start a second agent on the conversation the
// tmux one still holds.
func TestReviveIfNeededRefusesLegacy(t *testing.T) {
	r := &terminalRemote{}
	s := legacyRow()
	s.AgentName = "claude"
	s.AgentSessionID = "conv-1"
	revived, err := ReviveIfNeeded(context.Background(), r, &s, ReviveOptions{})
	if revived {
		t.Fatal("a legacy row must not revive")
	}
	requireLegacyRefusal(t, err, r)
}

// An empty backend means no terminal is recorded, not tmux: a dead session
// with no backend revives in the PTY runtime.
func TestReviveIfNeededRevivesEmptyBackend(t *testing.T) {
	r := &terminalRemote{output: map[string]string{
		"aiman pty get":   `{"error":{"code":"not_found"}}`,
		"native-sessions": "",
	}}
	s := domain.Session{ID: "dead", AgentName: "claude", AgentSessionID: "conv-42", WorkingDirectory: "/w", TerminalName: "feat"}
	revived, err := ReviveIfNeeded(context.Background(), r, &s, ReviveOptions{})
	if err != nil || !revived {
		t.Fatalf("expected revival: %v / %v", revived, err)
	}
	var created bool
	for path, body := range r.written {
		if strings.HasPrefix(path, "/tmp/aiman-pty-dead") && strings.Contains(string(body), `"claude --resume conv-42"`) {
			created = true
		}
	}
	if !created {
		t.Fatalf("resume command not in create payload: %v", r.written)
	}
	requireNoTmux(t, r.commands)
}

func TestCaptureSessionHandoffUsesPTYOnly(t *testing.T) {
	r := &terminalRemote{}
	_, _ = CaptureSessionHandoffBestEffort(context.Background(), r, domain.Session{ID: "sid", TerminalName: "WTB-1"}, "/w/summary.md")
	requireNoTmux(t, r.commands)
	if !strings.Contains(strings.Join(r.commands, "\n"), "aiman pty capture") {
		t.Fatalf("expected a pty capture: %v", r.commands)
	}

	legacy := &terminalRemote{}
	created, note := CaptureSessionHandoffBestEffort(context.Background(), legacy, legacyRow(), "/w/summary.md")
	if created || note == "" {
		t.Fatalf("legacy handoff = %v %q, want none with a note", created, note)
	}
	if len(legacy.commands) != 0 {
		t.Fatalf("legacy handoff ran commands: %v", legacy.commands)
	}
}

// A host whose serve is down cannot say which sessions it holds. That is an
// error, never an empty list: the merge step reads an empty list on a scanned
// host as "every session here is gone".
func TestScanPTYSessionsServeDownIsError(t *testing.T) {
	if !strings.Contains(scanPTYSessionsCmd, serveDownMarker) {
		t.Fatalf("scan command never reports serve down: %s", scanPTYSessionsCmd)
	}
	r := &terminalRemote{output: map[string]string{"aiman pty list": serveDownMarker}}
	got, err := ScanPTYSessions(context.Background(), r)
	if !errors.Is(err, ErrServeDown) {
		t.Fatalf("err = %v, want ErrServeDown", err)
	}
	if got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}
