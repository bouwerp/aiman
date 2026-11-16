package server

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

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
	own := s.cgroupDir(os.Getpid())
	for pid := range parents {
		if taken[pid] || own == "" || s.cgroupDir(pid) != own {
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
	moved, err := s.adoptKey(ctx, key, limits)
	if err != nil && moved == 0 {
		return errResp(req.ID, CodeUnavailable, err.Error())
	}
	result := map[string]any{
		"type": "session_scope", "session": sess.ID, "name": sess.Name,
		"scope": cgroup.ScopeName(key), "moved": moved, "limits": limits,
	}
	if err != nil {
		result["warning"] = err.Error()
	}
	s.recordAdopted(ctx, sess.ID, domain.EventActorUser, key, moved, err)
	return Response{ID: req.ID, Result: result}
}

// adoptKey moves key's running processes into its scope with limits l. It is
// the one adoption body session.adopt_scope and the sweep share.
func (s *Server) adoptKey(ctx context.Context, key string, l cgroup.Limits) (int, error) {
	pids := func() []int { return s.scopeProcs(ctx, key) }
	if s.adoptProcs != nil {
		return s.adoptProcs(ctx, key, l, pids)
	}
	sd := s.resourceOptions().Systemd
	if sd == nil {
		return 0, fmt.Errorf("no user systemd")
	}
	return sd.Adopt(ctx, key, l, pids)
}

// recordAdopted writes the resources.adopted ledger row for an adoption that
// moved something.
func (s *Server) recordAdopted(ctx context.Context, id string, actor domain.EventActor, key string, moved int, err error) {
	if moved == 0 {
		return
	}
	payload := map[string]any{"scope": cgroup.ScopeName(key), "moved": moved}
	if err != nil {
		payload["warning"] = err.Error()
	}
	s.appendEvent(ctx, id, actor, domain.EventResourcesAdopted, payload)
}

// cgroupDir is the cgroup directory of pid; tests replace Server.cgroupOf.
func (s *Server) cgroupDir(pid int) string {
	if s.cgroupOf != nil {
		return s.cgroupOf(pid)
	}
	return cgroup.Of(pid)
}

// unscopedHolders lists running holders outside the sessions slice, one per
// scope key, in pty list order. Empty without a user systemd.
func (s *Server) unscopedHolders(ctx context.Context) []UnscopedSession {
	out := []UnscopedSession{}
	sd := s.resourceOptions().Systemd
	if sd == nil || s.pty == nil {
		return out
	}
	held := s.holderHeld
	if held == nil {
		held = heldByHolder
	}
	slice := sd.SliceDir() + "/"
	seen := map[string]bool{}
	for _, info := range s.pty.List() {
		if info.PID <= 1 || info.Status != ptyruntime.StatusRunning || !held(info.PID) {
			continue
		}
		key := s.scopeKey(ctx, info.ID)
		if seen[key] || strings.HasPrefix(s.cgroupDir(info.PID)+"/", slice) {
			continue
		}
		seen[key] = true
		name := info.Name
		if sess, ok := s.sessionByID(ctx, key); ok && sess.Name != "" {
			name = sess.Name
		}
		out = append(out, UnscopedSession{ID: key, Name: name})
	}
	return out
}

const (
	adoptSweepFirst = 30 * time.Second
	adoptSweepEvery = 5 * time.Minute
)

// runAdoptSweep runs adoptSweep 30s after start and then every 5 minutes.
func (s *Server) runAdoptSweep(ctx context.Context) {
	wait := adoptSweepFirst
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		s.adoptSweep(ctx)
		wait = adoptSweepEvery
	}
}

// adoptSweep moves every running session that has no scope into one, with
// its stored limits. It never runs from a serve outside the user manager's
// subtree, where the kernel refuses the move. A failed adoption is tried
// again on the next pass and logged once per session and error text.
func (s *Server) adoptSweep(ctx context.Context) {
	opts := s.resourceOptions()
	sd := opts.Systemd
	if sd == nil {
		return
	}
	if own := s.cgroupDir(os.Getpid()); own == "" || !strings.HasPrefix(own+"/", sd.Root+"/") {
		s.adoptOutside.Do(func() {
			log.Printf("resources: serve runs outside the user manager (%q), so running sessions are not moved into scopes", own)
		})
		return
	}
	for _, u := range s.unscopedHolders(ctx) {
		moved, err := s.adoptKey(ctx, u.ID, opts.sessionLimits(u.ID))
		s.noteAdoptResult(u, err)
		s.recordAdopted(ctx, u.ID, domain.EventActorAiman, u.ID, moved, err)
	}
}

// noteAdoptResult logs a failure when its text differs from the last one
// logged for the session, and forgets the session once it succeeds.
func (s *Server) noteAdoptResult(u UnscopedSession, err error) {
	s.adoptMu.Lock()
	defer s.adoptMu.Unlock()
	if err == nil {
		delete(s.adoptErrs, u.ID)
		return
	}
	if s.adoptErrs == nil {
		s.adoptErrs = map[string]string{}
	}
	if s.adoptErrs[u.ID] == err.Error() {
		return
	}
	s.adoptErrs[u.ID] = err.Error()
	log.Printf("resources: adopting session %s into a scope: %v", u.ID, err)
}
