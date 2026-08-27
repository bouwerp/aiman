package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bouwerp/aiman/internal/ptyhold"
	"github.com/bouwerp/aiman/internal/server"
	"golang.org/x/term"
)

// runPTY drives the built-in PTY runtime through the serve socket:
//
//	aiman pty list
//	aiman pty create --id <aiman-session-id> --name <name> --dir <dir> --command <cmd> [--env K=V]…
//	aiman pty get|kill|forget <id>
//	aiman pty capture <id> [--lines N] [--max-bytes N]
//	aiman pty input <id> --data <text>
//	aiman pty attach <id>   # raw interactive relay; terminal goes raw mode
func runPTY(args []string) error {
	if len(args) == 0 {
		printPTYUsage(os.Stderr)
		return errUsage
	}
	// The holder needs neither the socket nor a resolvable home: it is handed an
	// explicit --root and --id.
	if args[0] == "hold" {
		return runPTYHold(args[1:])
	}
	sock, err := socketPath()
	if err != nil {
		return err
	}

	switch args[0] {
	case "events":
		return runPTYEvents(sock)
	case "list":
		return callAndPrint(sock, "pty.list", map[string]any{})
	case "create":
		return runPTYCreate(sock, args[1:])
	case "attach":
		if len(args) < 2 {
			writeCLIError(server.CodeInvalidParams, "pty attach requires a session id")
			return errUsage
		}
		return runPTYAttach(sock, args[1])
	case "capture", "get", "kill", "forget", "input", "resize":
		if len(args) < 2 {
			writeCLIError(server.CodeInvalidParams, "pty "+args[0]+" requires a session id")
			return errUsage
		}
		id := args[1]
		flags, _ := takeFlags(args[2:])
		method := "pty." + args[0]
		params := map[string]any{"id": id}
		switch args[0] {
		case "resize":
			cols, rows := atoi(flags["cols"]), atoi(flags["rows"])
			if cols <= 0 || rows <= 0 {
				writeCLIError(server.CodeInvalidParams, "pty resize requires --cols and --rows")
				return errUsage
			}
			params["cols"], params["rows"] = cols, rows
		case "capture":
			if n := flags["lines"]; n != "" {
				params["lines"] = atoi(n)
			}
			if n := flags["max-bytes"]; n != "" {
				params["max_bytes"] = atoi(n)
			}
		case "input":
			data, ok := flags["data"]
			if fpath := flags["file"]; !ok && fpath != "" {
				b, rerr := os.ReadFile(fpath)
				if rerr != nil {
					writeCLIError(server.CodeInvalidParams, "file unreadable: "+rerr.Error())
					return errUsage
				}
				data = string(b)
				ok = true
			}
			// Control characters go through --key, never --data. A shell does not
			// interpret the escape in `--data "\r"` or `--data '\x03'` — those
			// are the literal characters backslash and r, or backslash x 0 3 —
			// and nothing here unescapes them, so the agent typed them into its
			// input box instead of receiving Return or an interrupt.
			var keySeq string
			keyName := flags["key"]
			if keyName != "" {
				seq, known := ptyKeySequence(keyName)
				if !known {
					writeCLIError(server.CodeInvalidParams, "unknown key "+keyName+"; known: "+ptyKeyNames())
					return errUsage
				}
				keySeq = seq
			}
			if !ok && keySeq == "" {
				b, rerr := io.ReadAll(os.Stdin)
				if rerr != nil || len(b) == 0 {
					writeCLIError(server.CodeInvalidParams, "pty input requires --data, --file, --key, or stdin")
					return errUsage
				}
				data = string(b)
			}
			params["data"] = data + keySeq
		}
		return callAndPrint(sock, method, params)
	default:
		fmt.Fprintf(os.Stderr, "aiman pty: unknown command %q\n\n", args[0])
		printPTYUsage(os.Stderr)
		return errUsage
	}
}

func runPTYCreate(sock string, args []string) error {
	flags, _ := takeFlags(args)
	if pf := flags["params-file"]; pf != "" {
		raw, rerr := os.ReadFile(pf)
		if rerr != nil {
			writeCLIError(server.CodeInvalidParams, "params-file unreadable: "+rerr.Error())
			return errUsage
		}
		return callAndPrintRaw(sock, "pty.create", raw)
	}
	params := map[string]any{}
	for _, key := range []string{"id", "name", "dir", "command"} {
		if v := flags[key]; v != "" {
			params[key] = v
		}
	}
	if params["command"] == "" && flags["exec"] != "" {
		params["command"] = flags["exec"]
	}
	if params["id"] == "" || params["command"] == "" {
		writeCLIError(server.CodeInvalidParams, "pty create requires --id and --command")
		return errUsage
	}
	if envs, ok := flags["env"]; ok && envs != "" {
		env := map[string]string{}
		for _, kv := range splitComma(envs) {
			parts := splitFirst(kv, '=')
			if parts[0] != "" {
				env[parts[0]] = parts[1]
			}
		}
		params["env"] = env
	}
	if c := flags["cols"]; c != "" {
		params["cols"] = atoi(c)
	}
	if r := flags["rows"]; r != "" {
		params["rows"] = atoi(r)
	}
	return callAndPrint(sock, "pty.create", params)
}

// runPTYAttach is the remote end of `ssh -t host aiman pty attach <id>`: it
// puts the local terminal into raw mode and shuttles bytes between the tty and
// the serve socket until the session ends or the user detaches with ctrl+q.
func runPTYAttach(sock, id string) error {
	if !stdinIsTTY() {
		return errors.New("pty attach needs a terminal (run over `ssh -t`)")
	}
	cols, rows := terminalSize()

	connResp, err := server.AttachDial(sock, id, cols, rows)
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	defer connResp.Close()

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("pty attach: raw mode: %w", err)
	}
	defer func() {
		fmt.Fprint(os.Stdout, attachClose())
		_ = term.Restore(int(os.Stdin.Fd()), oldState)
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, resizeSignals()...)
	defer signal.Stop(stop)
	go func() {
		for range stop {
			if c, r := terminalSize(); c > 0 && r > 0 {
				_ = connResp.Resize(c, r)
			}
		}
	}()

	// Hint lives on the primary screen. attachOpen then switches to the alt
	// screen so a full-screen agent cannot paint over leftover local output.
	fmt.Fprint(os.Stdout, notice("[aiman] attached to "+id+" — press ctrl+q to detach (the session keeps running)"))
	fmt.Fprint(os.Stdout, attachOpen())

	stdin := detachOnCtrlQ(os.Stdin, connResp)
	// Two-step resize after the alt screen is cleared. kickAttachRedraw waits
	// once so Relay is already copying; otherwise the redraw fills the
	// subscribe buffer and is dropped.
	go kickAttachRedraw(connResp.Resize, cols, rows, time.Sleep)
	if err := attachExitErr(connResp.Relay(stdin, os.Stdout), stdin.Detached()); err != nil {
		return err
	}
	fmt.Fprint(os.Stdout, exitNotice(attachExitNote(id, stdin.Detached())))
	return nil
}

// attachExitNote explains why the relay ended. Losing the stream without a
// keypress means the far end went away — most often aiman serve being restarted
// or updated — and the useful next step is to reattach, since the session itself
// is owned by a holder process that outlives serve.
func attachExitNote(id string, detached bool) string {
	if detached {
		return "[aiman] detached from " + id
	}
	return "[aiman] stream to " + id + " ended (session exited, or aiman serve restarted)" +
		" — reattach with: aiman pty attach " + id
}

// runPTYEvents streams session activity as newline-delimited JSON until the
// connection ends. The dashboard runs this over SSH so it hears about a session
// changing instead of asking every half second.
func runPTYEvents(sock string) error {
	conn, err := server.EventsDial(sock)
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	defer conn.Close()

	enc := json.NewEncoder(os.Stdout)
	for {
		ev, rerr := conn.Next()
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				return nil
			}
			return rerr
		}
		if err := enc.Encode(ev); err != nil {
			return err
		}
	}
}

// ptyKeys maps the key names `pty input --key` accepts to the bytes a terminal
// sends for them.
//
// These exist so callers never have to smuggle a control character through a
// shell. `--data "\r"` and `--data '\x03'` both send the literal characters of
// the escape rather than the byte, which typed "\r" into the agent's input box
// instead of submitting, and "\x03" instead of interrupting.
var ptyKeys = map[string]string{
	"enter":  "\r",
	"return": "\r",
	"ctrl-c": "\x03",
	"ctrl-d": "\x04",
	"esc":    "\x1b",
	"tab":    "\t",
}

func ptyKeySequence(name string) (string, bool) {
	seq, ok := ptyKeys[strings.ToLower(strings.TrimSpace(name))]
	return seq, ok
}

func ptyKeyNames() string {
	names := make([]string, 0, len(ptyKeys))
	for k := range ptyKeys {
		names = append(names, k)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// notice formats an aiman message for a screen a full-screen agent is drawing
// on.
//
// The message goes out wherever the cursor happens to be, in the middle of the
// agent's frame, so it has to clean up after itself:
//
//	\x1b[0m   drop any styling the agent left set, or the message inherits its
//	          colours — agents routinely leave a foreground and background open
//	\r\n      start on a fresh line rather than mid-sentence
//	\x1b[2K   erase that line before writing over it
//	\x1b[K    erase whatever followed the cursor afterwards. Without this the
//	          remainder of the agent's box border trails off the end of the
//	          message as a long run of "─".
func notice(text string) string {
	return "\x1b[0m\r\n\x1b[2K" + text + "\x1b[K\r\n"
}

// exitNotice is notice for the last thing printed before the process exits, and
// clears the screen as well.
//
// What follows is outside this program's control: ssh prints "Connection to
// <host> closed." over whatever is on screen, and the dashboard redraws on top
// of that. Leaving a half-painted agent frame behind means both land on residue
// and produce overlapping, unreadable text.
func exitNotice(text string) string {
	return "\x1b[0m\x1b[2J\x1b[H" + text + "\r\n"
}

// attachExitErr decides whether a finished relay is a failure worth reporting.
//
// Detaching is a normal way to leave, but it looks like a failure from inside
// the relay: ctrl+q closes the connection, so whichever of the relay's two
// copy directions notices first reports "use of closed network connection" —
// and which one wins is a race. A non-nil return here becomes a non-zero exit
// from `aiman pty attach`, which the caller over `ssh -t` sees as exit status
// 1, which the dashboard then reports as a failed attach. So a detach the user
// asked for must never produce an error, and a closed connection is by
// definition one somebody closed deliberately.
func attachExitErr(err error, detached bool) error {
	switch {
	case err == nil, detached:
		return nil
	case errors.Is(err, io.EOF), errors.Is(err, net.ErrClosed):
		return nil
	default:
		return err
	}
}

func terminalSize() (int, int) {
	w, h, err := term.GetSize(int(os.Stdin.Fd()))
	if err != nil {
		return 80, 24
	}
	return w, h
}

func printPTYUsage(w io.Writer) {
	fmt.Fprint(w, `aiman pty — built-in PTY sessions on this host (needs aiman serve)

  aiman pty list
  aiman pty create --id ID --command "claude" [--dir DIR] [--env K=V,K2=V2] [--cols N --rows M]
  aiman pty get|capture|kill|forget ID     (capture: --lines N or --max-bytes N)
  aiman pty resize ID --cols N --rows M
  aiman pty input ID --data TEXT | --file PATH | --key enter|ctrl-c|ctrl-d|esc|tab
  aiman pty attach ID                      (interactive; detach with ctrl+q)
  aiman pty events                         (streams activity as JSON lines)

Sessions are owned by detached holder processes, so they survive disconnects and
serve restarts or updates; reattach with: aiman pty attach ID
`)
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func splitFirst(s string, sep byte) [2]string {
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			return [2]string{s[:i], s[i+1:]}
		}
	}
	return [2]string{s, ""}
}

// mouseTrackingOn asks the attaching terminal to send SGR mouse events
// (including the wheel). Claude's TUI does this itself, so wheel-scroll works
// there; Grok/agy/Codex often never get their own DECSET to the client (or
// send it only once in a dropped first frame), so PTY attach has to.
func mouseTrackingOn() string {
	return "\x1b[?1000h\x1b[?1002h\x1b[?1006h"
}

func mouseTrackingOff() string {
	return "\x1b[?1006l\x1b[?1002l\x1b[?1000l"
}

// attachRedrawGap is longer than Ink-style SIGWINCH debounce (~300ms). A
// shorter pause restores the original size before the agent reads it, so it
// skips a full layout and the cleared alt screen stays empty.
const attachRedrawGap = 600 * time.Millisecond

func attachRedrawNudge(cols, rows int) (int, int, bool) {
	if cols <= 0 || rows <= 0 {
		return 0, 0, false
	}
	nc, nr := cols, rows
	if cols > 3 {
		nc = cols - 2
	}
	if rows > 2 {
		nr = rows - 1
	}
	if nc != cols || nr != rows {
		return nc, nr, true
	}
	if cols > 1 {
		return cols - 1, rows, true
	}
	if rows > 1 {
		return cols, rows - 1, true
	}
	return 0, 0, false
}

// kickAttachRedraw forces a full agent layout onto the cleared alt screen.
func kickAttachRedraw(resize func(int, int) error, cols, rows int, sleep func(time.Duration)) {
	nudgeCols, nudgeRows, ok := attachRedrawNudge(cols, rows)
	if !ok {
		return
	}
	sleep(attachRedrawGap)
	_ = resize(nudgeCols, nudgeRows)
	sleep(attachRedrawGap)
	_ = resize(cols, rows)
}

// attachOpen puts the attaching tty on a blank alt screen with mouse tracking
// so leftover primary-screen text cannot show through a CUP-painted TUI.
func attachOpen() string {
	return "\x1b[?1049h\x1b[2J\x1b[H" + mouseTrackingOn()
}

func attachClose() string {
	return mouseTrackingOff() + "\x1b[?1049l"
}

// detachKey is the classic ctrl+q byte. TUIs that enable the kitty keyboard
// protocol or xterm modifyOtherKeys send a CSI sequence instead; those are
// listed in ctrlQSequences.
const detachKey = 0x11

var ctrlQSequences = [][]byte{
	{detachKey},
	[]byte("\x1b[113;5u"),    // kitty: q=113, ctrl
	[]byte("\x1b[113;5;1u"),  // kitty with press event type
	[]byte("\x1b[27;5;113~"), // xterm modifyOtherKeys
}

// detachOnCtrlQ wraps stdin so ctrl+q ends the relay instead of reaching the
// remote terminal.
func detachOnCtrlQ(r io.Reader, closer io.Closer) *detachReader {
	return &detachReader{r: r, closer: closer}
}

type detachReader struct {
	r        io.Reader
	closer   io.Closer
	buf      []byte
	detached atomic.Bool
}

// Detached reports whether the user asked to leave by pressing ctrl+q. The
// relay's other copy direction observes the resulting close concurrently, so
// this is read from a different goroutine than the one that sets it.
func (d *detachReader) Detached() bool { return d.detached.Load() }

func (d *detachReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if start, ok := indexCtrlQ(d.buf); ok {
			out := d.buf[:start]
			d.buf = nil
			d.detached.Store(true)
			_ = d.closer.Close()
			return copy(p, out), io.EOF
		}
		hold := trailingCtrlQPrefix(d.buf)
		deliver := d.buf[:len(d.buf)-hold]
		if len(deliver) > 0 {
			n := copy(p, deliver)
			d.buf = append(append([]byte(nil), deliver[n:]...), d.buf[len(d.buf)-hold:]...)
			return n, nil
		}
		tmp := make([]byte, max(len(p), 64))
		n, err := d.r.Read(tmp)
		d.buf = append(d.buf, tmp[:n]...)
		if err != nil {
			n = copy(p, d.buf)
			d.buf = d.buf[n:]
			return n, err
		}
	}
}

func indexCtrlQ(b []byte) (int, bool) {
	best := -1
	for _, seq := range ctrlQSequences {
		if i := bytes.Index(b, seq); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	if best < 0 {
		return 0, false
	}
	return best, true
}

// trailingCtrlQPrefix is the length of a proper prefix of a CSI ctrl+q
// sequence at the end of b. Only prefixes that already contain "113" (the
// codepoint for q) are held: holding a bare CSI introducer blocked stdin
// until the next key, which made ctrl+q feel stuck after agy enabled kitty
// keyboard reporting.
func trailingCtrlQPrefix(b []byte) int {
	best := 0
	for _, seq := range ctrlQSequences {
		if len(seq) < 2 {
			continue
		}
		for n := 2; n < len(seq); n++ {
			prefix := seq[:n]
			if !bytes.Contains(prefix, []byte("113")) {
				continue
			}
			if bytes.HasSuffix(b, prefix) && n > best {
				best = n
			}
		}
	}
	return best
}

// callAndPrintRaw issues a request whose params are pre-encoded JSON.
func callAndPrintRaw(sock, method string, rawParams []byte) error {
	resp, err := server.CallRaw(sock, method, rawParams)
	if err != nil {
		writeCLIError(server.CodeServerNotRunning, err.Error())
		return err
	}
	if resp.Error != nil {
		writeCLIError(resp.Error.Code, resp.Error.Message)
		return errors.New(resp.Error.Message)
	}
	return writeJSON(resp.Result)
}

// runPTYHold is the internal holder entry point (`aiman pty hold --root R
// --id ID`); it blocks for the lifetime of the session and is not part of the
// public CLI surface.
func runPTYHold(args []string) error {
	flags, _ := takeFlags(args)
	root, id := flags["root"], flags["id"]
	if root == "" || id == "" {
		return errors.New("pty hold requires --root and --id")
	}
	if err := ptyhold.Run(root, id); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	return nil
}
