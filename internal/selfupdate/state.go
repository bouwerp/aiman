package selfupdate

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Release is what one poll learns about the latest release.
type Release struct {
	Tag         string    `json:"tag,omitempty"`
	Ready       bool      `json:"ready,omitempty"`
	ETag        string    `json:"etag,omitempty"`
	PublishedAt time.Time `json:"published_at,omitzero"`
}

// AttemptStatus is how an install attempt ended, or started while running.
type AttemptStatus string

const (
	AttemptStarted AttemptStatus = "started"
	AttemptOK      AttemptStatus = "ok"
	AttemptFailed  AttemptStatus = "failed"
)

// Attempt records one install. It is saved as started before the script runs,
// so the serve process alive when the script ends can tell whether it took.
type Attempt struct {
	At     time.Time     `json:"at"`
	From   string        `json:"from"`
	To     string        `json:"to"`
	PID    int           `json:"pid,omitempty"`
	Status AttemptStatus `json:"status"`
	Exit   int           `json:"exit,omitempty"`
	Error  string        `json:"error,omitempty"`
}

// Result is what the detached wrapper writes when the script ends, whatever
// its exit. The JSON names are the ones the wrapper prints.
type Result struct {
	Tag  string    `json:"tag"`
	Exit int       `json:"exit"`
	At   time.Time `json:"at"`
}

// State is the host update file (~/.aiman/host-update.json). The serve probe
// passes it to the laptop verbatim, so its JSON names are a contract.
type State struct {
	CheckedAt time.Time `json:"checked_at,omitzero"`
	ETag      string    `json:"etag,omitempty"`
	Latest    Release   `json:"latest,omitzero"`
	Notified  string    `json:"notified,omitempty"`
	Attempt   *Attempt  `json:"attempt"`
}

// LoadState reads path. A missing file is a zero State and no error.
func LoadState(path string) (State, error) {
	var s State
	b, err := os.ReadFile(path) //nolint:gosec // G304: the caller owns the state path
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	err = json.Unmarshal(b, &s)
	return s, err
}

// Save writes the state on one line through a synced temp file renamed over
// path, so a reader sees the old or the new file whole.
func (s State) Save(path string) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeAtomic(path, append(b, '\n'))
}

func writeAtomic(path string, b []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(b); err != nil {
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

// ReadResult reads the wrapper's Result. It reports false when the file is
// absent, does not parse, or lacks a tag or time; the error is only for a file
// that exists but cannot be read.
func ReadResult(path string) (Result, bool, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: the caller owns the result path
	if errors.Is(err, fs.ErrNotExist) {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, err
	}
	var r Result
	if json.Unmarshal(b, &r) != nil || r.Tag == "" || r.At.IsZero() {
		return Result{}, false, nil
	}
	return r, true, nil
}
