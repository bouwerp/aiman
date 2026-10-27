package verify

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

// Status is a review's lifecycle state.
type Status string

const (
	StatusStarting   Status = "starting"
	StatusRunning    Status = "running"
	StatusComplete   Status = "complete"
	StatusIncomplete Status = "incomplete"
	StatusTimedOut   Status = "timed_out"
	StatusFailed     Status = "failed"
	StatusDisabled   Status = "disabled"
)

// Active reports whether the review still has a running reviewer.
func (s Status) Active() bool {
	return s == StatusStarting || s == StatusRunning
}

// Review is one verifier run.
type Review struct {
	ID           string `json:"id"`
	WorkerID     string `json:"worker_id"`
	ReviewerID   string `json:"reviewer_id"`
	Agent        string `json:"agent"`
	Tree         string `json:"tree"`
	Commit       string `json:"commit"`
	BaseRef      string `json:"base_ref"`
	BaseSHA      string `json:"base_sha,omitempty"`
	RepoWorktree string `json:"repo_worktree"`
	Worktree     string `json:"worktree"`
	// Dir is the worker's working directory relative to its worktree; the
	// reviewer runs, and writes its review, in the same place.
	Dir         string    `json:"dir,omitempty"`
	Error       string    `json:"error"`
	Status      Status    `json:"status"`
	Findings    []Finding `json:"findings"`
	Evidence    Evidence  `json:"evidence"`
	Required    int       `json:"required"`
	Suggestions int       `json:"suggestions"`
	CleanedUp   bool      `json:"cleaned_up"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at"`
}

// Passed reports a complete review with no required findings whose test
// command, run by the manager on the snapshot, exited 0. A review with no
// evidence cannot pass.
func (r Review) Passed() bool {
	return r.Status == StatusComplete && r.Required == 0 && r.Evidence.Command != "" && r.Evidence.ExitCode == 0
}

// ErrNotFound means the review id is not in the store.
var ErrNotFound = errors.New("review not found")

// Store keeps one JSON file per review.
type Store struct {
	dir string
	mu  sync.Mutex
}

// NewStore returns a store rooted at dir. Put creates dir on first write.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Put writes a review atomically: temp file, then rename.
func (s *Store) Put(r Review) error {
	if strings.TrimSpace(r.ID) == "" || strings.ContainsAny(r.ID, `/\`) {
		return fmt.Errorf("invalid review id %q", r.ID)
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
	tmp, err := os.CreateTemp(s.dir, ".review-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(s.dir, r.ID+".json"))
}

// Get reads one review.
func (s *Store) Get(id string) (Review, error) {
	body, err := os.ReadFile(filepath.Join(s.dir, id+".json"))
	if os.IsNotExist(err) {
		return Review{}, ErrNotFound
	}
	if err != nil {
		return Review{}, err
	}
	var r Review
	if err := json.Unmarshal(body, &r); err != nil {
		return Review{}, fmt.Errorf("decode review %s: %w", id, err)
	}
	return r, nil
}

// List returns every review, newest StartedAt first.
func (s *Store) List() ([]Review, error) {
	entries, err := os.ReadDir(s.dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Review
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		r, err := s.Get(id)
		if err != nil {
			return nil, err
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
