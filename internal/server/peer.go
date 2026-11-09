package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/procwalk"
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

// maxProcDepth bounds the ancestor walk and the session parent walk.
const maxProcDepth = procwalk.MaxDepth

// parentPID reads pid's parent from /proc/<pid>/stat.
func parentPID(pid int) (int, bool) { return procwalk.ParentPID(pid) }

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
	raw, ok := procwalk.Cmdline(pid)
	return ok && isHolderCmdline(raw)
}

// isHolderCmdline reports whether a NUL-separated command line is a holder's.
func isHolderCmdline(raw []byte) bool { return procwalk.IsHolderCmdline(raw) }

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
	return target == c.caller || c.isHelperID(target) || c.descends(target)
}

// descends reports whether target is a strict descendant of the caller, by row
// ParentID or the holder's recorded parent. Helper ptys do not count: they are
// named after the caller, not parented to it.
func (c sessionScope) descends(target string) bool {
	if c.caller == "" || !plainID(target) || target == c.caller {
		return false
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

// holderSessionOf returns the session whose running holder owns pid: the
// nearest ancestor of pid (or pid itself) that a confirmed holder runs.
func (s *Server) holderSessionOf(pid int) (string, bool) {
	return ownerOf(pid, s.holderOwners(), parentPID)
}

// holderOwners maps the PID each confirmed running holder runs to its session.
func (s *Server) holderOwners() map[int]string {
	if s.pty == nil {
		return nil
	}
	held := s.holderHeld
	if held == nil {
		held = heldByHolder
	}
	infos := s.pty.List()
	roots := holderRoots(infos, held)
	owner := make(map[int]string, len(roots))
	for _, info := range infos {
		if roots[info.PID] {
			owner[info.PID] = info.ID
		}
	}
	return owner
}

// ownerOf walks pid and its ancestors to the first one owner names.
func ownerOf(pid int, owner map[int]string, parent func(int) (int, bool)) (string, bool) {
	if len(owner) == 0 {
		return "", false
	}
	for range maxProcDepth {
		if pid <= 1 {
			return "", false
		}
		if id, ok := owner[pid]; ok {
			return id, true
		}
		pp, ok := parent(pid)
		if !ok {
			return "", false
		}
		pid = pp
	}
	return "", false
}

// SetPeerNotes turns peer notes on or off, from the pushed
// sessions.peer_notes. They are on until it is called.
func (s *Server) SetPeerNotes(on bool) { s.peerNotesOff.Store(!on) }

// peerIdentity binds the sender of a peer call to the process, not to the
// caller string. A connection whose PID a holder owns is that holder's
// session, and a caller naming anything but that session or one of its helper
// ids is refused. A PID outside every holder keeps the scope's caller.
func (s *Server) peerIdentity(ctx context.Context, req Request, scope sessionScope, target string) (string, *Response) {
	if pid, ok := ctx.Value(peerPIDKey{}).(int); ok {
		lookup := s.sessionOfPID
		if lookup == nil {
			lookup = s.holderSessionOf
		}
		if holder, found := lookup(pid); found {
			if req.Caller != "" && req.Caller != holder && !strings.HasPrefix(req.Caller, holder+"-") {
				r := errResp(req.ID, CodeSessionScope, "caller "+req.Caller+" is not this process's session")
				return "", &r
			}
			return holder, nil
		}
	}
	if scope.caller == "" {
		r := scopeRefusal(req.ID, scope, "target "+target)
		return "", &r
	}
	return scope.caller, nil
}

// peerAllowed checks a bound sender and a target outside its tree: peer
// notes on, both visible and neither a fixer, not the same session, the
// target not a legacy tmux row, its agent alive, and a steerer to queue the
// note.
func (s *Server) peerAllowed(req Request, scope sessionScope, from string, target domain.Session) *Response {
	if refused := s.peerScopeRefusal(req, scope, from, target); refused != nil {
		return refused
	}
	if target.IsLegacyTmux() {
		r := legacyTerminalResp(req.ID, target)
		return &r
	}
	if target.AgentEnded || !s.ptyRunning(target.ID) {
		r := errResp(req.ID, CodeInvalidParams, "target agent has ended")
		return &r
	}
	if s.steer == nil {
		r := errResp(req.ID, CodeInvalidParams, errSteeringDisabled.Error())
		return &r
	}
	return nil
}

// peerScopeRefusal is the session_scope part of peerAllowed: the switch, the
// sender's and target's visibility, and self.
func (s *Server) peerScopeRefusal(req Request, scope sessionScope, from string, target domain.Session) *Response {
	refuse := func(msg string) *Response {
		r := errResp(req.ID, CodeSessionScope, msg)
		return &r
	}
	if s.peerNotesOff.Load() {
		r := scopeRefusal(req.ID, sessionScope{caller: from}, "target "+target.ID)
		r.Error.Message += "; peer notes are off on this host (sessions.peer_notes)"
		return &r
	}
	sender, ok := scope.rows[from]
	if !ok || !peerVisible(sender) {
		return refuse("session " + from + " may not send peer notes")
	}
	if !peerVisible(target) {
		return refuse("target " + target.ID + " does not take peer notes")
	}
	if target.ID == from {
		return refuse("a session cannot send a peer note to itself")
	}
	return nil
}

// peerVisible reports whether a session may send or take peer notes: hidden
// helpers run serve's briefs and fixers are told what to fix by their review.
func peerVisible(sess domain.Session) bool {
	return !sess.Role.Hidden() && sess.Role != domain.SessionRoleFixer
}

// ptyRunning reports whether the pty runtime lists id as running. With no
// runtime there is nothing to compare against, so every session counts.
func (s *Server) ptyRunning(id string) bool {
	if s.pty == nil {
		return true
	}
	info, err := s.pty.Get(id)
	return err == nil && info.Status == ptyruntime.StatusRunning
}

// admitPeer decides a session call that targets a session outside the
// caller's tree. It returns the bound sender and the normalised payload, or
// the refusal.
func (s *Server) admitPeer(ctx context.Context, req Request, scope sessionScope, target domain.Session, text string) (domain.Session, string, *Response) {
	from, refused := s.peerIdentity(ctx, req, scope, target.ID)
	if refused != nil {
		return domain.Session{}, "", refused
	}
	if refused := s.peerAllowed(req, scope, from, target); refused != nil {
		return domain.Session{}, "", refused
	}
	payload, err := peerPayload(text)
	if err != nil {
		r := errResp(req.ID, CodeInvalidParams, err.Error())
		return domain.Session{}, "", &r
	}
	return scope.rows[from], payload, nil
}

// queuePeerNote queues the labelled note for target and records it on both
// ledgers. It returns the note's row id.
func (s *Server) queuePeerNote(ctx context.Context, req Request, from, target domain.Session, payload string) (int64, *Response) {
	id, err := s.Steerer().EnqueuePeer(SteerNote{
		SessionID: target.ID, From: from.ID, Author: domain.AuthorPeer,
		Text: peerNoteText(from, payload),
	})
	if err != nil {
		code := CodeInvalidParams
		if errors.Is(err, ErrPeerQueueFull) {
			code = CodePeerQueueFull
		}
		r := errResp(req.ID, code, err.Error())
		return 0, &r
	}
	chars := utf8.RuneCountInString(payload)
	s.appendEvent(ctx, target.ID, domain.EventActorAgent, domain.EventPeerNote, map[string]any{
		"from": from.ID, "from_name": from.Name, "chars": chars,
	})
	s.appendEvent(ctx, from.ID, domain.EventActorAgent, domain.EventPeerNote, map[string]any{
		"to": target.ID, "to_name": target.Name, "chars": chars,
	})
	return id, nil
}

// peerNoteText is the note a peer's agent sees: the bound sender id first, so
// a display name can never close the bracket, then the sanitised name.
func peerNoteText(from domain.Session, payload string) string {
	name := from.Name
	if name == "" {
		name = from.ID
	}
	return "[peer session " + from.ID + " " + peerName(name) + "] " + payload
}

// peerName replaces every rune outside [A-Za-z0-9._-] with an underscore.
func peerName(name string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			return r
		}
		return '_'
	}, name)
}

// peerReservedPrefixes are the tokens that carry authority in a typed note.
var peerReservedPrefixes = []string{"[aiman]", "[peer session"}

// peerPayload normalises a peer note's payload and refuses one that is empty,
// over PeerNoteMaxBytes, or starts with a reserved token. Default-ignorable
// and bidi code points go first and NFKC folds look-alikes, so neither a
// zero-width space nor fullwidth brackets slip past the check.
func peerPayload(text string) (string, error) {
	text = strings.Map(func(r rune) rune {
		if unicode.In(r, unicode.Cf, unicode.Bidi_Control, unicode.Other_Default_Ignorable_Code_Point, unicode.Variation_Selector) {
			return -1
		}
		return r
	}, text)
	text = oneLine(norm.NFKC.String(text))
	switch {
	case text == "":
		return "", errors.New("text is required")
	case len(text) > PeerNoteMaxBytes:
		return "", fmt.Errorf("peer note is over %d bytes", PeerNoteMaxBytes)
	}
	lower := strings.ToLower(text)
	for _, p := range peerReservedPrefixes {
		if strings.HasPrefix(lower, p) {
			return "", fmt.Errorf("a peer note may not start with %s", p)
		}
	}
	return text, nil
}
