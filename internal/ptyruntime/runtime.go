// Package ptyruntime is a thin client over the ptyhold contract: sessions are
// owned by detached holder processes (which survive serve restarts), and this
// package merely spawns holders and proxies operations to the session
// directory's files and socket. See internal/ptyhold for the contract.
package ptyruntime

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/ptyhold"
)

// ErrNotFound is returned when a session id does not exist.
var ErrNotFound = errors.New("pty session not found")

// killTimeout covers the holder's internal SIGTERM->SIGKILL grace plus slack.
const killTimeout = 8 * time.Second

// Spec describes a session to create.
type Spec struct {
	// ID is the aiman session id the PTY belongs to. Required; one PTY per id.
	ID string
	// Name is a human-facing handle (the branch-derived terminal name).
	Name string
	// Dir is the working directory for the spawned command.
	Dir string
	// Command runs under `bash -l -c '<command>; exec bash -i'` inside the PTY.
	Command string
	// Env is added on top of the holder's inherited environment.
	Env map[string]string
	// Cols/Rows set the initial window size (0 defaults to 80x24).
	Cols, Rows int
}

// Status describes the lifecycle state of a session.
type Status string

const (
	StatusRunning Status = "running"
	StatusExited  Status = "exited"
)

// SessionInfo is the wire-safe view of a session.
type SessionInfo struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Dir     string    `json:"dir"`
	Command string    `json:"command,omitempty"`
	Status  Status    `json:"status"`
	PID     int       `json:"pid,omitempty"`
	ExitErr string    `json:"exit_error,omitempty"`
	Started time.Time `json:"started_at"`
	Size    string    `json:"size,omitempty"`
}

// Manager creates and tracks holder-backed PTY sessions. It holds no process
// state: every call reads the contract files or talks to the live socket.
type Manager struct {
	root string
	// HolderCmd invokes the holder binary; default is this executable with
	// "pty hold". Injectable for tests.
	HolderCmd []string

	mu    sync.Mutex
	conns map[string]net.Conn // cached input connections per session id

	subs sync.WaitGroup
}

// NewManager returns a manager rooted at the aiman config directory.
func NewManager() *Manager {
	return &Manager{
		root:  mustRoot(),
		conns: map[string]net.Conn{},
	}
}

// NewManagerWithRoot returns a manager over an explicit root (tests).
func NewManagerWithRoot(root string, holderCmd []string) *Manager {
	return &Manager{root: root, HolderCmd: holderCmd, conns: map[string]net.Conn{}}
}

func mustRoot() string {
	dir, err := config.GetDir()
	if err != nil {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "."
		}
		return filepath.Join(home, ".aiman")
	}
	return dir
}

func (m *Manager) holderCmd() []string {
	if len(m.HolderCmd) > 0 {
		return m.HolderCmd
	}
	// Under `go test`, os.Executable() is the *test binary*, and a Go test
	// binary ignores unknown positional args — so re-execing it as
	// "<binary> pty hold <id>" silently re-runs the whole test suite instead
	// of holding a PTY. Any suite that then creates a session forks another
	// full suite, exponentially: this once grew to ~2000 resident processes
	// and OOM-killed the dev box. Fail loudly instead; tests must inject
	// HolderCmd (see NewManagerWithRoot).
	if testing.Testing() {
		panic("ptyruntime: HolderCmd must be set explicitly under go test — " +
			"defaulting to os.Executable() would re-exec the test binary as its own holder")
	}
	exe, err := os.Executable()
	if err != nil {
		return []string{"aiman", "pty", "hold"}
	}
	return []string{exe, "pty", "hold"}
}

// Create spawns a new holder-backed PTY session.
func (m *Manager) Create(spec Spec) (*SessionInfo, error) {
	if spec.ID == "" {
		return nil, errors.New("pty: id is required")
	}
	if spec.Command == "" {
		return nil, errors.New("pty: command is required")
	}
	switch insp := ptyhold.InspectSession(m.root, spec.ID); insp.Status {
	case ptyhold.StatusRunning:
		return nil, fmt.Errorf("pty: session %s already exists", spec.ID)
	case ptyhold.StatusExited:
		// Previous run's leftovers; clear them so the fresh holder starts clean.
		if err := ptyhold.Cleanup(m.root, spec.ID); err != nil {
			return nil, fmt.Errorf("pty: clean stale session: %w", err)
		}
	}
	if err := ptyhold.Spawn(m.root, ptyhold.Spec{
		ID:      spec.ID,
		Name:    spec.Name,
		Dir:     spec.Dir,
		Command: spec.Command,
		Env:     spec.Env,
		Cols:    spec.Cols,
		Rows:    spec.Rows,
	}, m.holderCmd()); err != nil {
		return nil, fmt.Errorf("pty: start %q: %w", spec.Command, err)
	}
	return m.Get(spec.ID)
}

// List returns every session known to the contract, sorted by start time.
func (m *Manager) List() []SessionInfo {
	ids, err := ptyhold.ScanIDs(m.root)
	if err != nil {
		return nil
	}
	out := make([]SessionInfo, 0, len(ids))
	for _, id := range ids {
		if info, err := m.Get(id); err == nil {
			out = append(out, *info)
		}
	}
	sortSessions(out)
	return out
}

// Get returns one session by id.
func (m *Manager) Get(id string) (*SessionInfo, error) {
	insp := ptyhold.InspectSession(m.root, id)
	if insp.Status == ptyhold.StatusGone && insp.Exit == "" {
		if _, err := os.Stat(ptyhold.Dir(m.root, id)); err != nil {
			return nil, ErrNotFound
		}
	}
	status := Status(insp.Status)
	info := SessionInfo{
		ID:      id,
		Name:    insp.Meta.Name,
		Dir:     insp.Meta.Dir,
		Command: insp.Meta.Command,
		PID:     insp.Meta.PID,
		Status:  status,
		ExitErr: insp.Exit,
	}
	if t, terr := time.Parse(time.RFC3339, insp.Meta.Started); terr == nil {
		info.Started = t
	}
	return &info, nil
}

// Write sends raw bytes to the session's terminal via the live socket.
func (m *Manager) Write(id string, data []byte) error {
	conn, err := m.inputConn(id)
	if err != nil {
		return err
	}
	if _, werr := conn.Write(data); werr != nil {
		// One redial on a stale connection, then give up.
		m.dropConn(id)
		conn, err = m.inputConn(id)
		if err != nil {
			return err
		}
		if _, werr := conn.Write(data); werr != nil {
			return fmt.Errorf("pty: write %s: %w", id, werr)
		}
	}
	return nil
}

func (m *Manager) inputConn(id string) (net.Conn, error) {
	m.mu.Lock()
	conn, ok := m.conns[id]
	m.mu.Unlock()
	if ok {
		return conn, nil
	}
	conn, err := ptyhold.Dial(m.root, id)
	if err != nil {
		return nil, fmt.Errorf("pty: session %s has exited", id)
	}
	m.mu.Lock()
	m.conns[id] = conn
	m.mu.Unlock()
	return conn, nil
}

func (m *Manager) dropConn(id string) {
	m.mu.Lock()
	conn, ok := m.conns[id]
	delete(m.conns, id)
	m.mu.Unlock()
	if ok {
		_ = conn.Close()
	}
}

// Resize asks the holder to set the window size.
func (m *Manager) Resize(id string, cols, rows int) error {
	if err := ptyhold.RequestResize(m.root, id, cols, rows); err != nil {
		return err
	}
	return nil
}

// Capture returns up to maxBytes of recent output from the spool files.
func (m *Manager) Capture(id string, maxBytes int) ([]byte, error) {
	if _, err := m.Get(id); err != nil {
		return nil, err
	}
	return ptyhold.ReadSpool(m.root, id, maxBytes), nil
}

// Kill terminates a session via the kill marker and waits for the holder to
// finish its cleanup.
func (m *Manager) Kill(id string) error {
	insp := ptyhold.InspectSession(m.root, id)
	if insp.Status == ptyhold.StatusGone {
		return ErrNotFound
	}
	if insp.Status != ptyhold.StatusRunning {
		return fmt.Errorf("pty: session %s has exited", id)
	}
	if err := ptyhold.RequestKill(m.root, id); err != nil {
		return err
	}
	deadline := time.Now().Add(killTimeout)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		if now := ptyhold.InspectSession(m.root, id); now.Status != ptyhold.StatusRunning {
			m.dropConn(id)
			return nil
		}
	}
	m.dropConn(id)
	return fmt.Errorf("pty: session %s did not stop in time", id)
}

// Forget removes an exited session's directory entirely.
func (m *Manager) Forget(id string) error {
	insp := ptyhold.InspectSession(m.root, id)
	if insp.Status == ptyhold.StatusGone {
		return ErrNotFound
	}
	if insp.Status == ptyhold.StatusRunning {
		return fmt.Errorf("pty: session still running")
	}
	return ptyhold.Cleanup(m.root, id)
}

// Subscribe returns everything currently in the spool for immediate replay
// plus a live channel fed from the session's socket. unsub must be called when
// the consumer goes away.
func (m *Manager) Subscribe(id string) ([]byte, <-chan []byte, func(), error) {
	if _, err := m.Get(id); err != nil {
		return nil, nil, nil, err
	}
	replay := ptyhold.ReadSpool(m.root, id, 1<<20)
	live := make(chan []byte, 256)

	conn, err := ptyhold.Dial(m.root, id)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("pty: session %s has exited", id)
	}
	done := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			close(done)
			_ = conn.Close()
		})
	}
	m.subs.Add(1)
	go func() {
		defer m.subs.Done()
		defer close(live)
		buf := make([]byte, 16<<10)
		for {
			n, rerr := conn.Read(buf)
			if n > 0 {
				chunk := append([]byte(nil), buf[:n]...)
				select {
				case live <- chunk:
				case <-done:
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()
	return replay, live, stop, nil
}

// CloseAll releases only serve-side resources. Holders are deliberately left
// running — surviving serve restarts and updates is their entire purpose.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	conns := m.conns
	m.conns = map[string]net.Conn{}
	m.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	m.subs.Wait()
}

func sortSessions(list []SessionInfo) {
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].Started.Before(list[j-1].Started); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}
