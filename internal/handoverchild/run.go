// Package handoverchild runs a hidden child agent that rewrites a worker
// session's handover from its transcript and proposes memory candidates. The
// mechanical handover serve writes first stays in place whenever the child
// fails.
package handoverchild

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status is a run's lifecycle state.
type Status string

const (
	StatusStarting   Status = "starting"
	StatusRunning    Status = "running"
	StatusComplete   Status = "complete"   // the child wrote the done marker
	StatusIncomplete Status = "incomplete" // the child exited without it
	StatusTimedOut   Status = "timed_out"
	StatusFailed     Status = "failed" // no child was started
)

// Active reports whether the run still has, or is about to have, a child.
func (s Status) Active() bool {
	return s == StatusStarting || s == StatusRunning
}

// Run is one handover child run for one worker session.
type Run struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"` // the worker
	ChildID   string `json:"child_id,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Preferred string `json:"preferred,omitempty"` // target agent on agent_switch
	Reason    string `json:"reason"`
	// LaterReasons are triggers that arrived while this run was active.
	LaterReasons []string `json:"later_reasons,omitempty"`
	Dir          string   `json:"dir"`                  // absolute scratch directory
	Transcript   string   `json:"transcript,omitempty"` // worker AgentSessionPath at start
	Worktree     string   `json:"worktree,omitempty"`
	Group        string   `json:"group,omitempty"`
	IssueKey     string   `json:"issue_key,omitempty"`
	Repo         string   `json:"repo,omitempty"`
	Label        string   `json:"label,omitempty"`
	Abstract     string   `json:"abstract,omitempty"`
	// Mechanical is the full mechanical render, the fallback note and job
	// source when the child fails.
	Mechanical    string `json:"mechanical,omitempty"`
	MechanicalSHA string `json:"mechanical_sha,omitempty"`
	// HeldPointer is a steering note withheld until the run ends.
	HeldPointer     string `json:"held_pointer,omitempty"`
	PointerReleased bool   `json:"pointer_released,omitempty"`
	Applied         bool   `json:"applied,omitempty"` // child handover written to the worker
	// AppliedSHA is the sha256 of the worker file the child's handover became.
	AppliedSHA string    `json:"applied_sha,omitempty"`
	Candidates int       `json:"candidates,omitempty"`
	Status     Status    `json:"status"`
	Error      string    `json:"error,omitempty"`
	CleanedUp  bool      `json:"cleaned_up,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
}

// ErrNotFound means the run id is not in the store.
var ErrNotFound = errors.New("handover run not found")

// Store keeps one JSON file per run.
type Store struct {
	dir string
	mu  sync.Mutex
}

// NewStore returns a store rooted at dir. Put creates dir on first write.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Put writes a run atomically: temp file, then rename.
func (s *Store) Put(r Run) error {
	if !validRunID(r.ID) {
		return fmt.Errorf("invalid run id %q", r.ID)
	}
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".run-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(s.dir, r.ID+".json"))
}

// Get reads one run.
func (s *Store) Get(id string) (Run, error) {
	if !validRunID(id) {
		return Run{}, ErrNotFound
	}
	body, err := os.ReadFile(filepath.Join(s.dir, id+".json"))
	if os.IsNotExist(err) {
		return Run{}, ErrNotFound
	}
	if err != nil {
		return Run{}, err
	}
	var r Run
	if err := json.Unmarshal(body, &r); err != nil {
		return Run{}, fmt.Errorf("decode run %s: %w", id, err)
	}
	return r, nil
}

// Delete removes one run. A missing run is not an error.
func (s *Store) Delete(id string) error {
	if !validRunID(id) {
		return ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Remove(filepath.Join(s.dir, id+".json")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// List returns every run, newest StartedAt first.
func (s *Store) List() ([]Run, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Run
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		r, err := s.Get(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			continue // one corrupt record must not hide the others
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartedAt.Equal(out[j].StartedAt) {
			return out[i].StartedAt.After(out[j].StartedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func validRunID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return false
	}
	return true
}
