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
	"github.com/bouwerp/aiman/internal/handoverchild"
	"github.com/bouwerp/aiman/internal/infra/sqlite"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/ptyruntime/ptyfake"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/todo"
	"github.com/bouwerp/aiman/internal/usage"
	"github.com/bouwerp/aiman/internal/usecase"
	"github.com/bouwerp/aiman/internal/verify"
)

// Harness is one in-process serve, its temp database, and the fake remote and
// PTY runtime the case observes. It starts no SSH connection and no process.
type Harness struct {
	Dir    string
	Socket string
	Repo   *sqlite.Repository

	t        *testing.T
	srv      *server.Server
	remote   *FakeRemote
	pty      *ptyfake.FakePTY
	pane     string
	usage    *usage.Tracker
	verifier *verify.Manager
	reviews  *verify.Store
	terminal *evalTerminal

	handovers    *handoverchild.Manager
	handoverRuns *handoverchild.Store

	coord *evalCoordinator

	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

// New starts serve on a short temp socket over a FakePTY: every session's
// terminal is a scripted screen, and typing into it is recorded.
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
	pty := ptyfake.New()
	remote := NewFakeRemote(dir, pty)
	srv := server.New(ln, repo, remote, FakeCreator{Dir: dir, PTY: pty},
		contextstore.NewFiles(filepath.Join(dir, "context")), pty, "eval")
	srv.EnableTodos(todo.NewFiles(filepath.Join(dir, "todos")), repo)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()
	h := &Harness{Dir: dir, Socket: server.SocketPath(dir), Repo: repo, t: t, srv: srv, remote: remote, pty: pty, cancel: cancel, done: done}
	t.Cleanup(h.Close)
	return h
}

// Close stops serve and closes the database. Safe to call more than once.
func (h *Harness) Close() {
	h.once.Do(func() {
		if h.verifier != nil {
			h.verifier.Close()
		}
		h.closeHandovers()
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

// RegisterSession persists a session and adds its terminal to the FakePTY.
// Empty id, name, status, backend and directory are filled.
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
		s.Backend = domain.BackendPTY
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
	if s.TerminalName == "" {
		s.TerminalName = "term-" + s.ID
	}
	if err := h.Repo.Save(context.Background(), &s); err != nil {
		h.t.Fatal(err)
	}
	// A legacy tmux row has no terminal aiman can reach.
	if !s.IsLegacyTmux() {
		h.pty.Add(ptyruntime.SessionInfo{ID: s.ID, Name: s.TerminalName, Dir: s.WorkingDirectory, Command: s.AgentName})
	}
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

// RepoDir is the registered session's worktree path.
func (h *Harness) RepoDir(id string) string {
	sess, err := h.Repo.Get(context.Background(), id)
	if err != nil {
		return ""
	}
	return sess.WorktreePath
}

// Writes is every Write the FakePTY received.
func (h *Harness) Writes() []ptyfake.Write {
	return h.pty.Writes()
}

// Kills is every session id the FakePTY was asked to kill.
func (h *Harness) Kills() []string {
	return h.pty.Kills()
}

// SetPane sets the screen every FakePTY session shows.
func (h *Harness) SetPane(text string) {
	h.pane = text
	h.pty.SetDefaultScreen(text)
}

// Pane is the screen SetPane last set.
func (h *Harness) Pane() string { return h.pane }

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
