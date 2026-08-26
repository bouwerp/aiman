package ptyhold

import "bytes"

// titleScanMax caps how much of a partial escape sequence is buffered between
// reads. A real title is a few dozen bytes; anything longer is a sequence this
// scanner does not understand, and holding it would let a stream of unterminated
// escapes grow the buffer without limit.
const titleScanMax = 1024

// titleScanner extracts terminal titles (OSC 0 and OSC 2) from a byte stream.
//
// Agents advertise what they are doing here. Claude Code sets
// "<spinner glyph> <current task>" and changes it several times a second while
// it works — 5,532 times in one real six-megabyte session — which makes a title
// that is still moving the cheapest and most certain evidence that an agent is
// working. It needs no rendered screen and no pattern matching against prose.
//
// The scanner is fed whatever the PTY read returned, so a sequence can be split
// at any byte: the tail of a partial sequence is carried over to the next call.
type titleScanner struct {
	pending []byte
}

// Feed returns every complete title in the data, in order.
func (t *titleScanner) Feed(data []byte) []string {
	buf := data
	if len(t.pending) > 0 {
		// A fresh slice rather than appending onto pending: append may reuse
		// pending's array, which would alias the buffer being scanned.
		joined := make([]byte, 0, len(t.pending)+len(data))
		joined = append(joined, t.pending...)
		joined = append(joined, data...)
		buf = joined
		t.pending = nil
	}

	var titles []string
	for {
		start := bytes.Index(buf, []byte("\x1b]"))
		if start < 0 {
			// Keep only a possible split at the very end ("…\x1b").
			if n := len(buf); n > 0 && buf[n-1] == 0x1b {
				t.carry(buf[n-1:])
			}
			return titles
		}
		body := buf[start+2:]

		// OSC introducer: the number, then ';'. Titles are 0 (icon+title) and
		// 2 (title). Anything else is skipped.
		semi := bytes.IndexByte(body, ';')
		if semi < 0 {
			t.carry(buf[start:])
			return titles
		}
		kind := string(body[:semi])
		rest := body[semi+1:]

		end, term := oscTerminator(rest)
		if end < 0 {
			t.carry(buf[start:])
			return titles
		}
		if kind == "0" || kind == "2" {
			titles = append(titles, string(rest[:end]))
		}
		buf = rest[end+term:]
	}
}

// carry stores an incomplete sequence for the next Feed, discarding it if it has
// grown past anything that could be a title.
func (t *titleScanner) carry(partial []byte) {
	if len(partial) > titleScanMax {
		t.pending = nil
		return
	}
	t.pending = append([]byte(nil), partial...)
}

// oscTerminator finds the end of an OSC string: BEL, or ST (ESC \). It returns
// the offset of the terminator and its length, or -1 if the string is not
// terminated yet.
func oscTerminator(b []byte) (offset, length int) {
	for i := 0; i < len(b); i++ {
		switch b[i] {
		case 0x07: // BEL
			return i, 1
		case 0x1b:
			if i+1 < len(b) && b[i+1] == '\\' { // ST
				return i, 2
			}
			// A bare ESC ends the OSC string in practice: the sequence was cut
			// short by whatever follows.
			return i, 1
		}
	}
	return -1, 0
}
