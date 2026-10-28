// Package ptyfake is an in-memory stand-in for the PTY runtime that serve
// owns. It runs no process: sessions are added by the test, every write and
// kill is recorded, and captures return a scripted screen.
package ptyfake

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/ptyruntime"
)

// Write is one Write call.
type Write struct {
	ID   string
	Data string
}

// FakePTY implements server.PTYRuntime. The zero value is not usable; call New.
type FakePTY struct {
	mu       sync.Mutex
	sessions map[string]ptyruntime.SessionInfo
	screens  map[string]string
	screen   string
	writes   []Write
	kills    []string
	forgets  []string
	creates  []ptyruntime.Spec
}

// New returns an empty runtime.
func New() *FakePTY {
	return &FakePTY{sessions: map[string]ptyruntime.SessionInfo{}, screens: map[string]string{}}
}

// Add registers a session. An empty status is running and an empty name is
// the id.
func (f *FakePTY) Add(info ptyruntime.SessionInfo) {
	if info.Status == "" {
		info.Status = ptyruntime.StatusRunning
	}
	if info.Name == "" {
		info.Name = info.ID
	}
	if info.Started.IsZero() {
		info.Started = time.Now().UTC()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[info.ID] = info
}

// SetScreen sets what captures of one session return.
func (f *FakePTY) SetScreen(id, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.screens[id] = text
}

// SetDefaultScreen sets what captures return for a session without its own
// screen.
func (f *FakePTY) SetDefaultScreen(text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.screen = text
}

// Writes returns a copy of every Write call, oldest first.
func (f *FakePTY) Writes() []Write {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Write(nil), f.writes...)
}

// Kills returns the ids Kill was called with.
func (f *FakePTY) Kills() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.kills...)
}

// Forgets returns the ids Forget was called with.
func (f *FakePTY) Forgets() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.forgets...)
}

// Creates returns the specs Create was called with.
func (f *FakePTY) Creates() []ptyruntime.Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ptyruntime.Spec(nil), f.creates...)
}

func (f *FakePTY) Create(spec ptyruntime.Spec) (*ptyruntime.SessionInfo, error) {
	if spec.ID == "" {
		return nil, fmt.Errorf("pty: id is required")
	}
	f.mu.Lock()
	f.creates = append(f.creates, spec)
	if info, ok := f.sessions[spec.ID]; ok && info.Status == ptyruntime.StatusRunning {
		f.mu.Unlock()
		return nil, fmt.Errorf("pty: session %s already exists", spec.ID)
	}
	f.mu.Unlock()
	f.Add(ptyruntime.SessionInfo{ID: spec.ID, Name: spec.Name, Dir: spec.Dir, Command: spec.Command})
	return f.Get(spec.ID)
}

func (f *FakePTY) List() []ptyruntime.SessionInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]ptyruntime.SessionInfo, 0, len(f.sessions))
	for _, info := range f.sessions {
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (f *FakePTY) Get(id string) (*ptyruntime.SessionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, ok := f.sessions[id]
	if !ok {
		return nil, ptyruntime.ErrNotFound
	}
	return &info, nil
}

func (f *FakePTY) Write(id string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sessions[id]; !ok {
		return ptyruntime.ErrNotFound
	}
	f.writes = append(f.writes, Write{ID: id, Data: string(data)})
	return nil
}

func (f *FakePTY) Resize(id string, _, _ int) error {
	_, err := f.Get(id)
	return err
}

func (f *FakePTY) Capture(id string, _ int) ([]byte, error) {
	s, err := f.CaptureScreen(id)
	return []byte(s), err
}

func (f *FakePTY) CaptureScreen(id string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.sessions[id]; !ok {
		return "", ptyruntime.ErrNotFound
	}
	if s, ok := f.screens[id]; ok {
		return s, nil
	}
	return f.screen, nil
}

func (f *FakePTY) CaptureFrame(id string) (view, all string, err error) {
	s, err := f.CaptureScreen(id)
	return s, s, err
}

func (f *FakePTY) TakeRedrawn(string) []string { return nil }

func (f *FakePTY) OnAltScreen(string) bool { return false }

// Kill marks the session exited and records the call.
func (f *FakePTY) Kill(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kills = append(f.kills, id)
	info, ok := f.sessions[id]
	if !ok {
		return ptyruntime.ErrNotFound
	}
	info.Status = ptyruntime.StatusExited
	f.sessions[id] = info
	return nil
}

// Forget drops the session and records the call.
func (f *FakePTY) Forget(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgets = append(f.forgets, id)
	if _, ok := f.sessions[id]; !ok {
		return ptyruntime.ErrNotFound
	}
	delete(f.sessions, id)
	delete(f.screens, id)
	return nil
}

// Subscribe replays the current screen and returns a channel that stays open
// until unsub.
func (f *FakePTY) Subscribe(id string) ([]byte, <-chan []byte, func(), error) {
	s, err := f.CaptureScreen(id)
	if err != nil {
		return nil, nil, nil, err
	}
	ch := make(chan []byte)
	var once sync.Once
	return []byte(s), ch, func() { once.Do(func() { close(ch) }) }, nil
}
