package images

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// Store is images.json: one record per platform manifest digest, written
// atomically under the flock images.lock so another process on the host
// never reads half a file.
type Store struct {
	dir string
	mu  sync.Mutex
}

// ErrNoRecord is a digest the store does not hold.
var ErrNoRecord = errors.New("no image record")

// errSkip, returned from an Update function, leaves the store as it is.
var errSkip = errors.New("skip")

// NewStore opens the store in dir (~/.aiman/harness).
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path() string { return filepath.Join(s.dir, "images.json") }

type storeFile struct {
	Images map[string]Record `json:"images"`
}

func (s *Store) read() (map[string]Record, error) {
	b, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Record{}, nil
	}
	if err != nil {
		return nil, err
	}
	var f storeFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("read %s: %w", s.path(), err)
	}
	if f.Images == nil {
		f.Images = map[string]Record{}
	}
	return f.Images, nil
}

func (s *Store) write(all map[string]Record) error {
	b, err := json.MarshalIndent(storeFile{Images: all}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".images-*.json")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), s.path())
}

func (s *Store) locked(fn func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := lockFile(filepath.Join(s.dir, "images.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}

// Get returns the record for digest, or ErrNoRecord.
func (s *Store) Get(digest string) (Record, error) {
	var rec Record
	err := s.locked(func() error {
		all, err := s.read()
		if err != nil {
			return err
		}
		r, ok := all[digest]
		if !ok {
			return ErrNoRecord
		}
		rec = r
		return nil
	})
	return rec, err
}

// List returns every record, newest first.
func (s *Store) List() ([]Record, error) {
	var out []Record
	err := s.locked(func() error {
		all, err := s.read()
		if err != nil {
			return err
		}
		for _, r := range all {
			out = append(out, r)
		}
		return nil
	})
	slices.SortFunc(out, func(a, b Record) int { return b.At.Compare(a.At) })
	return out, err
}

// Update applies fn to the record for digest and writes it back. fn gets a
// zero record and ok false when the store holds none. An error from fn
// writes nothing; errSkip is then no error.
func (s *Store) Update(digest string, fn func(r *Record, ok bool) error) (Record, error) {
	var rec Record
	err := s.locked(func() error {
		all, err := s.read()
		if err != nil {
			return err
		}
		r, ok := all[digest]
		if err := fn(&r, ok); err != nil {
			rec = r
			return err
		}
		r.Digest = digest
		all[digest] = r
		rec = r
		return s.write(all)
	})
	if errors.Is(err, errSkip) {
		err = nil
	}
	return rec, err
}
