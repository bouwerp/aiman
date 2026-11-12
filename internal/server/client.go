package server

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Client calls one serve socket under a fixed caller. The zero Caller sends
// none, whatever the process environment holds, so a process that speaks for
// someone other than the session it may have been started from (the gateway,
// for the phone) uses a Client instead of the package functions below, which
// take the caller from AIMAN_ID.
type Client struct {
	Socket string
	Caller string
}

// envCaller is the caller of a process running as a session's own CLI.
func envCaller() string {
	return strings.TrimSpace(os.Getenv("AIMAN_ID"))
}

func Call(socketPath, method string, params any) (Response, error) {
	if _, err := os.Stat(socketPath); err != nil {
		return Response{}, fmt.Errorf("%w: %s", ErrServerNotRunning, socketPath)
	}
	rawParams := []byte("{}")
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return Response{}, err
		}
		rawParams = b
	}
	return Client{Socket: socketPath, Caller: envCaller()}.callRawWait(method, rawParams, callDeadlineFor(method, rawParams))
}

// AttachConn is a bidirectional channel to a PTY session: output arrives as
// raw bytes, input goes out as frames (see attach_framing.go).
type AttachConn struct {
	conn net.Conn
	// r is the handshake reader. The confirmation line and the first TUI frame
	// often arrive in one packet; a throwaway bufio.Reader would keep the
	// frame in its buffer and Relay would never see it.
	r   *bufio.Reader
	wmu sync.Mutex
}

// AttachDial opens an attach stream to a PTY session. After the single
// confirmation line the connection carries raw terminal bytes only.
func AttachDial(socketPath, id string, cols, rows int) (*AttachConn, error) {
	return Client{Socket: socketPath, Caller: envCaller()}.AttachDial(id, cols, rows)
}

// AttachDial opens an attach stream under c's caller.
func (c Client) AttachDial(id string, cols, rows int) (*AttachConn, error) {
	if _, err := os.Stat(c.Socket); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrServerNotRunning, c.Socket)
	}
	conn, err := net.DialTimeout("unix", c.Socket, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServerNotRunning, err)
	}
	req := Request{
		ID:     uuid.NewString(),
		Method: "pty.attach",
		Params: []byte(fmt.Sprintf(`{"id":%q,"cols":%d,"rows":%d}`, id, cols, rows)),
		Caller: c.Caller,
	}
	body, err := json.Marshal(req)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if _, err := conn.Write(append(body, '\n')); err != nil {
		_ = conn.Close()
		return nil, err
	}
	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if resp.Error != nil {
		_ = conn.Close()
		return nil, errors.New(resp.Error.Message)
	}
	return &AttachConn{conn: conn, r: r}, nil
}

// frameWriter turns a raw byte stream into input frames.
type frameWriter struct {
	conn io.Writer
	mu   *sync.Mutex
}

func (f frameWriter) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := writeAttachFrame(f.conn, attachFrameInput, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Read copies raw terminal output from the session. Attach output is not framed.
func (a *AttachConn) Read(p []byte) (int, error) {
	return a.out().Read(p)
}

func (a *AttachConn) out() io.Reader {
	if a.r != nil {
		return a.r
	}
	return a.conn
}

// WriteInput sends raw terminal bytes as an input frame.
func (a *AttachConn) WriteInput(p []byte) error {
	a.wmu.Lock()
	defer a.wmu.Unlock()
	return writeAttachFrame(a.conn, attachFrameInput, p)
}

// Resize updates the remote session's window size.
func (a *AttachConn) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 || cols > 0xFFFF || rows > 0xFFFF {
		return fmt.Errorf("pty attach: invalid window size %dx%d", cols, rows)
	}
	payload := make([]byte, 4)
	binary.BigEndian.PutUint16(payload[:2], uint16(cols)) //nolint:gosec // G115: bounds-checked above
	binary.BigEndian.PutUint16(payload[2:], uint16(rows)) //nolint:gosec // G115: bounds-checked above
	a.wmu.Lock()
	defer a.wmu.Unlock()
	return writeAttachFrame(a.conn, attachFrameResize, payload)
}

// Relay shuttles bytes between in/out and the session until the connection
// closes or the session ends.
func (a *AttachConn) Relay(in io.Reader, out io.Writer) error {
	errCh := make(chan error, 2)
	go func() {
		_, err := io.Copy(frameWriter{conn: a.conn, mu: &a.wmu}, in)
		errCh <- err
	}()
	go func() {
		_, err := io.Copy(out, a.out())
		errCh <- err
	}()
	err := <-errCh
	_ = a.conn.Close() // the stream is done either way; close errors are moot
	return err
}

// Close tears down the attach stream.
func (a *AttachConn) Close() error { return a.conn.Close() }

// CallRaw is Call with caller-provided params JSON.
func CallRaw(socketPath, method string, rawParams json.RawMessage) (Response, error) {
	return CallRawWait(socketPath, method, rawParams, callDeadlineFor(method, rawParams))
}

// CallRawWait is CallRaw with an explicit wait. wait <= 0 uses callDeadlineFor.
func CallRawWait(socketPath, method string, rawParams json.RawMessage, wait time.Duration) (Response, error) {
	return Client{Socket: socketPath, Caller: envCaller()}.callRawWait(method, rawParams, wait)
}

// CallRaw calls method under c's caller, waiting callDeadlineFor for the answer.
func (c Client) CallRaw(method string, rawParams json.RawMessage) (Response, error) {
	return c.callRawWait(method, rawParams, callDeadlineFor(method, rawParams))
}

// maxRetryAfter bounds how long the client sleeps on a retry_after_ms.
const maxRetryAfter = 5 * time.Second

// callRawWait sends one request, and once more after retry_after_ms when serve
// answers server_busy or rate_limited. A second refusal is returned with a
// message that says which it was.
func (c Client) callRawWait(method string, rawParams json.RawMessage, wait time.Duration) (Response, error) {
	if wait <= 0 {
		wait = callDeadlineFor(method, rawParams)
	}
	resp, err := c.callOnce(method, rawParams, wait)
	if err != nil || !retryable(resp.Error) {
		return resp, err
	}
	time.Sleep(retryAfter(resp.Error))
	resp, err = c.callOnce(method, rawParams, wait)
	if err == nil && retryable(resp.Error) {
		resp.Error.Message = refusalMessage(resp.Error)
	}
	return resp, err
}

func retryable(e *Error) bool {
	return e != nil && (e.Code == CodeServerBusy || e.Code == CodeRateLimited)
}

// retryAfter reads data.retry_after_ms, a second when it is missing.
func retryAfter(e *Error) time.Duration {
	d := time.Second
	if b, err := json.Marshal(e.Data); err == nil {
		var data RetryAfterData
		if json.Unmarshal(b, &data) == nil && data.RetryAfterMS > 0 {
			d = time.Duration(data.RetryAfterMS) * time.Millisecond
		}
	}
	return min(d, maxRetryAfter)
}

// refusalMessage is the message of a refusal, led by what it was.
func refusalMessage(e *Error) string {
	lead := "rate limited"
	if e.Code == CodeServerBusy {
		lead = "server busy"
	}
	if strings.Contains(strings.ToLower(e.Message), lead) {
		return e.Message
	}
	return lead + ": " + e.Message
}

func (c Client) callOnce(method string, rawParams json.RawMessage, wait time.Duration) (Response, error) {
	req := Request{
		ID:     uuid.NewString(),
		Method: method,
		Params: rawParams,
		Caller: c.Caller,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	conn, err := net.DialTimeout("unix", c.Socket, 2*time.Second)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrServerNotRunning, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(wait))
	if _, err := conn.Write(append(body, '\n')); err != nil {
		return Response{}, err
	}
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return Response{}, err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}

// EventsConn is a live stream of session activity changes.
type EventsConn struct {
	conn net.Conn
	r    *bufio.Reader
}

// EventsDial opens the session event stream. The connection stays open and
// carries one JSON event per line, so a reader learns what a session is doing
// when it happens rather than by asking twice a second.
func EventsDial(socketPath string) (*EventsConn, error) {
	return Client{Socket: socketPath, Caller: envCaller()}.EventsDial()
}

// EventsDial opens the session event stream under c's caller.
func (c Client) EventsDial() (*EventsConn, error) {
	if _, err := os.Stat(c.Socket); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrServerNotRunning, c.Socket)
	}
	conn, err := net.DialTimeout("unix", c.Socket, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServerNotRunning, err)
	}
	req := Request{
		ID:     uuid.NewString(),
		Method: "session.events",
		Caller: c.Caller,
	}
	body, err := json.Marshal(req)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if _, err := conn.Write(append(body, '\n')); err != nil {
		_ = conn.Close()
		return nil, err
	}
	r := bufio.NewReader(conn)
	line, err := r.ReadBytes('\n')
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if resp.Error != nil {
		_ = conn.Close()
		return nil, errors.New(resp.Error.Message)
	}
	return &EventsConn{conn: conn, r: r}, nil
}

// Next blocks for the next event. The stream is deliberately unbounded in time,
// so callers set no read deadline; a dead connection surfaces as a read error.
func (e *EventsConn) Next() (SessionEvent, error) {
	var ev SessionEvent
	line, err := e.r.ReadBytes('\n')
	if err != nil {
		return ev, err
	}
	if err := json.Unmarshal(bytesTrimSpace(line), &ev); err != nil {
		return ev, err
	}
	return ev, nil
}

// Close ends the stream.
func (e *EventsConn) Close() error { return e.conn.Close() }

// DefaultWaitTimeout is how long serve waits for a state when a wait names no
// timeout_ms.
const DefaultWaitTimeout = 120 * time.Second

// unboundedWaitDeadline is the client deadline for a wait with timeout_ms <= 0,
// which serve runs with no limit.
const unboundedWaitDeadline = 7 * 24 * time.Hour

// callDeadlineFor is callDeadline, except for a call that waits on a session
// state (session.wait, and session.prompt with wait): its deadline follows
// the wait asked for, 15s past it and never under 30s.
func callDeadlineFor(method string, params json.RawMessage) time.Duration {
	var p struct {
		Wait      bool `json:"wait"`
		TimeoutMS *int `json:"timeout_ms"`
	}
	parsed := json.Unmarshal(params, &p) == nil
	switch {
	case method == "session.wait":
	case method == "session.prompt" && parsed && p.Wait:
	default:
		return callDeadline(method)
	}
	wait := DefaultWaitTimeout
	if parsed && p.TimeoutMS != nil {
		if *p.TimeoutMS <= 0 {
			return unboundedWaitDeadline
		}
		wait = time.Duration(*p.TimeoutMS) * time.Millisecond
	}
	return max(30*time.Second, wait+15*time.Second)
}

// callDeadline is how long CallRaw waits for method's answer: 30s, or a
// little past the server's own limit for the slow methods.
func callDeadline(method string) time.Duration {
	d, _ := slowCall(method)
	return d
}

// longCall reports whether method is a long call the host update gate waits
// for: a slow method that changes state midway, so not the read-only repo.list
// and aws.status.
func longCall(method string) bool {
	_, long := slowCall(method)
	return long
}

// slowCall is the one list of slow methods: the client deadline for method and
// whether a restart in the middle of it would leave half-written state.
// handover.write keeps the default deadline and is listed for the gate.
func slowCall(method string) (time.Duration, bool) {
	switch method {
	case "session.create", "review.fix", "review.assess":
		return SessionCreateWait, true
	case "session.restart":
		return SessionRestartTimeout + 15*time.Second, true
	case "session.terminate":
		return SessionTerminateTimeout + 15*time.Second, true
	case "session.land":
		return SessionLandTimeout + 15*time.Second, true
	case "repo.list":
		return RepoListTimeout + 15*time.Second, false
	case "design.publish":
		return DesignPublishTimeout + 15*time.Second, true
	case "aws.status":
		return AWSStatusTimeout + 15*time.Second, false
	case "aws.login_start":
		return AWSLoginStartTimeout + 15*time.Second, true
	case "review.post", "review.push", "review.approve":
		return ReviewPostTimeout + 15*time.Second, true
	case "review.run", "review.coverage":
		return ReviewRunTimeout + 15*time.Second, true
	case "handover.write":
		return 30 * time.Second, true
	}
	return 30 * time.Second, false
}
