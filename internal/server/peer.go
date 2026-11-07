package server

import (
	"context"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
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

// inHolderTree reports whether pid is, or descends from, the process a live
// pty holder runs (the session's agent). Processes the agent spawns are such
// descendants.
func (s *Server) inHolderTree(pid int) bool {
	if s.pty == nil {
		return false
	}
	return descendsFrom(pid, holderRoots(s.pty.List(), heldByHolder), parentPID)
}

// holderRoots is the PIDs of the running sessions in infos that held
// confirms. A record's PID (the holder's child) can outlive its holder and be
// reused by any process, so a PID counts only while a holder still runs it.
func holderRoots(infos []ptyruntime.SessionInfo, held func(int) bool) map[int]bool {
	roots := map[int]bool{}
	for _, info := range infos {
		if info.PID > 1 && info.Status == ptyruntime.StatusRunning && held(info.PID) {
			roots[info.PID] = true
		}
	}
	return roots
}

// heldByHolder reports whether pid is alive and its parent is a live
// `aiman pty hold` process.
func heldByHolder(pid int) bool {
	pp, ok := parentPID(pid)
	return ok && isHolderProc(pp)
}

// isHolderProc reports whether pid is a live `aiman pty hold` process, from
// /proc/<pid>/cmdline.
func isHolderProc(pid int) bool {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	return err == nil && isHolderCmdline(raw)
}

// isHolderCmdline reports whether a NUL-separated command line is a holder's:
// `<binary> pty hold ... --id <id>`, as ptyhold.Spawn starts it. The binary's
// name is not checked, so a renamed aiman still counts.
func isHolderCmdline(raw []byte) bool {
	args := strings.Split(strings.TrimRight(string(raw), "\x00"), "\x00")
	return len(args) >= 3 && args[1] == "pty" && args[2] == "hold" && slices.Contains(args[3:], "--id")
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

// sessionScope is what a session call may reach. The zero caller is a session
// with no usable identity, which reaches nothing.
type sessionScope struct {
	caller string
	rows   map[string]domain.Session
	pty    PTYRuntime
}

// sessionScopeOf returns the scope of a session call, and false for an operator
// call. It is the only place the scope check reads who is calling.
//
// A caller must be a known session row or pty: a holder peer that names none has
// no identity, and neither does a name nothing on this host answers to. A failed
// read of the rows also leaves no identity, so the check fails closed.
func (s *Server) sessionScopeOf(ctx context.Context, req Request) (sessionScope, bool) {
	if !s.isSessionCall(ctx, req) {
		return sessionScope{}, false
	}
	list, err := s.loadSessions(ctx)
	if err != nil {
		log.Printf("session scope: reading sessions: %v", err)
		return sessionScope{}, true
	}
	scope := sessionScope{rows: make(map[string]domain.Session, len(list)), pty: s.pty}
	for _, sess := range list {
		scope.rows[sess.ID] = sess
	}
	if scope.exists(req.Caller) {
		scope.caller = req.Caller
	}
	return scope, true
}

// plainID reports whether id can address one pty. The runtime joins an id into a
// path, so an id holding a separator could name another session's directory.
func plainID(id string) bool {
	return id != "" && !strings.ContainsAny(id, `/\`)
}

// exists reports whether id is a stored session or a pty on this host.
func (c sessionScope) exists(id string) bool {
	if !plainID(id) {
		return false
	}
	if _, ok := c.rows[id]; ok {
		return true
	}
	if c.pty == nil {
		return false
	}
	_, err := c.pty.Get(id)
	return err == nil
}

// parentOf returns id's parent: its row's, else what its holder recorded, which
// covers the seconds before the creator saves the row.
func (c sessionScope) parentOf(id string) string {
	if row, ok := c.rows[id]; ok && row.ParentID != "" {
		return row.ParentID
	}
	if c.pty == nil {
		return ""
	}
	info, err := c.pty.Get(id)
	if err != nil {
		return ""
	}
	return info.ParentID
}

// isHelperID reports whether id is named as a helper pty of the caller, as in
// the skill's "${AIMAN_ID}-run". It does not ask whether id has a row: helpers
// are listed like any live pty, and the dashboard can register them.
func (c sessionScope) isHelperID(id string) bool {
	return strings.HasPrefix(id, c.caller+"-")
}

// covers reports whether the caller may act on target: itself, a descendant, or
// one of its helper ptys.
func (c sessionScope) covers(target string) bool {
	if c.caller == "" || !plainID(target) {
		return false
	}
	if target == c.caller || c.isHelperID(target) {
		return true
	}
	// A depth bound, because a corrupt row could form a cycle.
	cur := target
	for range maxProcDepth {
		cur = c.parentOf(cur)
		if cur == "" {
			return false
		}
		if cur == c.caller {
			return true
		}
	}
	return false
}

// mayCreate reports whether the caller may create pty id under parent. An id
// already in use must be in scope too, because a create replaces an exited
// holder's directory.
func (c sessionScope) mayCreate(id, parent string) bool {
	if c.caller == "" || !plainID(id) {
		return false
	}
	if c.exists(id) && !c.covers(id) {
		return false
	}
	if parent != "" {
		return parent == c.caller
	}
	return c.isHelperID(id)
}

// refuseOutOfScope returns a session_scope response when req is a session call
// that may not act on target. Operator calls always pass.
func (s *Server) refuseOutOfScope(ctx context.Context, req Request, target string) (Response, bool) {
	scope, isSession := s.sessionScopeOf(ctx, req)
	if !isSession || scope.covers(target) {
		return Response{}, false
	}
	return scopeRefusal(req.ID, scope, "target "+target), true
}

// refuseCreateOutOfScope is refuseOutOfScope for pty.create.
func (s *Server) refuseCreateOutOfScope(ctx context.Context, req Request, id, parent string) (Response, bool) {
	scope, isSession := s.sessionScopeOf(ctx, req)
	if !isSession || scope.mayCreate(id, parent) {
		return Response{}, false
	}
	return scopeRefusal(req.ID, scope, "pty "+id), true
}

func scopeRefusal(reqID string, scope sessionScope, what string) Response {
	if scope.caller == "" {
		return errResp(reqID, CodeSessionScope, "this process has no session identity, so it may not act on "+what)
	}
	return errResp(reqID, CodeSessionScope, what+" is outside the tree of session "+scope.caller)
}
