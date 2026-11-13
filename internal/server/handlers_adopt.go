package server

import (
	"context"
	"os"

	"github.com/bouwerp/aiman/internal/cgroup"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/procwalk"
	"github.com/bouwerp/aiman/internal/ptyruntime"
)

// scopeProcs lists the running processes the session key pays for: each of
// its running holders (its own terminal and its helper terminals), what they
// run and every descendant, plus processes that left those trees (a daemon
// reparented to the user manager) but still sit in serve's own cgroup, start
// with AIMAN_ID equal to key and descend from no other session's holder.
func (s *Server) scopeProcs(ctx context.Context, key string) []int {
	if s.pty == nil {
		return nil
	}
	held := s.holderHeld
	if held == nil {
		held = heldByHolder
	}
	var mine, others []int
	for _, info := range s.pty.List() {
		if info.PID <= 1 || info.Status != ptyruntime.StatusRunning || !held(info.PID) {
			continue
		}
		roots := []int{info.PID}
		if holder, ok := parentPID(info.PID); ok && isHolderProc(holder) {
			roots = append(roots, holder)
		}
		if s.scopeKey(ctx, info.ID) == key {
			mine = append(mine, roots...)
		} else {
			others = append(others, roots...)
		}
	}
	if len(mine) == 0 {
		return nil
	}
	parents := procwalk.Parents()
	tree := procwalk.Descendants(mine, parents)
	taken := map[int]bool{os.Getpid(): true}
	for _, pid := range tree {
		taken[pid] = true
	}
	for _, pid := range procwalk.Descendants(others, parents) {
		taken[pid] = true
	}
	own := cgroup.Of(os.Getpid())
	for pid := range parents {
		if taken[pid] || own == "" || cgroup.Of(pid) != own {
			continue
		}
		if id, ok := procwalk.EnvValue(pid, "AIMAN_ID"); ok && id == key {
			tree = append(tree, pid)
		}
	}
	return tree
}

// handleSessionAdoptScope answers session.adopt_scope {session}: move a
// running session that has no scope (one started before serve had scopes, or
// whose scope launch failed) into its scope, with its limits. The session's
// TMPDIR does not change: a running process keeps its environment.
func (s *Server) handleSessionAdoptScope(ctx context.Context, req Request) Response {
	sess, bad, ok := s.resolveReq(ctx, req)
	if !ok {
		return bad
	}
	if s.isSessionCall(ctx, req) {
		return errResp(req.ID, CodeNeedsOperator, "only the operator may move a session into a scope")
	}
	opts := s.resourceOptions()
	sd := opts.Systemd
	if sd == nil {
		return errResp(req.ID, CodeUnavailable, "this host has no user systemd, so sessions have no scopes")
	}
	key := s.scopeKey(ctx, sess.ID)
	if len(s.scopeProcs(ctx, key)) == 0 {
		return errResp(req.ID, CodeInvalidParams, "session "+sess.Name+" has no running terminal")
	}
	limits := opts.sessionLimits(key)
	moved, err := sd.Adopt(ctx, key, limits, func() []int { return s.scopeProcs(ctx, key) })
	if err != nil && moved == 0 {
		return errResp(req.ID, CodeUnavailable, err.Error())
	}
	result := map[string]any{
		"type": "session_scope", "session": sess.ID, "name": sess.Name,
		"scope": cgroup.ScopeName(key), "moved": moved, "limits": limits,
	}
	payload := map[string]any{"scope": cgroup.ScopeName(key), "moved": moved}
	if err != nil {
		result["warning"] = err.Error()
		payload["warning"] = err.Error()
	}
	if moved > 0 {
		s.appendEvent(ctx, sess.ID, domain.EventActorUser, domain.EventResourcesAdopted, payload)
	}
	return Response{ID: req.ID, Result: result}
}
