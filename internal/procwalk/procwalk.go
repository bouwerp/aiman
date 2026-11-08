// Package procwalk reads /proc to tell whether a process runs inside an aiman
// pty holder's tree. Serve uses it to classify socket peers, and the gateway
// uses it to refuse to start inside a session's terminal.
package procwalk

import (
	"os"
	"slices"
	"strconv"
	"strings"
)

// MaxDepth bounds every ancestor walk, so a PID loop cannot hang the caller.
const MaxDepth = 64

// ParentPID reads pid's parent from /proc/<pid>/stat.
func ParentPID(pid int) (int, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	// The command name sits in parentheses and may hold spaces; the state and
	// parent PID follow the last ')'.
	s := string(raw)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 2 {
		return 0, false
	}
	pp, err := strconv.Atoi(f[1])
	return pp, err == nil
}

// Cmdline reads pid's NUL-separated command line from /proc/<pid>/cmdline.
func Cmdline(pid int) ([]byte, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return nil, false
	}
	return raw, true
}

// IsHolderCmdline reports whether a NUL-separated command line is a holder's:
// `<binary> pty hold ... --id <id>`, as ptyhold.Spawn starts it. The binary's
// name is not checked, so a renamed aiman still counts.
func IsHolderCmdline(raw []byte) bool {
	args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	return len(args) >= 3 && args[1] == "pty" && args[2] == "hold" && slices.Contains(args[3:], "--id")
}

// InHolderTree reports whether pid or one of its ancestors is a holder
// process. The walk stops at pid 1, at an unreadable parent, or after
// MaxDepth levels, and each of those ends false.
func InHolderTree(pid int, parent func(int) (int, bool), cmdline func(int) ([]byte, bool)) bool {
	for range MaxDepth {
		if pid <= 1 {
			return false
		}
		if raw, ok := cmdline(pid); ok && IsHolderCmdline(raw) {
			return true
		}
		pp, ok := parent(pid)
		if !ok {
			return false
		}
		pid = pp
	}
	return false
}

// Self reports whether this process runs inside a holder's tree.
func Self() bool {
	return InHolderTree(os.Getpid(), ParentPID, Cmdline)
}
