package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// State is serve's record of one review session, kept in
// <dir>/<session id>.json (dir is ~/.aiman/reviews on a host).
type State struct {
	// Ref is the PR under review, "owner/repo#n".
	Ref string `json:"ref"`
	// HeadSHA is the PR head the worktree was staged from.
	HeadSHA string `json:"head_sha"`
	// Revealed is set once the PR description has been written.
	Revealed bool `json:"revealed"`
	// Single records that the review went ahead with one reviewer.
	Single bool `json:"single"`
}

// statePath is the state file of session id in dir. The id must be a plain
// file name.
func statePath(dir, id string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return "", fmt.Errorf("review state: session id %q is not a file name", id)
	}
	return filepath.Join(dir, id+".json"), nil
}

// LoadState reads the state of session id. A missing file is os.ErrNotExist.
func LoadState(dir, id string) (State, error) {
	path, err := statePath(dir, id)
	if err != nil {
		return State{}, err
	}
	raw, err := os.ReadFile(path) //nolint:gosec // path is dir plus a validated file name
	if err != nil {
		return State{}, err
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return State{}, fmt.Errorf("review state %s: %w", id, err)
	}
	return st, nil
}

// SaveState writes the state of session id atomically.
func SaveState(dir, id string, st State) error {
	path, err := statePath(dir, id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}
