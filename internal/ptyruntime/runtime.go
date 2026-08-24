// Package ptyruntime owns long-lived PTY sessions inside aiman serve.
//
// Each session is a real pseudo-terminal (creack/pty) whose slave side runs an
// agent or shell. The master side is held by the manager: output is appended
// to a bounded scrollback ring and fanned out to live subscribers, input is
// written straight through, and attach clients replay the ring before
// streaming. Sessions live as long as the serve process does — they survive
// laptop disconnects by design, but a serve restart terminates them.
package ptyruntime

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	pty "github.com/creack/pty/v2"
)

const (
	// DefaultScrollbackBytes bounds each session's replay buffer (1 MiB).
	DefaultScrollbackBytes = 1 << 20
	// DefaultKillGrace is how long Kill waits after SIGTERM before SIGKILL.
	DefaultKillGrace = 3 * time.Second
)

// ErrNotFound is returned when a session id does not exist.
var ErrNotFound = errors.New("pty session not found")

// Spec describes a session to create.
type Spec struct {
	// ID is the aiman session id the PTY belongs to. Required; one PTY per id.
	ID string
	// Name is a human-facing handle (the branch-derived terminal name).
	Name string
	// Dir is the working directory for the spawned command.
	Dir string
	// Command is run via `bash -l -c '<command>; exec bash -i'` so agents get a
	// login PATH and drop to an interactive shell if they exit.
	Command string
	// Env is added on top of the serve process environment.
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
	Size    string    `json:"size"`
}

// bootstrapCommand mirrors flow_manager's tmux launch shape: login shell for
// PATH, then drop to an interactive bash when the command exits so failures
// stay inspectable in the pane.
func bootstrapCommand(command string) string {
	return fmt.Sprintf(
		"export PATH=\"$PATH:$HOME/.local/bin:$HOME/.npm-global/bin:$HOME/bin:$HOME/.bun/bin:$HOME/.local/share/pnpm:$HOME/.pnpm:$HOME/.yarn/bin:$HOME/.cargo/bin:/usr/local/bin:/opt/homebrew/bin:$HOME/.opencode/bin\"; %s; exec bash -i",
		command,
	)
}

func envMap(base []string, extra map[string]string) []string {
	out := make([]string, 0, len(base)+len(extra))
	seen := make(map[string]int, len(base)+len(extra))
	for _, kv := range base {
		for i := 0; i < len(kv); i++ {
			if kv[i] == '=' {
				seen[kv[:i]] = len(out)
				break
			}
		}
		out = append(out, kv)
	}
	for k, v := range extra {
		if v == "" {
			continue
		}
		full := k + "=" + v
		if at, ok := seen[k]; ok {
			out[at] = full
			continue
		}
		seen[k] = len(out)
		out = append(out, full)
	}
	return out
}

// Manager creates and tracks PTY sessions.
type Manager struct {
	mu       sync.Mutex
	sessions map[string]*session

	scrollback int
	killGrace  time.Duration
}

// NewManager returns a manager with default scrollback and kill grace.
func NewManager() *Manager {
	return &Manager{
		sessions:   map[string]*session{},
		scrollback: DefaultScrollbackBytes,
		killGrace:  DefaultKillGrace,
	}
}

// Create spawns a new PTY session. It fails if the id already exists.
func (m *Manager) Create(spec Spec) (*SessionInfo, error) {
	if spec.ID == "" {
		return nil, errors.New("pty: id is required")
	}
	dir := spec.Dir
	if dir == "" {
		dir = os.Getenv("HOME")
	}
	if _, err := os.Stat(dir); err != nil { //nolint:gosec // G703: the working directory is operator-provided by design
		return nil, fmt.Errorf("pty: working directory %q: %w", dir, err)
	}
	command := spec.Command
	if command == "" {
		return nil, errors.New("pty: command is required")
	}

	cols, rows := spec.Cols, spec.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}

	cmd := exec.Command("bash", "-l", "-c", bootstrapCommand(command)) //nolint:gosec // G204: the operator-configured agent command is what this runtime exists to run
	cmd.Dir = dir
	cmd.Env = envMap(os.Environ(), spec.Env)

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: clampUint16(cols),
		Rows: clampUint16(rows),
	}) //nolint:gosec // G204: running the operator-configured agent command is the purpose of this runtime
	if err != nil {
		return nil, fmt.Errorf("pty: start %q: %w", command, err)
	}

	s := newSession(SessionInfo{
		ID:      spec.ID,
		Name:    spec.Name,
		Dir:     dir,
		Command: command,
		Status:  StatusRunning,
		PID:     cmd.Process.Pid,
		Started: time.Now(),
		Size:    fmt.Sprintf("%dx%d", cols, rows),
	}, cmd, ptmx, m.scrollback)

	m.mu.Lock()
	if _, dup := m.sessions[spec.ID]; dup {
		m.mu.Unlock()
		_ = ptmx.Close()
		return nil, fmt.Errorf("pty: session %s already exists", spec.ID)
	}
	m.sessions[spec.ID] = s
	m.mu.Unlock()

	go s.pump()
	go s.reap(m.killGrace)

	info := s.snapshot()
	return &info, nil
}

// List returns every session sorted by start time.
func (m *Manager) List() []SessionInfo {
	m.mu.Lock()
	list := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		list = append(list, s)
	}
	m.mu.Unlock()

	out := make([]SessionInfo, 0, len(list))
	for _, s := range list {
		out = append(out, s.snapshot())
	}
	sortSessions(out)
	return out
}

// Get returns one session by id.
func (m *Manager) Get(id string) (*SessionInfo, error) {
	s, ok := m.lookup(id)
	if !ok {
		return nil, ErrNotFound
	}
	info := s.snapshot()
	return &info, nil
}

// Write sends raw bytes to the session's terminal (the send-keys path).
func (m *Manager) Write(id string, data []byte) error {
	s, ok := m.lookup(id)
	if !ok {
		return ErrNotFound
	}
	return s.write(data)
}

// Resize sets the session window size.
func (m *Manager) Resize(id string, cols, rows int) error {
	s, ok := m.lookup(id)
	if !ok {
		return ErrNotFound
	}
	return s.resize(cols, rows)
}

// Capture returns up to maxBytes of recent output from the ring buffer.
func (m *Manager) Capture(id string, maxBytes int) ([]byte, error) {
	s, ok := m.lookup(id)
	if !ok {
		return nil, ErrNotFound
	}
	return s.capture(maxBytes), nil
}

// Kill terminates a session: SIGTERM, grace period, then SIGKILL. The session
// record stays listed with StatusExited until Forget removes it.
func (m *Manager) Kill(id string) error {
	s, ok := m.lookup(id)
	if !ok {
		return ErrNotFound
	}
	return s.kill(m.killGrace)
}

// Forget drops an exited session's record entirely.
func (m *Manager) Forget(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		return ErrNotFound
	}
	if s.isRunning() {
		return errors.New("pty: session still running")
	}
	delete(m.sessions, id)
	return nil
}

func (m *Manager) lookup(id string) (*session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	return s, ok
}

// clampUint16 guards the pty window-size conversion.
func clampUint16(v int) uint16 {
	if v < 1 {
		return 1
	}
	if v > 0xFFFF {
		return 0xFFFF
	}
	return uint16(v)
}

func sortSessions(list []SessionInfo) {
	// insertion sort keeps this dependency-free; session counts are tiny
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].Started.Before(list[j-1].Started); j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
}

// CloseAll kills every session; used on serve shutdown so children never
// outlive the process that owns their PTY masters.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.Kill(id)
	}
}
