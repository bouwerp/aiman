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
	req := Request{
		ID:     uuid.NewString(),
		Method: method,
		Params: rawParams,
		Caller: strings.TrimSpace(os.Getenv("AIMAN_ID")),
	}
	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrServerNotRunning, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
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
	if _, err := os.Stat(socketPath); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrServerNotRunning, socketPath)
	}
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServerNotRunning, err)
	}
	req := Request{
		ID:     uuid.NewString(),
		Method: "pty.attach",
		Params: []byte(fmt.Sprintf(`{"id":%q,"cols":%d,"rows":%d}`, id, cols, rows)),
		Caller: strings.TrimSpace(os.Getenv("AIMAN_ID")),
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
	req := Request{
		ID:     uuid.NewString(),
		Method: method,
		Params: rawParams,
		Caller: strings.TrimSpace(os.Getenv("AIMAN_ID")),
	}
	body, err := json.Marshal(req)
	if err != nil {
		return Response{}, err
	}
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return Response{}, fmt.Errorf("%w: %v", ErrServerNotRunning, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
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
	if _, err := os.Stat(socketPath); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrServerNotRunning, socketPath)
	}
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrServerNotRunning, err)
	}
	req := Request{
		ID:     uuid.NewString(),
		Method: "session.events",
		Caller: strings.TrimSpace(os.Getenv("AIMAN_ID")),
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
