package tools

import (
	"bytes"
	"crypto/sha256"
	_ "embed" // the debugpy-python wrapper
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DebugpyPython is the wrap_env wrapper: an interpreter under debugpy on
// the run's debug address (debugpy-python.sh).
//
//go:embed debugpy-python.sh
var DebugpyPython []byte

// DebugpyPythonFile is the wrapper's file name.
const DebugpyPythonFile = "debugpy-python"

// EnsureFile writes content as name, mode 0755, in a directory of its own
// under Dir named after its digest, and returns that directory. A file whose
// bytes changed is written again.
func (s *Store) EnsureFile(name string, content []byte) (string, error) {
	if name == "" || name != filepath.Base(name) || strings.HasPrefix(name, ".") {
		return "", fmt.Errorf("tool file name %q", name)
	}
	sum := sha256.Sum256(content)
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := filepath.Join(s.Dir, name+"-"+hex.EncodeToString(sum[:6]))
	target := filepath.Join(dir, name)
	if b, err := os.ReadFile(target); err == nil && bytes.Equal(b, content) { //nolint:gosec // G304: serve's own tools directory
		if fi, err := os.Stat(target); err == nil && fi.Mode().Perm()&0o111 != 0 {
			return dir, nil
		}
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return "", err
	}
	scratch, err := os.MkdirTemp(s.Dir, ".file-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	if err := os.WriteFile(filepath.Join(scratch, name), content, 0o755); err != nil { //nolint:gosec // G306: a script the containers run as the host uid
		return "", err
	}
	if err := os.Chmod(scratch, 0o755); err != nil { //nolint:gosec // G302: containers run as the host uid and read it
		return "", err
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.Rename(scratch, dir); err != nil {
		return "", err
	}
	return dir, nil
}
