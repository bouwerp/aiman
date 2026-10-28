package ui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// recordingTerminal records every remote command and file write. pty get
// reports nothing running, so a recreate goes straight to create.
type recordingTerminal struct {
	mu     sync.Mutex
	cmds   []string
	writes []string
}

func (r *recordingTerminal) Execute(_ context.Context, cmd string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cmds = append(r.cmds, cmd)
	return "", nil
}

func (r *recordingTerminal) WriteFile(_ context.Context, path string, _ []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, path)
	return nil
}

func (r *recordingTerminal) all() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.cmds, "\n")
}

func legacyRestartSession() *domain.Session {
	return &domain.Session{
		ID: "sess-1", RemoteHost: "devbox", TerminalName: "PB-1",
		WorkingDirectory: "/work/repo@PB-1", Backend: domain.BackendLegacyTmux,
		Status: domain.SessionStatusActive, AgentName: "claude",
	}
}

func TestRestartTerminalLegacyRequiresConfirmation(t *testing.T) {
	remote := &recordingTerminal{}
	s := legacyRestartSession()

	err := restartTerminal(context.Background(), remote, s, restartTerminalSpec{
		WorkingDir: s.WorkingDirectory,
		AgentCmd:   "claude",
	})
	if !errors.Is(err, domain.ErrLegacyTmuxSession) {
		t.Fatalf("restart of an unconfirmed legacy row: err = %v, want ErrLegacyTmuxSession", err)
	}
	if got := remote.all(); got != "" {
		t.Fatalf("an unconfirmed legacy restart must run nothing, ran:\n%s", got)
	}
	if s.Backend != domain.BackendLegacyTmux {
		t.Fatalf("backend changed without confirmation: %q", s.Backend)
	}

	err = restartTerminal(context.Background(), remote, s, restartTerminalSpec{
		WorkingDir:    s.WorkingDirectory,
		AgentCmd:      "claude",
		LegacyStopped: true,
	})
	if err != nil {
		t.Fatalf("confirmed legacy restart: %v", err)
	}
	got := remote.all()
	if !strings.Contains(got, "aiman pty create") {
		t.Fatalf("confirmed legacy restart must create a pty session, ran:\n%s", got)
	}
	if strings.Contains(got, "tmux") {
		t.Fatalf("aiman must never run tmux, ran:\n%s", got)
	}
}

func TestRestartTerminalPTYDoesNotTouchTmux(t *testing.T) {
	remote := &recordingTerminal{}
	s := &domain.Session{
		ID: "sess-2", RemoteHost: "devbox", TerminalName: "PB-2",
		WorkingDirectory: "/work/repo@PB-2", Backend: domain.BackendPTY,
	}
	err := restartTerminal(context.Background(), remote, s, restartTerminalSpec{
		WorkingDir: s.WorkingDirectory,
		AgentCmd:   "kilo",
		Env:        map[string]string{"AIMAN_ID": "sess-2"},
		Secrets:    []domain.Secret{{Key: "TOKEN", Value: "x"}},
	})
	if err != nil {
		t.Fatalf("restartTerminal: %v", err)
	}
	got := remote.all()
	if strings.Contains(got, "tmux") {
		t.Fatalf("pty restart ran a tmux command:\n%s", got)
	}
	for _, want := range []string{"aiman pty kill", "aiman pty create"} {
		if !strings.Contains(got, want) {
			t.Errorf("pty restart did not run %q:\n%s", want, got)
		}
	}
}

func TestRestartSetsBackendPTY(t *testing.T) {
	for _, backend := range []string{"", domain.BackendPTY, domain.BackendLegacyTmux} {
		remote := &recordingTerminal{}
		s := legacyRestartSession()
		s.Backend = backend
		s.Status = domain.SessionStatusInactive
		if err := restartTerminal(context.Background(), remote, s, restartTerminalSpec{
			WorkingDir:    s.WorkingDirectory,
			AgentCmd:      "claude",
			LegacyStopped: true,
		}); err != nil {
			t.Fatalf("backend %q: %v", backend, err)
		}
		if s.Backend != domain.BackendPTY {
			t.Errorf("backend %q: after restart Backend = %q, want %q", backend, s.Backend, domain.BackendPTY)
		}
		if s.Status != domain.SessionStatusActive {
			t.Errorf("backend %q: after restart Status = %q, want active", backend, s.Status)
		}
	}
}
