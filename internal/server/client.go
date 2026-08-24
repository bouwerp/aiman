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
	wmu  sync.Mutex
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
	line, err := bufio.NewReader(conn).ReadBytes('\n')
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
	return &AttachConn{conn: conn}, nil
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
		_, err := io.Copy(out, a.conn)
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
