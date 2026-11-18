package edge

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"time"
)

// The control directory is the run's control/ directory, mounted only in the
// edge. The socket and the token file live directly in it.
const (
	SocketName = "edge.sock"
	TokenName  = "token"
)

// Control operations.
const (
	OpAdd    = "add"
	OpRemove = "remove"
	OpList   = "list"
	OpHealth = "health"
	// OpDAPDrop closes the client holding a DAP relay (take over, release).
	OpDAPDrop = "dap_drop"
	// OpDAPTarget points a DAP relay at another target (Request.Target),
	// closing its holder: the debug target switch between the app and a
	// debug check.
	OpDAPTarget = "dap_target"
	// OpCapture reads the capture proxy's flows after a flow ID.
	OpCapture = "capture"
	// OpCA reads the run CA's certificate (never its key).
	OpCA = "ca"
	// OpAWSCalls reads the AWS tap's call log after a call ID.
	OpAWSCalls = "aws_calls"
)

// Error codes in a control reply.
const (
	CodeUnauthorized  = "unauthorized"
	CodeInvalidParams = "invalid_params"
	CodeExists        = "exists"
	CodeNotFound      = "not_found"
	CodeBindFailed    = "bind_failed"
	CodeUnavailable   = "unavailable"
)

const (
	maxRequestBytes = 64 << 10
	idleTimeout     = 30 * time.Second
)

// Request is one line on the control socket. Every request carries the token.
type Request struct {
	Op    string     `json:"op"`
	Token string     `json:"token,omitempty"`
	Relay *RelaySpec `json:"relay,omitempty"`
	Name  string     `json:"name,omitempty"`
	// Target is OpDAPTarget's new host:port, empty for none.
	Target string `json:"target,omitempty"`
	// After and Limit page a capture read.
	After int64 `json:"after,omitempty"`
	Limit int   `json:"limit,omitempty"`
}

// Response is the edge's one-line answer to a Request.
type Response struct {
	OK     bool          `json:"ok"`
	Error  *Error        `json:"error,omitempty"`
	Relay  *RelayStatus  `json:"relay,omitempty"`
	Relays []RelayStatus `json:"relays,omitempty"`
	Health *Health       `json:"health,omitempty"`
	// Dropped is the DAP client a dap_drop closed.
	Dropped *DAPHolder `json:"dropped,omitempty"`
	// Flows and Capture answer a capture read.
	Flows   []Flow       `json:"flows,omitempty"`
	Capture *CaptureInfo `json:"capture,omitempty"`
	// CA is the run CA's certificate, PEM encoded.
	CA string `json:"ca,omitempty"`
	// AWSCalls and AWSInfo answer an aws_calls read.
	AWSCalls []AWSCall     `json:"aws_calls,omitempty"`
	AWSInfo  *AWSCallsInfo `json:"aws_info,omitempty"`
}

// Error is a control failure with a stable code.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ListenControl listens on the control socket at path, replacing a stale file
// left by a previous edge, and makes the socket owner-only.
func ListenControl(path string) (net.Listener, error) {
	if fi, err := os.Lstat(path); err == nil {
		if fi.IsDir() {
			return nil, fmt.Errorf("control socket path %s is a directory", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale control socket: %w", err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("stat control socket: %w", err)
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on control socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, fmt.Errorf("chmod control socket: %w", err)
	}
	return ln, nil
}

// ReadTokenFile reads the control token, trimmed of surrounding whitespace.
func ReadTokenFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path) //nolint:gosec // G304: the path is the operator's --token-file
	if err != nil {
		return nil, fmt.Errorf("read token file: %w", err)
	}
	tok := bytes.TrimSpace(b)
	if len(tok) < MinTokenBytes {
		return nil, fmt.Errorf("token file %s holds fewer than %d bytes", path, MinTokenBytes)
	}
	return tok, nil
}

// ServeControl answers control connections on ln until ln is closed.
func (e *Edge) ServeControl(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept control connection: %w", err)
		}
		go e.serveConn(c)
	}
}

// serveConn answers requests on one connection, one JSON line each. A request
// that fails the token check gets one unauthorized reply and the connection
// ends, so each guess costs a new dial.
func (e *Edge) serveConn(c net.Conn) {
	defer func() { _ = c.Close() }()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 0, 4096), maxRequestBytes)
	enc := json.NewEncoder(c)
	for {
		_ = c.SetReadDeadline(time.Now().Add(idleTimeout))
		if !sc.Scan() {
			return
		}
		resp, keep := e.handleLine(sc.Bytes())
		_ = c.SetWriteDeadline(time.Now().Add(idleTimeout))
		if err := enc.Encode(resp); err != nil || !keep {
			return
		}
	}
}

func (e *Edge) handleLine(line []byte) (Response, bool) {
	var req Request
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return fail(errorf(CodeInvalidParams, "bad request: %v", err)), false
	}
	if !e.tokenOK(req.Token) {
		return fail(errorf(CodeUnauthorized, "bad or missing token")), false
	}
	return e.dispatch(req), true
}

// tokenOK compares sha256 digests in constant time, so neither the content
// nor the length of the token leaks through timing.
func (e *Edge) tokenOK(got string) bool {
	want := sha256.Sum256(e.token)
	have := sha256.Sum256([]byte(got))
	return got != "" && subtle.ConstantTimeCompare(want[:], have[:]) == 1
}

func (e *Edge) dispatch(req Request) Response {
	switch req.Op {
	case OpAdd:
		if req.Relay == nil {
			return fail(errorf(CodeInvalidParams, "add needs a relay"))
		}
		st, err := e.Add(*req.Relay)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Relay: &st}
	case OpRemove:
		if err := e.Remove(req.Name); err != nil {
			return fail(err)
		}
		return Response{OK: true}
	case OpList:
		return Response{OK: true, Relays: e.List()}
	case OpHealth:
		h := e.Health()
		return Response{OK: true, Health: &h}
	case OpDAPDrop:
		dropped, err := e.DropDAP(req.Name)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Dropped: dropped}
	case OpDAPTarget:
		dropped, err := e.SetDAPTarget(req.Name, req.Target)
		if err != nil {
			return fail(err)
		}
		return Response{OK: true, Dropped: dropped}
	case OpCapture, OpCA:
		p := e.captureProxy()
		if p == nil {
			return fail(errorf(CodeUnavailable, "this edge runs no capture proxy"))
		}
		if req.Op == OpCA {
			return Response{OK: true, CA: string(p.CACertPEM())}
		}
		flows, info := p.Capture(req.After, req.Limit)
		return Response{OK: true, Flows: flows, Capture: &info}
	case OpAWSCalls:
		t := e.awsCallTap()
		if t == nil {
			return fail(errorf(CodeUnavailable, "this edge runs no aws tap"))
		}
		calls, info := t.Calls(req.After, req.Limit)
		return Response{OK: true, AWSCalls: calls, AWSInfo: &info}
	default:
		return fail(errorf(CodeInvalidParams, "unknown op %q", req.Op))
	}
}

func fail(err error) Response {
	var ce *Error
	if !errors.As(err, &ce) {
		ce = &Error{Code: CodeUnavailable, Message: err.Error()}
	}
	return Response{Error: ce}
}
