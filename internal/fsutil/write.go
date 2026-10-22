// Package fsutil holds small local filesystem helpers.
package fsutil

import (
	"bytes"
	"os"
	"path/filepath"
)

// WriteIfChanged writes content to path only when the existing bytes differ,
// so unchanged files keep their mtime and agents' cached prompt prefixes stay
// valid. Writes go through a temp file and rename; mode applies to new content.
func WriteIfChanged(path string, content []byte, mode os.FileMode) (changed bool, err error) {
	if old, rerr := os.ReadFile(path); rerr == nil && bytes.Equal(old, content) {
		return false, nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return false, err
	}
	name := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(name)
		}
	}()
	if _, err = tmp.Write(content); err != nil {
		_ = tmp.Close()
		return false, err
	}
	if err = tmp.Close(); err != nil {
		return false, err
	}
	if err = os.Chmod(name, mode); err != nil {
		return false, err
	}
	if err = os.Rename(name, path); err != nil {
		return false, err
	}
	return true, nil
}
