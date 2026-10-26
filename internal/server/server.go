package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
	"github.com/bouwerp/aiman/internal/todo"
)

// rememberedPane is the last rendered screen served for one session. The next
// capture diffs against it. One memory per session, not per client: a client
// whose hash does not match gets a full screen.

type sessionCreator interface {
	CreateSession(ctx context.Context, cfg domain.SessionConfig) (*domain.Session, error)
}

type Server struct {
	ln       *Listener
	repo     domain.SessionRepository
	remote   domain.RemoteExecutor
	create   sessionCreator
	ctxStore domain.ContextStore
	pty      *ptyruntime.Manager
	version  string

	// hub carries events published by handlers to session.events streams.
	hub *eventHub

	mu     sync.Mutex
	sessMu map[string]*sync.Mutex

	// attaches counts live pty.attach streams per session so a dashboard
	// preview fit cannot shrink a session out from under a fullscreen client.
	attachMu sync.Mutex
	attaches map[string]int

	// panes remembers the last rendered capture per session so a preview poll
	// can return a row patch instead of the whole screen.
	paneMu sync.Mutex
	panes  map[string]rememberedPane

	// todos holds each session's to-do list and steer queues notes for agents.
	// Both stay nil until EnableTodos; Kick and Forget on a nil steerer are
	// no-ops, so handlers call them unconditionally.
	todos todoStore
	steer *steerer

	// steerMu guards in-flight typing and the time confirm last finished.
	// A timeout after the text is in the composer must not type it again, and
	// the classifier quiet window runs from confirm, not from the first Return.
	steerMu      sync.Mutex
	steerFlights map[string]steerFlight
	steerSettled map[string]time.Time
	// target is the session side of steering, the Server itself outside tests.
	target steerTarget
	// todoLocks holds one *sync.Mutex per session id around a to-do write and
	// its render.
	todoLocks sync.Map
}

// todoStore is the to-do persistence serve needs: the shared Store plus the
// merge of an agent's own task-list snapshot.
type todoStore interface {
	todo.Store
	Mirror(sessionID string, snap []domain.TodoSnapshotItem, deliveredAt time.Time) (domain.TodoList, todo.Change, error)
}

type rememberedPane struct {
	text string
}

func New(ln *Listener, repo domain.SessionRepository, remote domain.RemoteExecutor, create sessionCreator, ctxStore domain.ContextStore, ptyMgr *ptyruntime.Manager, version string) *Server {
	if version == "" {
		version = "dev"
	}
	s := &Server{
		ln:       ln,
		repo:     repo,
		remote:   remote,
		create:   create,
		ctxStore: ctxStore,
		pty:      ptyMgr,
		version:  version,
		sessMu:   map[string]*sync.Mutex{},
		attaches: map[string]int{},
		hub:      newEventHub(),
	}
	s.target = s
	return s
}

func (s *Server) Serve(ctx context.Context) error {
	if s.steer != nil {
		go s.steer.Run(ctx)
	}
	go func() {
		<-ctx.Done()
		_ = s.ln.Close()
	}()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			if errorsIsEOF(err) {
				return
			}
			return
		}
		line = bytesTrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if isPTYAttach(line) {
			s.handlePTYAttachConn(ctx, conn, line)
			return
		}
		// Like pty.attach, the event stream takes over the connection instead of
		// answering once.
		if isSessionEvents(line) {
			s.handleSessionEventsConn(ctx, conn, line)
			return
		}
		resp := s.dispatch(ctx, line)
		out, encErr := EncodeResponse(resp)
		if encErr != nil {
			return
		}
		if _, err := conn.Write(out); err != nil {
			return
		}
	}
}

func (s *Server) dispatch(ctx context.Context, line []byte) Response {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		return Response{ID: "", Error: &Error{Code: CodeInvalidParams, Message: err.Error()}}
	}
	switch req.Method {
	case "ping":
		return Response{ID: req.ID, Result: map[string]any{
			"type":     "pong",
			"version":  s.version,
			"protocol": ProtocolVersion,
		}}
	case "session.list":
		return s.handleList(ctx, req)
	case "session.get":
		return s.handleGet(ctx, req)
	case "session.read":
		return s.handleRead(ctx, req)
	case "session.prompt":
		return s.handlePrompt(ctx, req)
	case "session.wait":
		return s.handleWait(ctx, req)
	case "session.create":
		return s.handleCreate(ctx, req)
	case "session.rename":
		return s.handleRename(ctx, req)
	case "session.move":
		return s.handleMove(ctx, req)
	case "session.forget":
		return s.handleForget(ctx, req)
	case "session.register":
		return s.handleRegister(ctx, req)
	case "session.report_agent_session":
		return s.handleReportAgentSession(ctx, req)
	case "context.put":
		return s.handleContextPut(ctx, req)
	case "context.get":
		return s.handleContextGet(ctx, req)
	case "context.list":
		return s.handleContextList(ctx, req)
	case "context.find":
		return s.handleContextFind(ctx, req)
	case "context.pack":
		return s.handleContextPack(ctx, req)
	case "context.stats":
		return s.handleContextStats(ctx, req)
	case "handover.write":
		return s.handleHandoverWrite(ctx, req)
	case "handover.point":
		return s.handleHandoverPoint(ctx, req)
	case "todo.get", "todo.summary", "todo.add", "todo.update", "todo.move", "todo.remove", "todo.mirror", "steer.send":
		return s.handleTodo(ctx, req)
	case "pty.create":
		return s.handlePTYCreate(ctx, req)
	case "pty.list":
		return s.handlePTYList(ctx, req)
	case "pty.get":
		return s.handlePTYGet(ctx, req)
	case "pty.input":
		return s.handlePTYInput(ctx, req)
	case "pty.run":
		return s.handlePTYRun(ctx, req)
	case "pty.wait-output":
		return s.handlePTYWaitOutput(ctx, req)
	case "pty.capture":
		return s.handlePTYCapture(ctx, req)
	case "pty.resize":
		return s.handlePTYResize(ctx, req)
	case "pty.kill":
		return s.handlePTYKill(ctx, req)
	case "pty.forget":
		return s.handlePTYForget(ctx, req)
	default:
		return Response{ID: req.ID, Error: &Error{Code: CodeInvalidParams, Message: "unknown method " + req.Method}}
	}
}

func errorsIsEOF(err error) bool {
	return err == io.EOF || strings.Contains(err.Error(), "use of closed network connection")
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

func (s *Server) lockSession(id string) func() {
	s.mu.Lock()
	m, ok := s.sessMu[id]
	if !ok {
		m = &sync.Mutex{}
		s.sessMu[id] = m
	}
	s.mu.Unlock()
	m.Lock()
	return m.Unlock
}

func errResp(id, code, msg string) Response {
	return Response{ID: id, Error: &Error{Code: code, Message: msg}}
}
