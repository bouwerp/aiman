package cgroup

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// readValue reads a single-number cgroup file such as memory.current or
// memory.high. "max" is Infinity; a missing or unreadable file is 0, false.
func readValue(dir, file string) (int64, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, file)) //nolint:gosec // G304: a cgroup control file under a directory this package built
	if err != nil {
		return 0, false
	}
	return parseValue(string(raw))
}

func parseValue(raw string) (int64, bool) {
	s := strings.TrimSpace(raw)
	if s == "max" {
		return Infinity, true
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// UserSliceMemoryHigh is the soft memory limit of the whole user slice:
// Infinity when it has none, 0 when it cannot be read.
func (s *Systemd) UserSliceMemoryHigh() int64 {
	v, _ := readValue(s.UserSliceDir(), "memory.high")
	return v
}
