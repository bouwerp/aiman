package ptyhold

import "bytes"

// modeScanner tracks the terminal modes an agent turns on for itself, by
// watching the DECSET/DECRST sequences in its output.
//
// Attach needs this because it cannot ask the agent. A client that reattaches
// mid-session sees none of the setup the agent emitted on its first frame — the
// agent believes those modes are already on and will not repeat them — so attach
// used to assert a fixed set on the attaching terminal instead. That guess is
// wrong for any agent that deliberately does without: forcing mouse reporting on
// one that ignores mouse events converts the wheel into escape sequences it
// drops, and forcing the alternate screen takes away the scrollback such an
// agent relies on. Between them the pane cannot be scrolled at all.
//
// Recording what the agent actually asked for lets attach mirror it.
type modeScanner struct {
	// carry holds a trailing partial escape sequence, so a DECSET split across
	// two reads is still recognised.
	carry []byte

	altScreen bool
	mouse     bool
}

// maxModeCarry bounds the partial-sequence buffer. A DECSET is a dozen bytes at
// most; anything longer is not one, and keeping it would grow without limit on
// output that happens to contain an unterminated CSI.
const maxModeCarry = 32

// altScreenModes are the private modes that swap to the alternate screen.
var altScreenModes = [][]byte{
	[]byte("1049"), // xterm alt screen with cursor save/restore
	[]byte("1047"), // alt screen
	[]byte("47"),   // legacy alt screen
}

// mouseModes are the private modes that make a terminal report mouse events.
// Any of them means the wheel belongs to the agent rather than the terminal.
var mouseModes = [][]byte{
	[]byte("1000"), // click tracking
	[]byte("1002"), // button-event tracking (drag)
	[]byte("1003"), // any-event tracking
	[]byte("1006"), // SGR extended coordinates
	[]byte("1015"), // urxvt extended coordinates
}

// Feed folds one chunk of agent output in, updating the tracked modes.
func (m *modeScanner) Feed(data []byte) {
	buf := data
	if len(m.carry) > 0 {
		buf = append(append([]byte(nil), m.carry...), data...)
		m.carry = nil
	}
	i := 0
	for {
		idx := bytes.Index(buf[i:], []byte("\x1b[?"))
		if idx < 0 {
			break
		}
		start := i + idx
		end := -1
		for j := start + 3; j < len(buf); j++ {
			c := buf[j]
			if c == 'h' || c == 'l' {
				end = j
				break
			}
			// A private-mode sequence is digits and semicolons only. Anything
			// else means this CSI is something other than DECSET/DECRST.
			if (c < '0' || c > '9') && c != ';' {
				break
			}
		}
		if end < 0 {
			// Either a partial sequence at the end of the chunk, or not a DECSET.
			if len(buf)-start <= maxModeCarry {
				m.carry = append([]byte(nil), buf[start:]...)
			}
			return
		}
		m.apply(buf[start+3:end], buf[end] == 'h')
		i = end + 1
	}
	if tail := len(buf) - i; tail > 0 && tail <= maxModeCarry {
		// Keep a possible sequence prefix that has not arrived in full yet.
		if k := bytes.LastIndex(buf[i:], []byte("\x1b")); k >= 0 {
			m.carry = append([]byte(nil), buf[i+k:]...)
		}
	}
}

// apply sets or clears the tracked modes named by one DECSET/DECRST parameter
// list, which may carry several modes separated by semicolons.
func (m *modeScanner) apply(params []byte, set bool) {
	for _, p := range bytes.Split(params, []byte(";")) {
		if len(p) == 0 {
			continue
		}
		for _, mode := range altScreenModes {
			if bytes.Equal(p, mode) {
				m.altScreen = set
			}
		}
		for _, mode := range mouseModes {
			if bytes.Equal(p, mode) {
				m.mouse = set
			}
		}
	}
}

// Modes reports the agent's current terminal modes.
func (m *modeScanner) Modes() (altScreen, mouse bool) { return m.altScreen, m.mouse }
