package ptyhold

import "bytes"

// cursorQueryScanner notices a Device Status Report for the cursor position
// (CSI 6 n) in agent output. Muse Code exits at startup if nothing answers
// within a short timeout ("cursor position could not be read within a normal
// duration"). The holder is the terminal, so it has to write the reply itself.
type cursorQueryScanner struct {
	carry []byte
}

const maxCursorCarry = 16

// cursorPositionReply is a valid CPR. Row 1 column 1 is enough: Muse only
// needs the query to complete, not a real layout.
var cursorPositionReply = []byte("\x1b[1;1R")

// Replies returns the cursor-position replies owed for this chunk. A CSI
// split across reads is held until the final byte arrives.
func (s *cursorQueryScanner) Replies(data []byte) []byte {
	buf := data
	if len(s.carry) > 0 {
		buf = append(append([]byte(nil), s.carry...), data...)
		s.carry = nil
	}
	var replies []byte
	i := 0
	for i < len(buf) {
		start := bytes.IndexByte(buf[i:], 0x1b)
		if start < 0 {
			break
		}
		start += i
		if start+1 >= len(buf) {
			s.remember(buf[start:])
			break
		}
		if buf[start+1] != '[' {
			i = start + 1
			continue
		}
		final := csiFinal(buf, start+2)
		if final < 0 {
			s.remember(buf[start:])
			break
		}
		if buf[final] == 'n' && bytes.Equal(buf[start+2:final], []byte("6")) {
			replies = append(replies, cursorPositionReply...)
		}
		i = final + 1
	}
	return replies
}

func (s *cursorQueryScanner) remember(partial []byte) {
	if len(partial) > 0 && len(partial) <= maxCursorCarry {
		s.carry = append([]byte(nil), partial...)
	}
}

// csiFinal is the index of the CSI terminator, or -1 when the sequence is
// still incomplete. Parameters are digits and semicolons only; anything else
// is not a cursor query and is skipped by the caller on the next byte.
func csiFinal(buf []byte, from int) int {
	for j := from; j < len(buf); j++ {
		c := buf[j]
		if c >= 0x40 && c <= 0x7e {
			return j
		}
		if (c < '0' || c > '9') && c != ';' {
			return j
		}
	}
	return -1
}
