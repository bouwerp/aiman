package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/ptyruntime"
)

// PTY methods are the socket API surface for the built-in PTY runtime. All are
// plain request/response except pty.attach, which switches the connection into
// a raw bidirectional byte stream after a single confirmation line.

func (s *Server) handlePTYCreate(ctx context.Context, req Request) Response {
	var params struct {
		ID      string            `json:"id"`
		Name    string            `json:"name"`
		Dir     string            `json:"dir"`
		Command string            `json:"command"`
		Env     map[string]string `json:"env"`
		Cols    int               `json:"cols"`
		Rows    int               `json:"rows"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errResp(req.ID, CodeInvalidParams, err.Error())
	}
	if params.ID == "" || params.Command == "" {
		return errResp(req.ID, CodeInvalidParams, "id and command are required")
	}
	info, err := s.pty.Create(ptyruntime.Spec{
		ID:      params.ID,
		Name:    params.Name,
		Dir:     params.Dir,
		Command: params.Command,
		Env:     params.Env,
		Cols:    params.Cols,
		Rows:    params.Rows,
	})
	if err != nil {
		return s.ptyErrResp(req.ID, err)
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "pty_session", "session": info}}
}

func (s *Server) handlePTYList(ctx context.Context, req Request) Response {
	list := s.pty.List()
	return Response{ID: req.ID, Result: map[string]any{"type": "pty_list", "sessions": list}}
}

func (s *Server) resolvePTYID(req Request) (string, Response, bool) {
	var params struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.ID == "" {
		return "", errResp(req.ID, CodeInvalidParams, "id is required"), false
	}
	return params.ID, Response{}, true
}

func (s *Server) handlePTYGet(ctx context.Context, req Request) Response {
	id, fail, ok := s.resolvePTYID(req)
	if !ok {
		return fail
	}
	info, err := s.pty.Get(id)
	if err != nil {
		return s.ptyErrResp(req.ID, err)
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "pty_session", "session": info}}
}

func (s *Server) handlePTYInput(ctx context.Context, req Request) Response {
	id, fail, ok := s.resolvePTYID(req)
	if !ok {
		return fail
	}
	var params struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.Data == "" {
		return errResp(req.ID, CodeInvalidParams, "data is required")
	}
	if err := s.pty.Write(id, []byte(params.Data)); err != nil {
		return s.ptyErrResp(req.ID, err)
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "pty_input", "sent": true}}
}

func (s *Server) handlePTYCapture(ctx context.Context, req Request) Response {
	id, fail, ok := s.resolvePTYID(req)
	if !ok {
		return fail
	}
	var params struct {
		MaxBytes int `json:"max_bytes"`
		Lines    int `json:"lines"`
	}
	_ = json.Unmarshal(req.Params, &params)
	// Rendered, not raw: the caller wants a screen, the way tmux capture-pane
	// gives one. MaxBytes is deliberately not applied to the spool here —
	// truncating the byte stream would cut mid-escape-sequence and corrupt the
	// replay; the rendered screen is already bounded by the session's size.
	text, err := s.pty.CaptureScreen(id)
	if err != nil {
		return s.ptyErrResp(req.ID, err)
	}
	if params.Lines > 0 {
		text = tailLines(text, params.Lines)
	}
	// The activity fields ride along so a caller judging what the session is
	// doing gets the screen and the timings in one round trip. Silence and a
	// moving title are what actually decide the answer; the screen is the
	// fallback evidence.
	result := map[string]any{"type": "pane_read", "text": text}
	if info, ierr := s.pty.Get(id); ierr == nil {
		if !info.LastOutput.IsZero() {
			result["last_output"] = info.LastOutput.UTC().Format(time.RFC3339Nano)
		}
		if !info.TitleChanged.IsZero() {
			result["title_changed_at"] = info.TitleChanged.UTC().Format(time.RFC3339Nano)
		}
		if info.Title != "" {
			result["title"] = info.Title
		}
	}
	return Response{ID: req.ID, Result: result}
}

func (s *Server) handlePTYKill(ctx context.Context, req Request) Response {
	id, fail, ok := s.resolvePTYID(req)
	if !ok {
		return fail
	}
	if err := s.pty.Kill(id); err != nil {
		return s.ptyErrResp(req.ID, err)
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "pty_kill", "killed": true}}
}

func (s *Server) handlePTYForget(ctx context.Context, req Request) Response {
	id, fail, ok := s.resolvePTYID(req)
	if !ok {
		return fail
	}
	if err := s.pty.Forget(id); err != nil {
		return s.ptyErrResp(req.ID, err)
	}
	return Response{ID: req.ID, Result: map[string]any{"type": "pty_forget", "forgotten": true}}
}

// handlePTYResize sets a session's window size outside an attach stream.
//
// Resizing was previously reachable only from inside pty.attach, which left no
// way to fit a session to a viewer that is not attached — the dashboard shows a
// preview panel far narrower than the terminal that last sized the session, so
// without this the agent renders wider than anything can display.
func (s *Server) handlePTYResize(_ context.Context, req Request) Response {
	id, fail, ok := s.resolvePTYID(req)
	if !ok {
		return fail
	}
	var params struct {
		Cols int `json:"cols"`
		Rows int `json:"rows"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errResp(req.ID, CodeInvalidParams, "invalid params")
	}
	if params.Cols <= 0 || params.Rows <= 0 {
		return errResp(req.ID, CodeInvalidParams, "cols and rows must both be positive")
	}
	if s.attachCount(id) > 0 {
		return Response{ID: req.ID, Result: map[string]any{
			"type": "pty_resize", "id": id, "cols": params.Cols, "rows": params.Rows,
			"applied": false, "reason": "attached",
		}}
	}
	if err := s.pty.Resize(id, params.Cols, params.Rows); err != nil {
		return s.ptyErrResp(req.ID, err)
	}
	return Response{ID: req.ID, Result: map[string]any{
		"type": "pty_resize", "id": id, "cols": params.Cols, "rows": params.Rows,
		"applied": true,
	}}
}

func (s *Server) beginAttach(id string) {
	s.attachMu.Lock()
	if s.attaches == nil {
		s.attaches = map[string]int{}
	}
	s.attaches[id]++
	s.attachMu.Unlock()
}

func (s *Server) endAttach(id string) {
	s.attachMu.Lock()
	s.attaches[id]--
	if s.attaches[id] <= 0 {
		delete(s.attaches, id)
	}
	s.attachMu.Unlock()
}

func (s *Server) attachCount(id string) int {
	s.attachMu.Lock()
	defer s.attachMu.Unlock()
	return s.attaches[id]
}

func (s *Server) ptyErrResp(id string, err error) Response {
	if errors.Is(err, ptyruntime.ErrNotFound) {
		return errResp(id, CodeNotFound, err.Error())
	}
	return errResp(id, CodeInvalidParams, err.Error())
}

// handlePTYAttach answers once, then streams raw output to the connection and
// consumes framed client messages (input + live resize) until either side
// closes.
func (s *Server) handlePTYAttach(ctx context.Context, conn io.ReadWriter, req Request) {
	var params struct {
		ID   string `json:"id"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil || params.ID == "" {
		writeResponse(conn, errResp(req.ID, CodeInvalidParams, "id is required"))
		return
	}
	replay, live, unsub, err := s.pty.Subscribe(params.ID)
	if err != nil {
		writeResponse(conn, s.ptyErrResp(req.ID, err))
		return
	}
	defer unsub()
	s.beginAttach(params.ID)
	defer s.endAttach(params.ID)

	if params.Cols > 0 && params.Rows > 0 {
		_ = s.pty.Resize(params.ID, params.Cols, params.Rows)
	}

	writeResponse(conn, Response{ID: req.ID, Result: map[string]any{"type": "pty_attached"}})

	// Replay the scrollback synchronously before streaming live output so the
	// attaching client sees a coherent pane from byte one.
	for len(replay) > 0 {
		n, werr := conn.Write(replay)
		if werr != nil {
			return
		}
		replay = replay[n:]
	}

	// Connection -> session (framed: input + resize).
	go handlePTYAttachConnInput(ctx, params.ID, func(data []byte) error {
		return s.pty.Write(params.ID, data)
	}, func(cols, rows int) error {
		return s.pty.Resize(params.ID, cols, rows)
	}, conn)

	// Live -> connection (output).
	for {
		select {
		case <-ctx.Done():
			return
		case chunk, ok := <-live:
			if !ok {
				return
			}
			if _, werr := conn.Write(chunk); werr != nil {
				return
			}
		}
	}
}

func writeResponse(conn io.Writer, resp Response) {
	out, err := EncodeResponse(resp)
	if err != nil {
		return
	}
	_, _ = conn.Write(out)
}

func tailLines(text string, lines int) string {
	parts := strings.Split(text, "\n")
	if len(parts) > lines {
		return strings.Join(parts[len(parts)-lines:], "\n")
	}
	return text
}

// isPTYAttach reports whether a request line switches this connection to raw
// relay mode.
func isPTYAttach(line []byte) bool {
	var probe struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(line, &probe); err != nil {
		return false
	}
	return probe.Method == "pty.attach"
}

// handlePTYAttachConn wraps handlePTYAttach with the net.Conn typed context
// cancellation the raw loop needs.
func (s *Server) handlePTYAttachConn(ctx context.Context, conn net.Conn, line []byte) {
	var req Request
	if err := json.Unmarshal(line, &req); err != nil {
		writeResponse(conn, errResp("", CodeInvalidParams, err.Error()))
		return
	}
	s.handlePTYAttach(ctx, conn, req)
}
