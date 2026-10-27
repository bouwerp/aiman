package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/bouwerp/aiman/evals/fakeagent"
	"github.com/bouwerp/aiman/internal/contextstore"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/todo"
	"github.com/bouwerp/aiman/internal/usecase"
)

// Harness is one in-process serve, its temp database, and the fake remote the
// case observes. It starts no SSH connection and no terminal runtime.
type Harness struct {
	Dir    string
	Socket string
	Repo   *sqlite.Repository

	t      *testing.T
	srv    *server.Server
	remote *FakeRemote
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

// New starts serve on a short temp socket. pty is nil, so every session must
// be a tmux session; the fake remote stands in for the terminal.
func New(t *testing.T) *Harness {
	t.Helper()
	dir, err := os.MkdirTemp("", "aimaneval-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := server.Listen(dir)
	if err != nil {
		t.Skipf("evals: cannot create a unix socket: %v", err)
		return nil
	}
	repo, err := sqlite.NewRepository(filepath.Join(dir, "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	remote := NewFakeRemote(dir)
	srv := server.New(ln, repo, remote, FakeCreator{Dir: dir},
		contextstore.NewFiles(filepath.Join(dir, "context")), nil, "eval")
	srv.EnableTodos(todo.NewFiles(filepath.Join(dir, "todos")), repo)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()
	h := &Harness{Dir: dir, Socket: server.SocketPath(dir), Repo: repo, t: t, srv: srv, remote: remote, cancel: cancel, done: done}
	t.Cleanup(h.Close)
	return h
}

// Close stops serve and closes the database. Safe to call more than once.
func (h *Harness) Close() {
	h.once.Do(func() {
		h.cancel()
		select {
		case <-h.done:
		case <-time.After(2 * time.Second):
			h.t.Log("evals: serve did not stop within 2s")
		}
		_ = h.Repo.Close()
	})
}

// Call invokes one socket method and turns an error response into an error.
func (h *Harness) Call(method string, params any) (server.Response, error) {
	h.t.Helper()
	resp, err := server.Call(h.Socket, method, params)
	if err != nil {
		return resp, err
	}
	if resp.Error != nil {
		return resp, fmt.Errorf("%s: %s", resp.Error.Code, resp.Error.Message)
	}
	return resp, nil
}

// RegisterSession persists a session and tells the fake remote which tmux
// session exists. Empty id, name, status, backend and directory are filled.
func (h *Harness) RegisterSession(s domain.Session) {
	h.t.Helper()
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	if s.Name == "" {
		s.Name = s.ID
	}
	if s.Status == "" {
		s.Status = domain.SessionStatusActive
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	if s.Backend == "" {
		s.Backend = domain.BackendTmux
	}
	if s.WorkingDirectory == "" {
		s.WorkingDirectory = filepath.Join(h.Dir, "sessions", s.ID)
		if err := os.MkdirAll(s.WorkingDirectory, 0o750); err != nil {
			h.t.Fatal(err)
		}
	}
	if s.WorktreePath == "" {
		s.WorktreePath = s.WorkingDirectory
	}
	if s.TmuxSession == "" {
		s.TmuxSession = "tmux-" + s.ID
	}
	if err := h.Repo.Save(context.Background(), &s); err != nil {
		h.t.Fatal(err)
	}
	h.remote.addSession(s.TmuxSession, s.WorkingDirectory)
}

// Report sends a vendor hook payload exactly as the hook script would.
func (h *Harness) Report(raw []byte) error {
	h.t.Helper()
	_, err := h.Call("session.report_agent_session", json.RawMessage(raw))
	return err
}

// RunFakeAgent runs a script in-process. The command is also available for a
// subprocess case.
func (h *Harness) RunFakeAgent(script fakeagent.Script) error {
	h.t.Helper()
	return fakeagent.Run(h.Socket, script)
}

// Events reads the session ledger.
func (h *Harness) Events(sessionID string) []domain.SessionEvent {
	h.t.Helper()
	events, err := h.Repo.ListSessionEvents(context.Background(), sessionID, 0, 1000)
	if err != nil {
		h.t.Fatal(err)
	}
	return events
}

// Execs is every command the fake remote received.
func (h *Harness) Execs() []string {
	return h.remote.Execs()
}

// SetPane sets the pane the fake remote captures.
func (h *Harness) SetPane(text string) { h.remote.Pane = text }

// Pane is the fake remote's current capture.
func (h *Harness) Pane() string { return h.remote.Pane }

// TerminateSession runs the production terminal teardown against the fake
// remote. It does not touch the session row.
func (h *Harness) TerminateSession(s domain.Session) error {
	return usecase.TerminateSessionTerminal(context.Background(), h.remote, s)
}

// Artifacts reads the files a metric may inspect. Missing files are omitted.
func (h *Harness) Artifacts() map[string]string {
	out := map[string]string{}
	if raw, err := os.ReadFile(filepath.Join(h.Dir, "sessions", "s1", domain.AimanHandoverFileName)); err == nil {
		out["handover"] = string(raw)
	}
	if raw, err := os.ReadFile(filepath.Join(h.Dir, "todos", "s1.json")); err == nil {
		out["todo"] = string(raw)
	}
	return out
}
