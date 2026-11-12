package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/bouwerp/aiman/internal/cgroup"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/notify"
	"github.com/bouwerp/aiman/internal/ptyruntime"
)

// ResourcesReport is resources.get's result body: the sampler's last
// snapshot with the limits serve applies.
type ResourcesReport struct {
	cgroup.Snapshot
	// Limits are the defaults of one session scope (resources.session).
	Limits cgroup.Limits `json:"limits"`
	// SliceMemoryHigh is the aggregate limit serve set on the sessions
	// slice; absent when there is none.
	SliceMemoryHigh int64 `json:"slice_memory_high,omitempty"`
	// Tmpdir is true when each session gets its own TMPDIR.
	Tmpdir bool `json:"tmpdir"`
	// Missing names the session asked for when it has no scope: it runs
	// without limits until its terminal is restarted.
	Missing string `json:"missing,omitempty"`
}

// RunResources samples every session scope on a timer, keeps the snapshot
// resources.get answers from, and raises the threshold alerts. It returns
// at once on a host without session scopes, and otherwise when ctx ends.
func (s *Server) RunResources(ctx context.Context) {
	sd := s.resourceOptions().Systemd
	if sd == nil {
		return
	}
	sampler := cgroup.NewSampler(sd)
	sampler.Sessions = s.resourceSessions
	alerter := cgroup.NewAlerter()
	sampler.OnSample = func(snap cgroup.Snapshot) {
		for _, a := range alerter.Evaluate(snap, snap.At) {
			s.raiseResourceAlert(ctx, a)
		}
	}
	s.sampler.Store(sampler)
	sampler.Run(ctx)
}

// resourceSessions lists what may own a scope: every stored session, and
// every running terminal that is not a session's helper.
func (s *Server) resourceSessions(ctx context.Context) []cgroup.SessionRef {
	var refs []cgroup.SessionRef
	seen := map[string]bool{}
	if s.repo != nil {
		rows, err := s.storedSessions(ctx)
		if err != nil {
			log.Printf("resources: list sessions: %v", err)
		}
		for _, row := range rows {
			seen[row.ID] = true
			refs = append(refs, cgroup.SessionRef{ID: row.ID, Name: row.Name})
		}
	}
	if s.pty == nil {
		return refs
	}
	for _, info := range s.pty.List() {
		if seen[info.ID] || info.Status != ptyruntime.StatusRunning || s.scopeKey(ctx, info.ID) != info.ID {
			continue
		}
		seen[info.ID] = true
		refs = append(refs, cgroup.SessionRef{ID: info.ID, Name: info.Name})
	}
	return refs
}

// resourceAlertNote is the line typed into a session that is near or over
// its memory budget.
func resourceAlertNote(a cgroup.Alert) string {
	state := "is close to its limit"
	if a.Throttled {
		state = "is being throttled"
	}
	return fmt.Sprintf("[aiman] this session is at %s and %s; reduce parallelism. Do not retry the same job.", a.Text(), state)
}

// raiseResourceAlert tells the operator, and for a session alert the
// session's own agent. The Alerter has already rate-limited it.
func (s *Server) raiseResourceAlert(ctx context.Context, a cgroup.Alert) {
	rec := s.NotifyRecorder()
	switch a.Scope {
	case cgroup.AlertSession:
		label := a.SessionName
		hidden := false
		if sess, ok := s.sessionByID(ctx, a.SessionID); ok {
			label = notify.Label(sess.IssueKey, sess.Name, sess.ID)
			hidden = sess.Role.Hidden()
		}
		if label == "" {
			label = a.SessionID
		}
		detail := a.Text()
		if a.Throttled {
			detail += ", throttled"
		}
		if err := rec.Emit(ctx, a.SessionID, label, domain.NotifyResources, detail); err != nil {
			log.Printf("resources: notify %s: %v", a.SessionID, err)
		}
		s.appendEvent(ctx, a.SessionID, domain.EventActorAiman, domain.EventResourcesAlert, map[string]any{
			"memory_current": a.Usage.MemoryCurrent, "memory_high": a.Usage.MemoryHigh,
			"memory_pressure": a.Usage.MemoryPressure, "throttled": a.Throttled,
		})
		// A hidden helper runs serve's brief; its parent's operator was told.
		if st := s.Steerer(); st != nil && !hidden {
			st.Enqueue(SteerNote{SessionID: a.SessionID, Author: domain.AuthorAiman, Text: resourceAlertNote(a)})
		}
	default:
		what := "all sessions together are"
		if a.Scope == cgroup.AlertUserSlice {
			what = "this host's user slice is"
		}
		text := fmt.Sprintf("%s at %s: the whole host slows down past the limit", what, a.Text())
		if err := rec.Emit(ctx, "", "", domain.NotifyResources, text); err != nil {
			log.Printf("resources: notify %s: %v", a.Scope, err)
		}
	}
}

// handleResourcesGet answers resources.get {session?}: every session scope
// with the slice and host totals, or one session's. It only reads the
// sampler's snapshot. A host without session scopes answers supported:false.
func (s *Server) handleResourcesGet(ctx context.Context, req Request) Response {
	opts := s.resourceOptions()
	report := ResourcesReport{
		Snapshot:        s.sampler.Load().Snapshot(),
		Limits:          opts.Limits,
		SliceMemoryHigh: opts.SliceMemoryHigh,
		Tmpdir:          opts.TmpRoot != "",
	}
	if target := sessionTarget(req.Params); target != "" {
		id := target
		if list, err := s.loadSessions(ctx); err == nil {
			if sess, ok := resolveSession(list, target); ok {
				id = sess.ID
			}
		}
		id = s.scopeKey(ctx, id)
		kept := []cgroup.SessionUsage{}
		for _, u := range report.Sessions {
			if u.ID == id {
				kept = append(kept, u)
			}
		}
		report.Sessions = kept
		if len(kept) == 0 && report.Supported {
			report.Missing = id
		}
		report.Limits = opts.sessionLimits(id)
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "resources", "resources": report}}
}

// limitParams are the per-session limit overrides session.create and
// session.limits take: sizes such as "12G", or "infinity".
type limitParams struct {
	MemoryHigh string `json:"memory_high"`
	MemoryMax  string `json:"memory_max"`
}

func (p limitParams) empty() bool {
	return strings.TrimSpace(p.MemoryHigh) == "" && strings.TrimSpace(p.MemoryMax) == ""
}

func (p limitParams) limits() (cgroup.Limits, error) {
	high, err := cgroup.ParseSize(p.MemoryHigh)
	if err != nil {
		return cgroup.Limits{}, fmt.Errorf("memory_high: %w", err)
	}
	limit, err := cgroup.ParseSize(p.MemoryMax)
	if err != nil {
		return cgroup.Limits{}, fmt.Errorf("memory_max: %w", err)
	}
	if high > 0 && limit > 0 && limit < high {
		return cgroup.Limits{}, fmt.Errorf("memory_max %s is below memory_high %s", p.MemoryMax, p.MemoryHigh)
	}
	return cgroup.Limits{MemoryHigh: high, MemoryMax: limit}, nil
}

// createLimits validates session.create's limit override and stores it
// under id, so the new session's first terminal starts with it. It returns
// a refusal, or nil. Only the operator may raise a budget: a session that
// could would hand its children any limit it liked.
func (s *Server) createLimits(ctx context.Context, req Request, p limitParams, id string) *Response {
	if p.empty() {
		return nil
	}
	fail := func(code, msg string) *Response { r := errResp(req.ID, code, msg); return &r }
	if s.isSessionCall(ctx, req) {
		return fail(CodeNeedsOperator, "only the operator may set a session's memory limits")
	}
	over, err := p.limits()
	if err != nil {
		return fail(CodeInvalidParams, err.Error())
	}
	if _, err := s.resourceOptions().Overrides.Set(id, over); err != nil {
		return fail(CodeCreateFailed, "storing the session's limits: "+err.Error())
	}
	return nil
}

// handleSessionLimits answers session.limits {session, memory_high?,
// memory_max?}: store the override and apply it to the running scope. With
// no value it reports the session's limits.
func (s *Server) handleSessionLimits(ctx context.Context, req Request) Response {
	var p limitParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	sess, bad, ok := s.resolveReq(ctx, req)
	if !ok {
		return bad
	}
	opts := s.resourceOptions()
	applied := false
	if !p.empty() {
		if s.isSessionCall(ctx, req) {
			return errResp(req.ID, CodeNeedsOperator, "only the operator may set a session's memory limits")
		}
		over, err := p.limits()
		if err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
		if _, err := opts.Overrides.Set(sess.ID, over); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
		if sd := opts.Systemd; sd != nil && sd.Active(sess.ID) {
			if err := sd.SetLimits(ctx, sess.ID, opts.sessionLimits(sess.ID)); err != nil {
				return errResp(req.ID, CodeUnavailable, err.Error())
			}
			applied = true
		}
		s.appendEvent(ctx, sess.ID, domain.EventActorUser, domain.EventResourcesLimits, map[string]any{
			"memory_high": over.MemoryHigh, "memory_max": over.MemoryMax, "applied": applied,
		})
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type": "session_limits", "session": sess.ID, "name": sess.Name,
		"limits": opts.sessionLimits(sess.ID), "applied": applied,
		"scoped": opts.Systemd != nil && opts.Systemd.Active(sess.ID),
	}}
}
