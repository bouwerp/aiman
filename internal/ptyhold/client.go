package ptyhold

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// spawnReadyTimeout bounds how long Spawn waits for a holder to bind its
// socket. See the comment at its use for why waiting is safe.
//
// It must stay comfortably below the agent API client's 30s read deadline
// (internal/server/client.go): a pty.create request travels through that
// client, so a server-side wait as long as the client's own deadline means the
// caller gives up at the same instant and reports a bare "i/o timeout" instead
// of the real answer. Generous enough for a cold spawn on a loaded machine,
// short enough to always answer first.
const spawnReadyTimeout = 15 * time.Second

// Spawn launches a detached holder process for the spec. The holder is a child
// of the caller in name only: Setsid detaches it into its own session, so it
// survives the caller's death and is reparented to init. Returns once the
// holder signalled readiness (meta.json present) or failed (exit file present
// or timeout).
//
// holderCmd is how to invoke the holder, e.g. ["aiman", "pty", "hold"] — kept
// as a parameter so tests can point at a freshly built binary.
func Spawn(root string, spec Spec, holderCmd []string) error {
	dir := Dir(root, spec.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("ptyhold: mkdir: %w", err)
	}
	// A previous holder's leftovers would confuse this one.
	for _, f := range []string{RequestFile, ExitFile, KillFile, ResizeFile} {
		_ = os.Remove(filepath.Join(dir, f))
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(filepath.Join(dir, RequestFile), raw); err != nil {
		return fmt.Errorf("ptyhold: write request: %w", err)
	}

	cmd := exec.Command(holderCmd[0], append(holderCmd[1:], "--root", root, "--id", spec.ID)...) //nolint:gosec // G204: the operator-configured agent command travels inside the request file
	cmd.SysProcAttr = detachAttr()
	// Keep the holder's own output. Discarding it made a holder that failed
	// before it could write the exit file completely undiagnosable: all the
	// caller ever saw was "did not become ready in time", with no way to tell a
	// slow spawn from a binary that could not exec or a PTY that could not be
	// allocated. The file is small, lives beside the session, and is quoted back
	// in the timeout error below.
	logPath := filepath.Join(dir, HolderLogFile)
	if logFile, lerr := os.Create(logPath); lerr == nil {
		defer logFile.Close()
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ptyhold: spawn: %w", err)
	}
	// Do not wait on the holder; it outlives us by design.
	go func() { _, _ = cmd.Process.Wait() }()

	// Readiness means the live socket is bound, not merely that meta.json
	// exists. The holder writes meta *before* binding, so a meta-only check
	// reported success for sessions whose socket never came up — the caller
	// then saw an unexplained "exited" status instead of the real error.
	//
	// The budget is generous because it only bounds the pathological case: a
	// holder that fails outright writes the exit file and is reported on the
	// very next poll, so waiting longer never delays a real failure. It only
	// covers a cold process spawn on a loaded machine, where 5s was tight
	// enough to time out spuriously.
	deadline := time.Now().Add(spawnReadyTimeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(dir, ExitFile)); err == nil {
			line, _ := readSmallFile(filepath.Join(dir, ExitFile))
			return fmt.Errorf("ptyhold: holder exited immediately: %s", strings.TrimSpace(line))
		}
		if socketReady(dir) {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("ptyhold: holder %s did not become ready in %s%s",
		spec.ID, spawnReadyTimeout, holderLogSuffix(dir))
}

// holderLogSuffix quotes what the holder printed, so a readiness timeout says
// why instead of only that it happened.
func holderLogSuffix(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, HolderLogFile))
	if err != nil {
		return " (no holder output captured)"
	}
	out := strings.TrimSpace(string(raw))
	if out == "" {
		return " (holder produced no output)"
	}
	const max = 600
	if len(out) > max {
		out = out[len(out)-max:]
	}
	return ": holder output: " + out
}

func socketReady(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, MetaFile)); err != nil {
		return false
	}
	_, err := os.Stat(filepath.Join(dir, SocketFile))
	return err == nil
}

func readSmallFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

// ReadSpool returns up to maxBytes of recent output from the spool files.
// maxBytes <= 0 returns everything retained. Tolerates missing files.
func ReadSpool(root, id string, maxBytes int) []byte {
	dir := Dir(root, id)
	old := readIfExists(filepath.Join(dir, SpoolOld))
	cur := readIfExists(filepath.Join(dir, SpoolFile))
	all := make([]byte, 0, len(old)+len(cur))
	all = append(all, old...)
	all = append(all, cur...)
	if maxBytes > 0 && len(all) > maxBytes {
		all = all[len(all)-maxBytes:]
	}
	return all
}

func readIfExists(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return b
}

// ReadSpoolFrom returns the bytes of the spool stream from offset onwards, plus
// the stream's current total length. The stream is spool.old followed by spool,
// and it only ever grows by appending — so a caller that remembers the total it
// last saw can ask for just what arrived since.
//
// A total *smaller* than the offset means the stream was rotated (spool became
// spool.old, discarding the previous spool.old) and the offset no longer refers
// to the same bytes. Callers must treat that as "start again from zero": this
// returns the whole stream in that case, so a caller that resets its offset gets
// a correct answer either way.
//
// This exists because replaying the entire spool to render one screen costs
// hundreds of milliseconds once a session has been busy for an hour — measured
// at ~1.3s for a 6 MB spool on a real remote — and the dashboard asks twice a
// second.
func ReadSpoolFrom(root, id string, offset int64) ([]byte, int64) {
	dir := Dir(root, id)
	oldSize := fileSize(filepath.Join(dir, SpoolOld))
	curSize := fileSize(filepath.Join(dir, SpoolFile))
	total := oldSize + curSize

	if offset < 0 || offset > total {
		offset = 0 // rotated, truncated, or a bogus offset: hand back everything
	}
	if offset == total {
		return nil, total
	}

	out := make([]byte, 0, total-offset)
	if offset < oldSize {
		out = append(out, readRange(filepath.Join(dir, SpoolOld), offset, oldSize-offset)...)
		out = append(out, readRange(filepath.Join(dir, SpoolFile), 0, curSize)...)
		return out, total
	}
	return readRange(filepath.Join(dir, SpoolFile), offset-oldSize, total-offset), total
}

func fileSize(path string) int64 {
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fi.Size()
}

// readRange reads n bytes at off, tolerating a file that is shorter than the
// stat said (the holder is appending to it concurrently).
func readRange(path string, off, n int64) []byte {
	if n <= 0 {
		return nil
	}
	f, err := os.Open(path) //nolint:gosec // G304: path is built from the contract's own directory
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, n)
	read, err := f.ReadAt(buf, off)
	if read <= 0 && err != nil {
		return nil
	}
	return buf[:read]
}

// Dial connects to the live socket. Live output only — replay comes from
// ReadSpool.
func Dial(root, id string) (net.Conn, error) {
	conn, err := net.DialTimeout("unix", SocketPath(root, id), 2*time.Second)
	if err != nil {
		return nil, fmt.Errorf("ptyhold: dial: %w", err)
	}
	return conn, nil
}

// Status derives a session's lifecycle state from the contract files.
type Status string

const (
	StatusRunning Status = "running"
	StatusExited  Status = "exited"
	StatusGone    Status = "gone"
)

// Inspect reads one session's state without touching the holder process.
type Inspect struct {
	Status Status
	Meta   Meta
	Exit   string
}

func InspectSession(root, id string) Inspect {
	dir := Dir(root, id)
	out := Inspect{Status: StatusGone}
	if line, err := readSmallFile(filepath.Join(dir, ExitFile)); err == nil {
		out.Status = StatusExited
		out.Exit = strings.TrimSpace(line)
		return out
	}
	metaRaw, merr := readSmallFile(filepath.Join(dir, MetaFile))
	if merr != nil {
		return out // no meta, no exit: never started or fully cleaned
	}
	var meta Meta
	if json.Unmarshal([]byte(metaRaw), &meta) == nil {
		out.Meta = meta
	}
	if _, serr := os.Stat(filepath.Join(dir, SocketFile)); serr == nil && pidAlive(out.Meta.PID) {
		out.Status = StatusRunning
	} else if pidAlive(out.Meta.PID) {
		out.Status = StatusRunning
	} else {
		out.Status = StatusExited
	}
	return out
}

func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// ScanIDs lists every session id that has a directory under root/pty.
func ScanIDs(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "pty"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	return ids, nil
}

// RequestResize writes the resize marker file.
func RequestResize(root, id string, cols, rows int) error {
	if cols <= 0 || rows <= 0 || cols > 0xFFFF || rows > 0xFFFF {
		return fmt.Errorf("ptyhold: invalid window size %dx%d", cols, rows)
	}
	return writeFileAtomic(filepath.Join(Dir(root, id), ResizeFile), []byte(strconv.Itoa(cols)+"x"+strconv.Itoa(rows)+"\n"))
}

// RequestKill touches the kill marker file.
func RequestKill(root, id string) error {
	f, err := os.OpenFile(filepath.Join(Dir(root, id), KillFile), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// Cleanup removes a session directory entirely. Only safe when not running.
func Cleanup(root, id string) error {
	return os.RemoveAll(Dir(root, id))
}
