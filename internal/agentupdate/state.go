package agentupdate

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// LastUpdate records the most recent update attempt for one agent.
type LastUpdate struct {
	At   time.Time `json:"at"`
	From string    `json:"from,omitempty"`
	To   string    `json:"to,omitempty"`
	OK   bool      `json:"ok"`
	Log  string    `json:"log,omitempty"`
}

// AgentState is what the last probe learned about one agent.
type AgentState struct {
	Agent      string      `json:"agent"`
	Installed  string      `json:"installed,omitempty"`
	Latest     string      `json:"latest,omitempty"`
	Method     Method      `json:"method,omitempty"`
	Path       string      `json:"path,omitempty"`
	CheckedAt  time.Time   `json:"checked_at"`
	Error      string      `json:"error,omitempty"`
	LastUpdate *LastUpdate `json:"last_update,omitempty"`
}

// Outdated reports whether a newer version than the installed one is known.
func (a AgentState) Outdated() bool {
	return a.Installed != "" && a.Latest != "" && CompareVersions(a.Installed, a.Latest) < 0
}

// State is the per-host version file (~/.aiman/agent-versions.json).
type State struct {
	Agents map[string]AgentState `json:"agents"`
	// Notified maps an agent to the latest version a notification was sent
	// for. A new latest version replaces the entry, so the set stays one
	// key per agent.
	Notified map[string]string `json:"notified,omitempty"`
}

// NewState returns an empty state.
func NewState() *State {
	return &State{Agents: map[string]AgentState{}, Notified: map[string]string{}}
}

// LoadState reads path. A missing file is an empty state.
func LoadState(path string) (*State, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: the caller owns the state path
	if errors.Is(err, fs.ErrNotExist) {
		return NewState(), nil
	}
	if err != nil {
		return nil, err
	}
	s := NewState()
	if err := json.Unmarshal(b, s); err != nil {
		return nil, err
	}
	if s.Agents == nil {
		s.Agents = map[string]AgentState{}
	}
	if s.Notified == nil {
		s.Notified = map[string]string{}
	}
	return s, nil
}

// Save writes the state atomically: a temp file in the same directory is
// synced and renamed over path, so readers see the old or the new file whole.
func (s *State) Save(path string) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".agent-versions-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// WasNotified reports whether a notification for (agent, latest) was sent.
func (s *State) WasNotified(agent, latest string) bool {
	return s.Notified[agent] == latest
}

// MarkNotified records (agent, latest) and reports whether it was new.
func (s *State) MarkNotified(agent, latest string) bool {
	if s.WasNotified(agent, latest) {
		return false
	}
	if s.Notified == nil {
		s.Notified = map[string]string{}
	}
	s.Notified[agent] = latest
	return true
}
