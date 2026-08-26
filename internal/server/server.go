package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/ptyruntime"
)

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

	mu     sync.Mutex
	sessMu map[string]*sync.Mutex
}

func New(ln *Listener, repo domain.SessionRepository, remote domain.RemoteExecutor, create sessionCreator, ctxStore domain.ContextStore, ptyMgr *ptyruntime.Manager, version string) *Server {
	if version == "" {
		version = "dev"
	}
	return &Server{
		ln:       ln,
		repo:     repo,
		remote:   remote,
		create:   create,
		ctxStore: ctxStore,
		pty:      ptyMgr,
		version:  version,
		sessMu:   map[string]*sync.Mutex{},
	}
}

func (s *Server) Serve(ctx context.Context) error {
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
	case "pty.create":
		return s.handlePTYCreate(ctx, req)
	case "pty.list":
		return s.handlePTYList(ctx, req)
	case "pty.get":
		return s.handlePTYGet(ctx, req)
	case "pty.input":
		return s.handlePTYInput(ctx, req)
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
