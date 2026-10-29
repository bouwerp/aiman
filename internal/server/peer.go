package server

import (
	"context"
	"os"
	"strconv"
	"strings"
)

type peerPIDKey struct{}

// withPeerPID records the connection's peer PID on ctx when it is known.
func withPeerPID(ctx context.Context, pid int, ok bool) context.Context {
	if !ok {
		return ctx
	}
	return context.WithValue(ctx, peerPIDKey{}, pid)
}

// maxProcDepth bounds the ancestor walk.
const maxProcDepth = 64

// parentPID reads pid's parent from /proc/<pid>/stat.
func parentPID(pid int) (int, bool) {
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

// descendsFrom reports whether pid is one of roots or has one as an ancestor.
func descendsFrom(pid int, roots map[int]bool, parent func(int) (int, bool)) bool {
	for range maxProcDepth {
		if pid <= 1 {
			return false
		}
		if roots[pid] {
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

// inHolderTree reports whether pid is, or descends from, a live pty holder.
// Processes a session's agent spawns are such descendants.
func (s *Server) inHolderTree(pid int) bool {
	if s.pty == nil {
		return false
	}
	roots := map[int]bool{}
	for _, info := range s.pty.List() {
		if info.PID > 1 {
			roots[info.PID] = true
		}
	}
	return descendsFrom(pid, roots, parentPID)
}

// isSessionCall reports whether req came from a session. A request naming a
// caller is one; so is a request with no caller made by a process inside a pty
// holder, because an agent can clear AIMAN_ID. An unknown peer counts as the
// operator.
func (s *Server) isSessionCall(ctx context.Context, req Request) bool {
	if req.Caller != "" {
		return true
	}
	pid, ok := ctx.Value(peerPIDKey{}).(int)
	if !ok {
		return false
	}
	check := s.procInSession
	if check == nil {
		check = s.inHolderTree
	}
	return check(pid)
}
