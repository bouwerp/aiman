package cgroup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// Overrides keeps the limits the operator set for single sessions, one JSON
// file per session id under Dir, so a restarted terminal gets them again.
type Overrides struct {
	Dir string
}

func (o *Overrides) path(id string) (string, bool) {
	if o == nil || o.Dir == "" || id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return "", false
	}
	return filepath.Join(o.Dir, id+".json"), true
}

// Get returns the session's override; zero when it has none.
func (o *Overrides) Get(id string) Limits {
	var l Limits
	if p, ok := o.path(id); ok {
		if raw, err := os.ReadFile(p); err == nil { //nolint:gosec // G304: path built from a checked id under Dir
			_ = json.Unmarshal(raw, &l)
		}
	}
	return l
}

// Set stores l as the session's override, replacing the fields l names and
// keeping the rest. It returns the stored override.
func (o *Overrides) Set(id string, l Limits) (Limits, error) {
	p, ok := o.path(id)
	if !ok {
		return Limits{}, errors.New("cgroup: no override store for this session id")
	}
	merged := o.Get(id).Merge(l)
	raw, err := json.Marshal(merged)
	if err != nil {
		return Limits{}, err
	}
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return Limits{}, err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return Limits{}, err
	}
	return merged, os.Rename(tmp, p)
}

// Remove forgets the session's override.
func (o *Overrides) Remove(id string) {
	if p, ok := o.path(id); ok {
		_ = os.Remove(p)
	}
}
