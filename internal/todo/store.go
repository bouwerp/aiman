package todo

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/agenthook"
	"github.com/bouwerp/aiman/internal/domain"
)

// ErrInvalid wraps every rejection of a mutated list (empty text, bad state).
var ErrInvalid = errors.New("invalid to-do list")

const maxTombstones = 50

// Store reads and mutates per-session to-do lists. Serve is the only writer.
type Store interface {
	Get(sessionID string) (domain.TodoList, error)
	Mutate(sessionID string, author domain.TodoAuthor, fn func(*domain.TodoList) error) (domain.TodoList, Change, error)
}

// Tombstone records a removed item so it can be recognised if it reappears.
type Tombstone struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	RemovedAt time.Time `json:"removed_at"`
}

type fileData struct {
	List       domain.TodoList `json:"list"`
	NextID     int             `json:"next_id"`
	Tombstones []Tombstone     `json:"tombstones,omitempty"`
}

// Files stores each list as <root>/<session id>.json.
type Files struct {
	root string
	now  func() time.Time

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewFiles returns a Files store rooted at root.
func NewFiles(root string) *Files {
	return &Files{root: root, now: time.Now, locks: map[string]*sync.Mutex{}}
}

var _ Store = (*Files)(nil)

func (f *Files) lock(id string) *sync.Mutex {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.locks[id]
	if m == nil {
		m = &sync.Mutex{}
		f.locks[id] = m
	}
	return m
}

func (f *Files) path(sessionID string) (string, string, error) {
	id := agenthook.SafeSessionID(sessionID)
	if id == "" {
		return "", "", fmt.Errorf("invalid session id %q", sessionID)
	}
	return id, filepath.Join(f.root, id+".json"), nil
}

func (f *Files) load(sessionID, path string) (fileData, error) {
	d := fileData{List: domain.TodoList{SessionID: sessionID}, NextID: 1}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return d, fmt.Errorf("decode %s: %w", path, err)
	}
	d.List.SessionID = sessionID
	if d.NextID < 1 {
		d.NextID = 1
	}
	return d, nil
}

// Get returns the session's list, or an empty list when none exists.
func (f *Files) Get(sessionID string) (domain.TodoList, error) {
	id, path, err := f.path(sessionID)
	if err != nil {
		return domain.TodoList{}, err
	}
	m := f.lock(id)
	m.Lock()
	defer m.Unlock()
	d, err := f.load(sessionID, path)
	return d.List, err
}

// Mutate applies fn to a copy of the list, normalises and stamps the result,
// and writes it when anything changed.
func (f *Files) Mutate(sessionID string, author domain.TodoAuthor, fn func(*domain.TodoList) error) (domain.TodoList, Change, error) {
	id, path, err := f.path(sessionID)
	if err != nil {
		return domain.TodoList{}, Change{}, err
	}
	m := f.lock(id)
	m.Lock()
	defer m.Unlock()

	d, err := f.load(sessionID, path)
	if err != nil {
		return domain.TodoList{}, Change{}, err
	}
	prev := d.List
	next := prev
	next.Items = append([]domain.TodoItem(nil), prev.Items...)
	next.NotInAgent = append([]string(nil), prev.NotInAgent...)
	if err := fn(&next); err != nil {
		return prev, Change{}, err
	}
	next.SessionID = sessionID

	if err := validate(next); err != nil {
		return prev, Change{}, err
	}
	for i := range next.Items {
		if next.Items[i].ID == "" {
			next.Items[i].ID = "t" + strconv.Itoa(d.NextID)
			d.NextID++
		}
	}
	if err := checkUniqueIDs(next); err != nil {
		return prev, Change{}, err
	}
	demoteExtraActive(prev, &next)
	now := f.now()
	stamp(prev, &next, author, now)

	ops := Diff(prev, next)
	if len(ops) == 0 {
		return prev, Change{Author: author, Rev: prev.Rev}, nil
	}
	d.Tombstones = addTombstones(d.Tombstones, prev, next, now)
	next.Rev = prev.Rev + 1
	next.UpdatedAt = now
	d.List = next
	if err := writeAtomic(f.root, path, d); err != nil {
		return prev, Change{}, err
	}
	return next, Change{Author: author, Ops: ops, Rev: next.Rev}, nil
}

func validate(l domain.TodoList) error {
	for _, it := range l.Items {
		if strings.TrimSpace(it.Text) == "" {
			return fmt.Errorf("%w: item %q has empty text", ErrInvalid, it.ID)
		}
		if !it.State.Valid() {
			return fmt.Errorf("%w: item %q has state %q", ErrInvalid, it.ID, it.State)
		}
	}
	return nil
}

func checkUniqueIDs(l domain.TodoList) error {
	seen := map[string]bool{}
	for _, it := range l.Items {
		if seen[it.ID] {
			return fmt.Errorf("%w: duplicate id %q", ErrInvalid, it.ID)
		}
		seen[it.ID] = true
	}
	return nil
}

// demoteExtraActive leaves at most one active item: the last whose state
// changed in this call, or the last active one when none changed.
func demoteExtraActive(prev domain.TodoList, next *domain.TodoList) {
	before := map[string]domain.TodoState{}
	for _, it := range prev.Items {
		before[it.ID] = it.State
	}
	keep, keepChanged := -1, false
	for i, it := range next.Items {
		if it.State != domain.TodoActive {
			continue
		}
		changed := before[it.ID] != domain.TodoActive
		if keep < 0 || changed || !keepChanged {
			keep, keepChanged = i, changed
		}
	}
	for i := range next.Items {
		if i != keep && next.Items[i].State == domain.TodoActive {
			next.Items[i].State = domain.TodoPending
		}
	}
}

// stamp sets UpdatedBy/UpdatedAt. New or retexted items belong to the author.
// A state or position change does too, except that the agent mirroring its own
// progress leaves a user's or aiman's item attributed as before.
func stamp(prev domain.TodoList, next *domain.TodoList, author domain.TodoAuthor, now time.Time) {
	old := map[string]int{}
	for i, it := range prev.Items {
		old[it.ID] = i
	}
	for i := range next.Items {
		it := &next.Items[i]
		pi, ok := old[it.ID]
		if !ok {
			it.UpdatedBy, it.UpdatedAt = author, now
			continue
		}
		p := prev.Items[pi]
		it.UpdatedBy, it.UpdatedAt = p.UpdatedBy, p.UpdatedAt
		switch {
		case p.Text != it.Text:
			it.UpdatedBy, it.UpdatedAt = author, now
		case p.State != it.State || pi != i:
			if author == domain.AuthorAgent && p.UpdatedBy != domain.AuthorAgent {
				continue
			}
			it.UpdatedBy, it.UpdatedAt = author, now
		}
	}
}

func addTombstones(ts []Tombstone, prev, next domain.TodoList, now time.Time) []Tombstone {
	kept := map[string]bool{}
	for _, it := range next.Items {
		kept[it.ID] = true
	}
	for _, it := range prev.Items {
		if !kept[it.ID] {
			ts = append(ts, Tombstone{ID: it.ID, Text: it.Text, RemovedAt: now})
		}
	}
	if len(ts) > maxTombstones {
		ts = append([]Tombstone(nil), ts[len(ts)-maxTombstones:]...)
	}
	return ts
}

func writeAtomic(dir, path string, d fileData) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".todo-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
