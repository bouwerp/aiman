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
//
// Any startup failure is recorded in the exit file before returning. The
// holder is spawned detached with its stdio discarded, so a bare error return
// is invisible to everyone: the manager had already accepted the session as
// started (meta.json is written before the socket is bound), so a later
// failure surfaced only as an unexplained "exited" status. Writing the reason
// down is what makes it diagnosable — a too-long socket path on macOS, for
// instance, reported nothing at all before this.
func Run(root, id string) (err error) {
	dir := Dir(root, id)
	defer func() {
		if err != nil {
			_ = writeFileAtomic(filepath.Join(dir, ExitFile), []byte("holder failed to start: "+err.Error()))
		}
	}()
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
	cmd := exec.Command("bash", "-l", "-c", childShell(spec.Command))
	cmd.Dir = spec.Dir
	// The holder allocates a real PTY, so the child is entitled to a terminal
	// type — but the holder is spawned by aiman serve, a daemon with no tty and
	// therefore no TERM to inherit. Without one, agents fall back to a dumb
	// terminal and emit no colour at all. tmux does the same thing for its own
	// panes; this is the PTY backend's equivalent. Anything explicit in the
	// spec still wins.
	cmd.Env = envMap(withTerminalEnv(os.Environ()), spec.Env)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}) //nolint:gosec // G115: bounded below
	if err != nil {
		return fmt.Errorf("holder: start: %w", err)
	}
	defer ptmx.Close()

	meta := Meta{
		ID:      spec.ID,
		Name:    spec.Name,
		Dir:     spec.Dir,
		Command: spec.Command,
		PID:     cmd.Process.Pid,
		Started: time.Now().UTC().Format(time.RFC3339),
		Size:    fmt.Sprintf("%dx%d", cols, rows),
	}
	if err := writeMeta(dir, meta); err != nil {
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

	// PTY -> spool + clients, recording what the output says about the session
	// as it passes. Doing it here is what makes it free: the bytes are already
	// in hand, and the alternative is replaying the whole spool through an
	// emulator later and pattern-matching the result.
	act := newActivityTracker(dir)
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
				act.observe(data)
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
			if applyResizeFile(dir, ptmx, &meta) {
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
	act.flush()
	_ = writeFileAtomic(filepath.Join(dir, ExitFile), []byte(exitLine))
	_ = os.Remove(filepath.Join(dir, MetaFile))
	_ = os.Remove(filepath.Join(dir, KillFile))
	_ = os.Remove(filepath.Join(dir, ResizeFile))
	_ = os.Remove(filepath.Join(dir, ActivityFile))
	_ = os.Remove(sockPath)
	return nil
}

func terminate(proc *os.Process) {
	_ = proc.Signal(syscall.SIGTERM)
}

// writeMeta atomically publishes the session's metadata.
func writeMeta(dir string, meta Meta) error {
	raw, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, MetaFile), raw)
}

// applyResizeFile consumes a resize marker if present, recording the new size
// in meta so callers can read back the size they asked for.
func applyResizeFile(dir string, ptmx *os.File, meta *Meta) bool {
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
			curCols, curRows := 0, 0
			if meta != nil {
				curCols, curRows = parseWxH(meta.Size)
			}
			if nc, nr, ok := resizeNudge(cols, rows, curCols, curRows); ok {
				_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(nc), Rows: uint16(nr)}) //nolint:gosec // G115: nudge is in-range
			}
			_ = pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)}) //nolint:gosec // G115: bounds-checked above
			if meta != nil {
				meta.Size = fmt.Sprintf("%dx%d", cols, rows)
				_ = writeMeta(dir, *meta)
			}
		}
	}
	_ = os.Remove(path)
	return true
}

// resizeNudge is a one-cell size change applied before a TIOCSWINSZ that
// would otherwise be a no-op. The kernel does not SIGWINCH when the winsize
// is unchanged, so a reattach that clears the client tty would leave Grok
// (and other CUP TUIs) without a chrome redraw.
func resizeNudge(cols, rows, curCols, curRows int) (int, int, bool) {
	if cols != curCols || rows != curRows || cols <= 0 || rows <= 0 {
		return 0, 0, false
	}
	if cols > 1 {
		return cols - 1, rows, true
	}
	if rows > 1 {
		return cols, rows - 1, true
	}
	return 2, 1, true
}

func parseWxH(s string) (int, int) {
	parts := strings.SplitN(strings.TrimSpace(s), "x", 2)
	if len(parts) != 2 {
		return 0, 0
	}
	cols, err1 := strconv.Atoi(parts[0])
	rows, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return 0, 0
	}
	return cols, rows
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

// defaultTERM is what the holder gives a child that inherited none. 256-colour
// xterm is the safe common denominator: every agent CLI recognises it, and it
// matches what tmux hands its own panes.
const defaultTERM = "xterm-256color"

// childShell is the login-shell script that runs the agent inside the PTY.
// TERM is exported here, after profile scripts, because bash -l can clobber
// the holder process environment with TERM=dumb. Codex 0.150 then refuses
// its TUI and the trailing exec bash is what an attach shows.
func childShell(command string) string {
	return fmt.Sprintf(
		"export PATH=\"$PATH:$HOME/.local/bin:$HOME/.npm-global/bin:$HOME/bin:$HOME/.bun/bin:$HOME/.local/share/pnpm:$HOME/.pnpm:$HOME/.yarn/bin:$HOME/.cargo/bin:/usr/local/bin:/opt/homebrew/bin:$HOME/.opencode/bin\"; export TERM=%s COLORTERM=truecolor LANG=\"${LANG:-%s}\"; %s; exec bash -i",
		defaultTERM, defaultUTF8Locale, command,
	)
}

// withTerminalEnv ensures TERM, COLORTERM, and a UTF-8 locale are present,
// without overriding usable values that are already set.
func withTerminalEnv(base []string) []string {
	haveTERM, haveColor, haveUTF8 := false, false, false
	for _, kv := range base {
		switch {
		case strings.HasPrefix(kv, "TERM="):
			// An inherited TERM=dumb is worse than none: it actively tells
			// agents to disable colour and cursor addressing.
			if strings.TrimSpace(strings.TrimPrefix(kv, "TERM=")) != "" &&
				strings.TrimPrefix(kv, "TERM=") != "dumb" {
				haveTERM = true
			}
		case strings.HasPrefix(kv, "COLORTERM="):
			haveColor = true
		case isLocaleKey(kv) && localeValueIsUTF8(kv):
			haveUTF8 = true
		}
	}
	out := make([]string, 0, len(base)+3)
	for _, kv := range base {
		if !haveTERM && strings.HasPrefix(kv, "TERM=") {
			continue // drop the unusable value so envMap's later entry wins
		}
		if !haveUTF8 && isLocaleKey(kv) {
			continue // drop LANG=C and friends; box drawing needs UTF-8
		}
		out = append(out, kv)
	}
	if !haveTERM {
		out = append(out, "TERM="+defaultTERM)
	}
	if !haveColor {
		out = append(out, "COLORTERM=truecolor")
	}
	if !haveUTF8 {
		out = append(out, "LANG="+defaultUTF8Locale)
	}
	return out
}

const defaultUTF8Locale = "C.UTF-8"

func isLocaleKey(kv string) bool {
	return strings.HasPrefix(kv, "LANG=") ||
		strings.HasPrefix(kv, "LC_ALL=") ||
		strings.HasPrefix(kv, "LC_CTYPE=")
}

func localeValueIsUTF8(kv string) bool {
	eq := strings.IndexByte(kv, '=')
	if eq < 0 {
		return false
	}
	v := strings.ToLower(strings.TrimSpace(kv[eq+1:]))
	return strings.Contains(v, "utf-8") || strings.Contains(v, "utf8")
}
