package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/todo"
)

// steerCoalesceWindow is how long the steerer gathers changes into one note.
// A variable so tests can shorten it.
var steerCoalesceWindow = 800 * time.Millisecond

const msgTodosDisabled = "to-dos not enabled"

var (
	errTodosDisabled = errors.New(msgTodosDisabled)
	// errTodoNotFound marks an unknown item id; handlers answer not_found.
	errTodoNotFound = errors.New("to-do item not found")
)

// EnableTodos turns on to-do lists and steering. It restores the persisted
// steering queue before returning, so it must run before Serve accepts
// connections. s.todos is assigned only after Load, and handlers refuse
// writes while it is nil.
func (s *Server) EnableTodos(store todoStore, notes SteerStore) {
	st := newSteerer(notes, s.target, s.hub.Publish, steerCoalesceWindow)
	ctx, cancel := context.WithTimeout(context.Background(), steerOpTimeout)
	defer cancel()
	if err := st.Load(ctx); err != nil {
		log.Printf("steer: load queue: %v", err)
	}
	s.steer = st
	s.todos = store
}

// noopSteerer stands in for the steerer while to-dos are disabled.
type noopSteerer struct{}

func (noopSteerer) Enqueue(SteerNote)         {}
func (noopSteerer) Status(string) SteerStatus { return SteerStatus{} }

// Steerer is the steering queue for in-process callers. It is a no-op until
// EnableTodos runs.
func (s *Server) Steerer() Steerer {
	if s.steer == nil {
		return noopSteerer{}
	}
	return s.steer
}

// deriveAuthor decides who a request writes as. It is the only authorship
// check: the caller's "as" is a request, not a claim the server trusts.
//
// A session acting on its own list is its agent, whatever it passes as "as",
// and todo.mirror only ever carries the agent's own task list. Everyone else is
// the user, except that aiman-trigger, which talks to the socket directly, may
// steer as aiman. Serve-internal writers use MutateTodos with their author.
func deriveAuthor(method, callerID, targetID, as string) (domain.TodoAuthor, error) {
	if method == "todo.mirror" {
		return domain.AuthorAgent, nil
	}
	if callerID != "" && callerID == targetID {
		if method == "steer.send" {
			return "", errors.New("a session cannot steer itself")
		}
		return domain.AuthorAgent, nil
	}
	switch as {
	case "", string(domain.AuthorUser):
		return domain.AuthorUser, nil
	case string(domain.AuthorAiman):
		if method == "steer.send" {
			return domain.AuthorAiman, nil
		}
	}
	return "", fmt.Errorf("cannot act as %q", as)
}

// oneLine collapses newlines and runs of whitespace so an item or note stays a
// single line in .aiman_todo.md and in the agent's prompt.
func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// todoLock serialises a session's write and render so .aiman_todo.md never
// ends on an older list than the store holds.
func (s *Server) todoLock(id string) func() {
	v, _ := s.todoLocks.LoadOrStore(id, &sync.Mutex{})
	m := v.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

// MutateTodos applies fn to the session's list as author. A change is rendered
// into the session's working directory, announced as todo_changed, and queued
// for the agent unless the agent made it.
func (s *Server) MutateTodos(_ context.Context, sess domain.Session, author domain.TodoAuthor, fn func(*domain.TodoList) error) (domain.TodoList, error) {
	list, _, err := s.mutateTodos(sess, author, fn)
	return list, err
}

// mutateTodos is MutateTodos that also returns what changed.
func (s *Server) mutateTodos(sess domain.Session, author domain.TodoAuthor, fn func(*domain.TodoList) error) (domain.TodoList, todo.Change, error) {
	if s.todos == nil {
		return domain.TodoList{}, todo.Change{}, errTodosDisabled
	}
	unlock := s.todoLock(sess.ID)
	defer unlock()
	var prevRev int64
	list, change, err := s.todos.Mutate(sess.ID, author, func(l *domain.TodoList) error {
		prevRev = l.Rev
		return fn(l)
	})
	if err != nil {
		return list, change, err
	}
	s.afterTodoChange(sess, author, list, change, prevRev, true)
	return list, change, nil
}

// mirrorTodos merges the agent's task-list snapshot, then runs the same
// follow-up as MutateTodos.
func (s *Server) mirrorTodos(sess domain.Session, snap []domain.TodoSnapshotItem) (domain.TodoList, error) {
	unlock := s.todoLock(sess.ID)
	defer unlock()
	prev, err := s.todos.Get(sess.ID)
	if err != nil {
		return prev, err
	}
	list, change, err := s.todos.Mirror(sess.ID, snap, s.steer.Status(sess.ID).LastDeliveredAt)
	if err != nil {
		return list, err
	}
	s.afterTodoChange(sess, domain.AuthorAgent, list, change, prev.Rev, true)
	return list, nil
}

// afterTodoChange renders, announces and steers a written list. A write that
// changed only NotInAgent still bumps Rev, so readers refresh on it too.
func (s *Server) afterTodoChange(sess domain.Session, author domain.TodoAuthor, list domain.TodoList, change todo.Change, prevRev int64, steer bool) {
	if change.Empty() && list.Rev == prevRev {
		return
	}
	if sess.WorkingDirectory != "" {
		if _, err := todo.WriteRendered(sess.WorkingDirectory, list); err != nil {
			log.Printf("todo: render %s: %v", sess.ID, err)
		}
	}
	s.hub.Publish(SessionEvent{Type: "todo_changed", ID: sess.ID, Rev: list.Rev})
	s.appendEvent(context.Background(), sess.ID, todoEventActor(author), domain.EventTodoChanged, map[string]any{
		"rev": list.Rev, "author": string(author),
	})
	if steer && change.SteersAgent() {
		s.steer.EnqueueTodo(sess.ID, author, change.Ops)
	}
}

func todoEventActor(author domain.TodoAuthor) domain.EventActor {
	switch author {
	case domain.AuthorAgent:
		return domain.EventActorAgent
	case domain.AuthorAiman:
		return domain.EventActorAiman
	default:
		return domain.EventActorUser
	}
}

// TodoAccess is the to-do adapter serve hands to its background watchers.
type TodoAccess struct{ s *Server }

// TodoAccess returns the adapter over this server's to-do lists.
func (s *Server) TodoAccess() TodoAccess { return TodoAccess{s: s} }

// Get returns the session's list, empty when it has none.
func (a TodoAccess) Get(sessionID string) (domain.TodoList, error) {
	if a.s.todos == nil {
		return domain.TodoList{}, errTodosDisabled
	}
	return a.s.todos.Get(sessionID)
}

// Mutate changes the session's list as author. The session comes from
// loadSessions, then the write goes through MutateTodos.
func (a TodoAccess) Mutate(ctx context.Context, sessionID string, author domain.TodoAuthor, fn func(*domain.TodoList) error) error {
	list, err := a.s.loadSessions(ctx)
	if err != nil {
		return err
	}
	sess, ok := resolveSession(list, sessionID)
	if !ok {
		return fmt.Errorf("todo: session %s not found", sessionID)
	}
	_, err = a.s.MutateTodos(ctx, sess, author, fn)
	return err
}

// todoParams is the union of the to-do and steer methods' parameters.
type todoParams struct {
	Session string           `json:"session"`
	As      string           `json:"as"`
	ID      string           `json:"id"`
	Text    *string          `json:"text"`
	State   string           `json:"state"`
	After   string           `json:"after"`
	Ref     string           `json:"ref"`
	ToIndex int              `json:"to_index"`
	Items   []todoMirrorItem `json:"items"`
	Mode    string           `json:"mode"`
	Quote   string           `json:"quote"`
}

// gateParams carries the gate ref of todo.gate.
type gateParams struct {
	Ref string `json:"ref"`
}

type todoMirrorItem struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

// todoRequest decodes params and resolves the target session.
func (s *Server) todoRequest(ctx context.Context, req Request) (todoParams, domain.Session, *Response) {
	var p todoParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			r := errResp(req.ID, CodeInvalidParams, err.Error())
			return p, domain.Session{}, &r
		}
	}
	if strings.TrimSpace(p.Session) == "" {
		r := errResp(req.ID, CodeInvalidParams, "session is required")
		return p, domain.Session{}, &r
	}
	list, err := s.loadSessions(ctx)
	if err != nil {
		r := errResp(req.ID, CodeInvalidParams, err.Error())
		return p, domain.Session{}, &r
	}
	sess, ok := resolveSession(list, p.Session)
	if !ok {
		r := errResp(req.ID, CodeNotFound, "session not found")
		return p, domain.Session{}, &r
	}
	return p, sess, nil
}

// todoErrResp maps a mutation error to its protocol code.
func todoErrResp(id string, err error) Response {
	switch {
	case errors.Is(err, errTodoNotFound):
		return errResp(id, CodeNotFound, err.Error())
	case errors.Is(err, todo.ErrGateLocked):
		return errResp(id, CodeGateNeedsOperator, err.Error())
	default:
		return errResp(id, CodeInvalidParams, err.Error())
	}
}

func todoListResp(id string, list domain.TodoList) Response {
	return Response{ID: id, Result: map[string]any{"type": "todo_list", "list": list}}
}

func findTodo(l *domain.TodoList, id string) (int, error) {
	for i, it := range l.Items {
		if it.ID == id {
			return i, nil
		}
	}
	return -1, fmt.Errorf("%w: %s", errTodoNotFound, id)
}

// handleTodo serves the to-do and steer methods.
func (s *Server) handleTodo(ctx context.Context, req Request) Response {
	if s.todos == nil {
		return errResp(req.ID, CodeInvalidParams, msgTodosDisabled)
	}
	if req.Method == "todo.summary" {
		return s.handleTodoSummary(ctx, req)
	}
	p, sess, bad := s.todoRequest(ctx, req)
	if bad != nil {
		return *bad
	}
	if req.Method == "todo.get" {
		list, err := s.todos.Get(sess.ID)
		if err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
		return Response{ID: req.ID, Result: map[string]any{"type": "todo_list", "list": list, "steer": s.steer.Status(sess.ID)}}
	}
	author, err := deriveAuthor(req.Method, req.Caller, sess.ID, p.As)
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	switch req.Method {
	case "todo.ensure_done":
		return s.handleTodoEnsureDone(ctx, req, sess)
	case "todo.mark_met":
		return s.handleTodoMarkMet(req, sess)
	case "todo.gate":
		return s.handleTodoGate(req, sess)
	case "todo.approve_design":
		return s.approveGate(ctx, req, p, sess, todo.DesignGateRef, domain.EventDesignApproved)
	case "todo.approve_review":
		return s.approveGate(ctx, req, p, sess, todo.ReviewGateRef, domain.EventReviewApproved)
	case "steer.send":
		return s.handleSteerSend(ctx, req, p, sess, author)
	case "todo.mirror":
		return s.handleTodoMirror(req, p, sess)
	case "todo.add":
		return s.handleTodoAdd(req, p, sess, author, s.isSessionCall(ctx, req))
	}

	var fn func(*domain.TodoList) error
	switch req.Method {
	case "todo.update":
		fn, err = todoUpdateFn(p)
	case "todo.move":
		fn = func(l *domain.TodoList) error {
			i, err := findTodo(l, p.ID)
			if err != nil {
				return err
			}
			it := l.Items[i]
			l.Items = append(l.Items[:i], l.Items[i+1:]...)
			to := min(max(p.ToIndex, 0), len(l.Items))
			l.Items = append(l.Items[:to], append([]domain.TodoItem{it}, l.Items[to:]...)...)
			return nil
		}
	case "todo.remove":
		fn = func(l *domain.TodoList) error {
			i, err := findTodo(l, p.ID)
			if err != nil {
				return err
			}
			l.Items = append(l.Items[:i], l.Items[i+1:]...)
			return nil
		}
	default:
		return errResp(req.ID, CodeInvalidParams, "unknown method "+req.Method)
	}
	if err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	list, err := s.MutateTodos(ctx, sess, author, guardGate(s.isSessionCall(ctx, req), fn))
	if err != nil {
		return todoErrResp(req.ID, err)
	}
	return todoListResp(req.ID, list)
}

func todoUpdateFn(p todoParams) (func(*domain.TodoList) error, error) {
	if strings.TrimSpace(p.ID) == "" {
		return nil, errors.New("id is required")
	}
	var text string
	if p.Text != nil {
		if text = oneLine(*p.Text); text == "" {
			return nil, errors.New("text is empty")
		}
	}
	state := domain.TodoState(p.State)
	if p.State != "" && !state.Valid() {
		return nil, fmt.Errorf("unknown state %q", p.State)
	}
	if text == "" && p.State == "" {
		return nil, errors.New("text or state is required")
	}
	return func(l *domain.TodoList) error {
		i, err := findTodo(l, p.ID)
		if err != nil {
			return err
		}
		if text != "" {
			l.Items[i].Text = text
		}
		if p.State != "" {
			l.Items[i].State = state
		}
		return nil
	}, nil
}

// ensureDone adds the Done-when item without steering the agent. It is
// idempotent and still renders and announces a real change.
func (s *Server) ensureDone(sess domain.Session) (domain.TodoList, bool, error) {
	if s.todos == nil {
		return domain.TodoList{}, false, errTodosDisabled
	}
	unlock := s.todoLock(sess.ID)
	defer unlock()
	prev, err := s.todos.Get(sess.ID)
	if err != nil {
		return prev, false, err
	}
	added := false
	list, change, err := s.todos.Mutate(sess.ID, domain.AuthorAiman, func(l *domain.TodoList) error {
		added = todo.EnsureDoneItem(l)
		return nil
	})
	if err != nil {
		return prev, false, err
	}
	s.afterTodoChange(sess, domain.AuthorAiman, list, change, prev.Rev, false)
	return list, added, nil
}

func (s *Server) markDoneMet(sess domain.Session) (domain.TodoList, bool, error) {
	if s.todos == nil {
		return domain.TodoList{}, false, errTodosDisabled
	}
	unlock := s.todoLock(sess.ID)
	defer unlock()
	prev, err := s.todos.Get(sess.ID)
	if err != nil {
		return prev, false, err
	}
	changed := false
	list, change, err := s.todos.Mutate(sess.ID, domain.AuthorAiman, func(l *domain.TodoList) error {
		for i := range l.Items {
			if l.Items[i].Ref == todo.DoneRef && l.Items[i].State != domain.TodoDone {
				l.Items[i].State = domain.TodoDone
				changed = true
			}
		}
		return nil
	})
	if err != nil {
		return prev, false, err
	}
	s.afterTodoChange(sess, domain.AuthorAiman, list, change, prev.Rev, true)
	return list, changed, nil
}

func (s *Server) handleTodoMarkMet(req Request, sess domain.Session) Response {
	list, changed, err := s.markDoneMet(sess)
	if err != nil {
		return todoErrResp(req.ID, err)
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "todo_list", "list": list, "changed": changed}}
}

func (s *Server) handleTodoEnsureDone(_ context.Context, req Request, sess domain.Session) Response {
	list, added, err := s.ensureDone(sess)
	if err != nil {
		return todoErrResp(req.ID, err)
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type": "todo_list", "list": list, "added": added,
	}}
}

func (s *Server) handleTodoAdd(req Request, p todoParams, sess domain.Session, author domain.TodoAuthor, fromSession bool) Response {
	text := ""
	if p.Text != nil {
		text = oneLine(*p.Text)
	}
	if text == "" {
		return errResp(req.ID, CodeInvalidParams, "text is required")
	}
	list, change, err := s.mutateTodos(sess, author, guardGate(fromSession, func(l *domain.TodoList) error {
		at := len(l.Items)
		if p.After != "" {
			i, err := findTodo(l, p.After)
			if err != nil {
				return err
			}
			at = i + 1
		}
		item := domain.TodoItem{Text: text, State: domain.TodoPending, Ref: strings.TrimSpace(p.Ref)}
		l.Items = append(l.Items[:at], append([]domain.TodoItem{item}, l.Items[at:]...)...)
		return nil
	}))
	if err != nil {
		return todoErrResp(req.ID, err)
	}
	// The store assigns the id; the change's add op carries it.
	id := ""
	for _, op := range change.Ops {
		if op.Kind == todo.OpAdd {
			id = op.ID
			break
		}
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "todo_list", "list": list, "id": id}}
}

func (s *Server) handleTodoMirror(req Request, p todoParams, sess domain.Session) Response {
	snap := make([]domain.TodoSnapshotItem, 0, len(p.Items))
	for _, it := range p.Items {
		text := oneLine(it.Text)
		if text == "" {
			continue
		}
		snap = append(snap, domain.TodoSnapshotItem{Text: text, Status: domain.NormalizeVendorTodoStatus(it.Status)})
	}
	list, err := s.mirrorTodos(sess, snap)
	if err != nil {
		return todoErrResp(req.ID, err)
	}
	return todoListResp(req.ID, list)
}

// handleSteerSend queues a note for the agent, or with mode "answer" types
// the text straight into a dialog the agent is waiting on.
func (s *Server) handleSteerSend(ctx context.Context, req Request, p todoParams, sess domain.Session, author domain.TodoAuthor) Response {
	text := ""
	if p.Text != nil {
		text = oneLine(*p.Text)
	}
	if text == "" {
		return errResp(req.ID, CodeInvalidParams, "text is required")
	}
	switch p.Mode {
	case "", "note":
		s.steer.Enqueue(SteerNote{SessionID: sess.ID, Author: author, Text: text})
		return Response{ID: req.ID, Result: map[string]any{"type": "steer_status", "steer": s.steer.Status(sess.ID)}}
	case "answer":
		answered, err := s.answerDialog(ctx, sess.ID, text)
		if err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
		return Response{ID: req.ID, Result: map[string]any{"type": "steer_status", "steer": s.steer.Status(sess.ID), "answered": answered}}
	default:
		return errResp(req.ID, CodeInvalidParams, fmt.Sprintf("unknown mode %q", p.Mode))
	}
}

// answerDialog types text at once when the agent is held on a permission or
// choice dialog. Answers bypass the queue: they are never stored or retried.
func (s *Server) answerDialog(ctx context.Context, id, text string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, steerOpTimeout)
	defer cancel()
	if _, held := s.target.steerReady(ctx, id, time.Time{}); held != steerHeldDialog {
		return false, nil
	}
	if _, err := s.target.deliverSteer(ctx, id, text); err != nil {
		return false, err
	}
	return true, nil
}

// todoSummaryEntry is one session's progress for the dashboard's list.
type todoSummaryEntry struct {
	Done   int    `json:"done"`
	Total  int    `json:"total"`
	Active string `json:"active"`
	Rev    int64  `json:"rev"`
}

// handleTodoSummary reports progress for the given sessions, or for every
// session serve knows. Sessions that never had a list are left out.
func (s *Server) handleTodoSummary(ctx context.Context, req Request) Response {
	var p struct {
		Sessions []string `json:"sessions"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	ids := p.Sessions
	if ids == nil {
		list, err := s.loadSessions(ctx)
		if err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
		for _, sess := range list {
			ids = append(ids, sess.ID)
		}
	}
	out := map[string]todoSummaryEntry{}
	for _, id := range ids {
		list, err := s.todos.Get(id)
		if err != nil || list.Rev == 0 {
			continue
		}
		e := todoSummaryEntry{Rev: list.Rev}
		for _, it := range list.Items {
			switch it.State {
			case domain.TodoDropped:
				continue
			case domain.TodoDone:
				e.Done++
			case domain.TodoActive:
				e.Active = it.Text
			}
			e.Total++
		}
		out[id] = e
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "todo_summary", "sessions": out}}
}

// guardGate wraps fn so a session call cannot change an open gate.
// Anything else is the operator, who may.
func guardGate(fromSession bool, fn func(*domain.TodoList) error) func(*domain.TodoList) error {
	if !fromSession {
		return fn
	}
	return func(l *domain.TodoList) error {
		prev := *l
		prev.Items = append([]domain.TodoItem(nil), l.Items...)
		if err := fn(l); err != nil {
			return err
		}
		return todo.GuardGate(prev, *l)
	}
}

// mutateQuiet applies fn as aiman without steering the agent, and still
// renders and announces a real change.
func (s *Server) mutateQuiet(sess domain.Session, fn func(*domain.TodoList) error) (domain.TodoList, error) {
	if s.todos == nil {
		return domain.TodoList{}, errTodosDisabled
	}
	unlock := s.todoLock(sess.ID)
	defer unlock()
	prev, err := s.todos.Get(sess.ID)
	if err != nil {
		return prev, err
	}
	list, change, err := s.todos.Mutate(sess.ID, domain.AuthorAiman, fn)
	if err != nil {
		return prev, err
	}
	s.afterTodoChange(sess, domain.AuthorAiman, list, change, prev.Rev, false)
	return list, nil
}

// handleTodoGate adds the open gate named by params.ref (default the design
// gate) to the session's list.
func (s *Server) handleTodoGate(req Request, sess domain.Session) Response {
	var gp gateParams
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params, &gp); err != nil {
			return errResp(req.ID, CodeInvalidParams, err.Error())
		}
	}
	if gp.Ref == "" {
		gp.Ref = todo.DesignGateRef
	}
	if _, ok := todo.GateText(gp.Ref); !ok {
		return errResp(req.ID, CodeInvalidParams, "unknown gate ref "+gp.Ref)
	}
	added := false
	list, err := s.mutateQuiet(sess, func(l *domain.TodoList) error {
		added = todo.AddGate(l, gp.Ref)
		return nil
	})
	if err != nil {
		return todoErrResp(req.ID, err)
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "todo_list", "list": list, "added": added}}
}

// approveGate closes the session's gate for ref on the strength of the
// operator's quote and records kind. Only the session itself may call it: the
// quote is the operator's words relayed by the session, recorded in the ledger.
func (s *Server) approveGate(ctx context.Context, req Request, p todoParams, sess domain.Session, ref string, kind domain.EventKind) Response {
	name := strings.TrimPrefix(ref, "gate:")
	if req.Caller != sess.ID {
		return errResp(req.ID, CodeGateNeedsOperator, "only the session itself can record its "+name+" approval")
	}
	quote := strings.TrimSpace(p.Quote)
	if quote == "" {
		return errResp(req.ID, CodeInvalidParams, "quote is required: the operator's words approving the "+name)
	}
	closed, found := false, false
	list, err := s.mutateQuiet(sess, func(l *domain.TodoList) error {
		for i := range l.Items {
			if l.Items[i].Ref != ref {
				continue
			}
			found = true
			if l.Items[i].State != domain.TodoDone {
				l.Items[i].State = domain.TodoDone
				closed = true
			}
		}
		return nil
	})
	if err != nil {
		return todoErrResp(req.ID, err)
	}
	if !found {
		return errResp(req.ID, CodeInvalidParams, "session has no "+name+" gate")
	}
	if closed {
		s.appendEvent(ctx, sess.ID, domain.EventActorAgent, kind, map[string]any{"quote": quote})
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "todo_list", "list": list, "approved": closed}}
}
