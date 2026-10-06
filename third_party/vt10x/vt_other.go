//go:build plan9 || nacl || windows
// +build plan9 nacl windows

package vt10x

import (
	"bufio"
	"unicode"
	"unicode/utf8"
)

type terminal struct {
	*State

	// pending is a trailing prefix of a synchronized-update open that arrived
	// split from the rest of the sequence. It stays unparsed until the next
	// write shows whether it really is that open.
	pending []byte
	// syncing holds output until the update closes. A screen sampled between
	// reads would otherwise show the update one line at a time.
	syncing bool
	syncBuf []byte
}

func newTerminal(info TerminalInfo) *terminal {
	t := &terminal{State: newState(info.w)}
	t.init(info.cols, info.rows)
	return t
}

func (t *terminal) init(cols, rows int) {
	t.numlock = true
	t.state = t.parse
	t.cur.Attr.FG = DefaultFG
	t.cur.Attr.BG = DefaultBG
	t.Resize(cols, rows)
	t.reset()
}

func (t *terminal) Write(p []byte) (int, error) {
	t.lock()
	defer t.unlock()
	return t.writeLocked(p)
}

// TODO: add tests for expected blocking behavior
func (t *terminal) Parse(br *bufio.Reader) error {
	var locked bool
	defer func() {
		if locked {
			t.unlock()
		}
	}()
	for {
		c, sz, err := br.ReadRune()
		if err != nil {
			return err
		}
		if c == unicode.ReplacementChar && sz == 1 {
			t.logln("invalid utf8 sequence")
			break
		}
		if !locked {
			t.lock()
			locked = true
		}

		// put rune for parsing and update state
		t.put(c)

		// break if our buffer is empty, or if buffer contains an
		// incomplete rune.
		n := br.Buffered()
		if n == 0 || (n < 4 && !fullRuneBuffered(br)) {
			break
		}
	}
	return nil
}

func fullRuneBuffered(br *bufio.Reader) bool {
	n := br.Buffered()
	buf, err := br.Peek(n)
	if err != nil {
		return false
	}
	return utf8.FullRune(buf)
}

func (t *terminal) Resize(cols, rows int) {
	t.lock()
	defer t.unlock()
	_ = t.resize(cols, rows)
}
