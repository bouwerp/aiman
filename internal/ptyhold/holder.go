package ptyhold

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	pty "github.com/creack/pty/v2"
)

// controlPollInterval is how often the holder checks the resize/kill marker
// files. File-based control keeps the socket a pure byte pipe; latency of one
// poll tick is invisible to a human.
const controlPollInterval = 150 * time.Millisecond

// killGrace mirrors ptyruntime.DefaultKillGrace.
const killGrace = 3 * time.Second

// Run executes the holder for the session dir under root. It blocks until the
// child exits or a kill marker is honoured, then performs exit cleanup. This
// function IS the holder: keep it minimal and boring.
func Run(root, id string) error {
	dir := Dir(root, id)
	raw, err := os.ReadFile(filepath.Join(dir, RequestFile))
	if err != nil {
		return fmt.Errorf("holder: read request: %w", err)
	}
	var spec Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return fmt.Errorf("holder: parse request: %w", err)
	}
	if spec.Command == "" {
		return fmt.Errorf("holder: empty command")
	}
	cols, rows := spec.Cols, spec.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}

	//nolint:gosec // G204: running the operator-configured agent command is the purpose of this program
	cmd := exec.Command("bash", "-l", "-c",
		fmt.Sprintf("export PATH=\"$PATH:$HOME/.local/bin:$HOME/.npm-global/bin:$HOME/bin:$HOME/.bun/bin:$HOME/.local/share/pnpm:$HOME/.pnpm:$HOME/.yarn/bin:$HOME/.cargo/bin:/usr/local/bin:/opt/homebrew/bin:$HOME/.opencode/bin\"; %s; exec bash -i", spec.Command))
	cmd.Dir = spec.Dir
	cmd.Env = envMap(os.Environ(), spec.Env)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}) //nolint:gosec // G115: bounded below
	if err != nil {
		return fmt.Errorf("holder: start: %w", err)
	}
	defer ptmx.Close()

	meta, _ := json.Marshal(Meta{
		ID:      spec.ID,
		Name:    spec.Name,
		Dir:     spec.Dir,
		Command: spec.Command,
		PID:     cmd.Process.Pid,
		Started: time.Now().UTC().Format(time.RFC3339),
	})
	if err := writeFileAtomic(filepath.Join(dir, MetaFile), meta); err != nil {
		return fmt.Errorf("holder: write meta: %w", err)
	}

	sockPath := filepath.Join(dir, SocketFile)
	_ = os.Remove(sockPath) // stale socket from a crashed predecessor
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("holder: listen: %w", err)
	}
	defer ln.Close()

	hub := newFanout()
	spool := newSpoolWriter(dir)

	// PTY -> spool + clients.
	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		buf := make([]byte, 16<<10)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				data := append([]byte(nil), buf[:n]...)
				hub.broadcast(data)
				spool.write(data)
			}
			if rerr != nil {
				return
			}
		}
	}()

	// Clients: raw byte pipe both ways.
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			rx := hub.attach()
			go func() {
				defer conn.Close()
				for chunk := range rx {
					if _, werr := conn.Write(chunk); werr != nil {
						return
					}
				}
			}()
			go func() {
				buf := make([]byte, 4<<10)
				for {
					n, rerr := conn.Read(buf)
					if n > 0 {
						if _, werr := ptmx.Write(buf[:n]); werr != nil {
							return
						}
					}
					if rerr != nil {
						return
					}
				}
			}()
		}
	}()

	// Control files + child wait.
	ticker := time.NewTicker(controlPollInterval)
	defer ticker.Stop()
	childDone := make(chan error, 1)
	go func() { childDone <- cmd.Wait() }()

	exitLine := "exit=0"
	for {
		select {
		case werr := <-childDone:
			if werr != nil {
				exitLine = "exit=" + werr.Error()
			}
			goto cleanup
		case <-ticker.C:
			if applyResizeFile(dir, ptmx) {
				continue
			}
			if _, kerr := os.Stat(filepath.Join(dir, KillFile)); kerr == nil {
				terminate(cmd.Process)
				select {
				case werr := <-childDone:
					if werr != nil {
						exitLine = "exit=" + werr.Error()
					}
				case <-time.After(killGrace + 2*time.Second):
					_ = cmd.Process.Kill()
					<-childDone
					exitLine = "exit=killed"
				}
				goto cleanup
			}
		}
	}

cleanup:
	// Order matters for observers: stop accepting first so no client sees the
	// world after status flips.
	_ = ln.Close()
	hub.closeAll()
	<-outputDone
	spool.close()
	_ = writeFileAtomic(filepath.Join(dir, ExitFile), []byte(exitLine))
	_ = os.Remove(filepath.Join(dir, MetaFile))
	_ = os.Remove(filepath.Join(dir, KillFile))
	_ = os.Remove(filepath.Join(dir, ResizeFile))
	_ = os.Remove(sockPath)
	return nil
}

func terminate(proc *os.Process) {
	_ = proc.Signal(syscall.SIGTERM)
}

// applyResizeFile consumes a resize marker if present.
func applyResizeFile(dir string, ptmx *os.File) bool {
	path := filepath.Join(dir, ResizeFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	parts := strings.SplitN(strings.TrimSpace(string(raw)), "x", 2)
	if len(parts) == 2 {
		cols, c1 := strconv.Atoi(parts[0])
		rows, c2 := strconv.Atoi(parts[1])
		if c1 == nil && c2 == nil && cols > 0 && rows > 0 && cols <= 0xFFFF && rows <= 0xFFFF {
			_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}) //nolint:gosec // G115: bounds-checked above
		}
	}
	_ = os.Remove(path)
	return true
}

// fanout broadcasts live output to attached readers and pipes each client's
// input back through its returned writer.
type fanout struct {
	mu      sync.Mutex
	readers map[chan []byte]struct{}
	done    chan struct{}
}

func newFanout() *fanout {
	return &fanout{
		readers: make(map[chan []byte]struct{}),
		done:    make(chan struct{}),
	}
}

func (h *fanout) attach() <-chan []byte {
	rx := make(chan []byte, 256)
	h.mu.Lock()
	h.readers[rx] = struct{}{}
	h.mu.Unlock()
	return rx
}

func (h *fanout) broadcast(data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for rx := range h.readers {
		select {
		case rx <- data:
		default: // slow client: drop rather than stall the PTY
		}
	}
}

func (h *fanout) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case <-h.done:
		return
	default:
	}
	close(h.done)
	for rx := range h.readers {
		close(rx)
		delete(h.readers, rx)
	}
}

func envMap(base []string, extra map[string]string) []string {
	out := make([]string, 0, len(base)+len(extra))
	index := make(map[string]int, len(base)+len(extra))
	for _, kv := range base {
		if eq := indexByte(kv, '='); eq > 0 {
			index[kv[:eq]] = len(out)
		}
		out = append(out, kv)
	}
	for k, v := range extra {
		if v == "" {
			continue
		}
		full := k + "=" + v
		if at, ok := index[k]; ok {
			out[at] = full
			continue
		}
		index[k] = len(out)
		out = append(out, full)
	}
	return out
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
