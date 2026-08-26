package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync/atomic"

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
	defer func() { _ = term.Restore(int(os.Stdin.Fd()), oldState) }()

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

	// Say how to get out. A PTY session has no tmux prefix, so the muscle
	// memory of ctrl+b d does nothing here and there is otherwise no hint on
	// screen that ctrl+q is the way back.
	fmt.Fprint(os.Stdout, notice("[aiman] attached to "+id+" — press ctrl+q to detach (the session keeps running)"))

	stdin := detachOnCtrlQ(os.Stdin, connResp)
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

// detachKey is ctrl+q; pressing it closes the attach stream without touching
// the session itself.
const detachKey = 0x11

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
	if len(d.buf) > 0 {
		n := copy(p, d.buf)
		d.buf = d.buf[n:]
		return n, nil
	}
	n, err := d.r.Read(p)
	for i := 0; i < n; i++ {
		if p[i] == detachKey {
			// Keep everything before ctrl+q, drop it and everything after.
			out := p[:i]
			d.detached.Store(true)
			_ = d.closer.Close()
			if copy(p, out) < len(out) {
				return 0, io.EOF
			}
			return len(out), io.EOF
		}
	}
	return n, err
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
