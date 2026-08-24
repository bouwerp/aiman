package ptyruntime

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	pty "github.com/creack/pty/v2"
)

// session is one live PTY: the manager owns the master side, appends output to
// a scrollback ring, and fans bytes out to attach subscribers.
type session struct {
	mu   sync.Mutex
	info SessionInfo

	cmd  *exec.Cmd
	ptmx *os.File

	ring *ringBuffer
	subs map[*subscriber]struct{}

	exited chan struct{}
}

func newSession(info SessionInfo, cmd *exec.Cmd, ptmx *os.File, scrollback int) *session {
	return &session{
		info:   info,
		cmd:    cmd,
		ptmx:   ptmx,
		ring:   newRingBuffer(scrollback),
		subs:   make(map[*subscriber]struct{}),
		exited: make(chan struct{}),
	}
}

// pump copies master-side output into the ring and every subscriber until the
// PTY closes.
func (s *session) pump() {
	buf := make([]byte, 16<<10)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			data := make([]byte, n)
			copy(data, buf[:n])
			s.ring.append(data)
			s.broadcast(data)
		}
		if err != nil {
			return
		}
	}
}

// reap marks the session exited once its process is gone, then releases the
// master. A short grace covers processes that exit slowly after the PTY read
// loop already saw EOF.
func (s *session) reap(grace time.Duration) {
	_ = s.cmd.Wait()
	if grace > 0 {
		select {
		case <-time.After(grace):
		case <-s.exited:
			return
		}
	}
	s.mu.Lock()
	s.info.Status = StatusExited
	if s.cmd.ProcessState != nil && s.cmd.ProcessState.String() != "" {
		s.info.ExitErr = s.cmd.ProcessState.String()
	}
	close(s.exited)
	s.mu.Unlock()

	s.mu.Lock()
	for sub := range s.subs {
		delete(s.subs, sub)
		sub.close()
	}
	s.mu.Unlock()
	_ = s.ptmx.Close()
}

func (s *session) broadcast(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sub := range s.subs {
		select {
		case sub.ch <- data:
		default:
			// Slow attach client: drop rather than block the PTY.
		}
	}
}

func (s *session) snapshot() SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info
}

func (s *session) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.info.Status == StatusRunning
}

func (s *session) write(data []byte) error {
	_, err := s.ptmx.Write(data)
	if errors.Is(err, os.ErrClosed) || errors.Is(err, syscall.EIO) {
		return fmt.Errorf("pty: session %s has exited", s.info.ID)
	}
	return err
}

func (s *session) resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return errors.New("pty: cols and rows must be positive")
	}
	if cols > 0xFFFF || rows > 0xFFFF {
		return errors.New("pty: cols and rows must be below 65536")
	}
	if err := pty.Setsize(s.ptmx, &pty.Winsize{Cols: clampUint16(cols), Rows: clampUint16(rows)}); err != nil {
		return fmt.Errorf("pty: resize: %w", err)
	}
	s.mu.Lock()
	s.info.Size = fmt.Sprintf("%dx%d", cols, rows)
	s.mu.Unlock()
	return nil
}

func (s *session) capture(maxBytes int) []byte {
	return s.ring.tail(maxBytes)
}

// subscribe returns a channel of live output plus everything currently in the
// ring, so an attaching client resumes exactly where the pane is.
func (s *session) subscribe(buffered chan []byte) (<-chan []byte, func(), error) {
	replay := s.ring.tail(0) // 0 means "everything retained"
	s.mu.Lock()
	if s.info.Status != StatusRunning {
		s.mu.Unlock()
		return nil, nil, fmt.Errorf("pty: session %s has exited", s.info.ID)
	}
	sub := &subscriber{ch: make(chan []byte, 256)}
	s.subs[sub] = struct{}{}
	unsub := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.subs[sub]; ok {
			delete(s.subs, sub)
			sub.close()
		}
	}
	s.mu.Unlock()

	go func() {
		chunks := splitChunks(replay, len(replay))
		for _, c := range chunks {
			select {
			case buffered <- c:
			case <-s.exited:
				return
			}
		}
	}()
	return sub.ch, unsub, nil
}

func (s *session) kill(grace time.Duration) error {
	s.mu.Lock()
	running := s.info.Status == StatusRunning
	proc := s.cmd.Process
	s.mu.Unlock()

	if !running || proc == nil {
		return fmt.Errorf("pty: session %s has exited", s.info.ID)
	}
	_ = proc.Signal(syscall.SIGTERM)

	select {
	case <-s.exited:
		return nil
	case <-time.After(grace):
		_ = proc.Kill()
		<-s.exited
		return nil
	}
}

// splitChunks yields data as a single chunk; kept as a helper so replay
// chunking policy has one home.
func splitChunks(data []byte, _ int) [][]byte {
	if len(data) == 0 {
		return nil
	}
	return [][]byte{data}
}

// Subscribe returns the retained scrollback for immediate replay plus a live
// channel of subsequent output. unsub must be called when the consumer goes
// away; it is safe to call more than once.
func (m *Manager) Subscribe(id string) ([]byte, <-chan []byte, func(), error) {
	s, ok := m.lookup(id)
	if !ok {
		return nil, nil, nil, ErrNotFound
	}
	replay := s.ring.tail(0)

	s.mu.Lock()
	if s.info.Status != StatusRunning {
		s.mu.Unlock()
		return nil, nil, nil, fmt.Errorf("pty: session %s has exited", s.info.ID)
	}
	sub := &subscriber{ch: make(chan []byte, 256)}
	s.subs[sub] = struct{}{}
	s.mu.Unlock()

	unsub := func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.subs[sub]; ok {
			delete(s.subs, sub)
			sub.close()
		}
	}
	return replay, sub.ch, unsub, nil
}
