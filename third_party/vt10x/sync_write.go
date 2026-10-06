package vt10x

import (
	"bytes"
	"io"
	"unicode"
)

const (
	syncOpen  = "\x1b[?2026h"
	syncClose = "\x1b[?2026l"
	// An unclosed synchronized update must not pin the screen. Session
	// history fits under this; a missing terminator is parsed as it stands.
	syncHoldMax = 4 << 20
)

func (t *terminal) writeLocked(p []byte) (int, error) {
	in := len(p)
	if len(t.pending) > 0 {
		p = append(append([]byte{}, t.pending...), p...)
		t.pending = nil
	}
	if t.syncing {
		t.syncBuf = append(t.syncBuf, p...)
		t.drainSyncLocked()
		return in, nil
	}
	apply, hold, opened := splitSyncOpen(p)
	if len(apply) > 0 {
		n, err := t.parseLocked(apply)
		if err != nil || n != len(apply) {
			// apply and hold share a backing array. Append onto a fresh
			// slice so the unparsed tail cannot overwrite hold.
			pending := make([]byte, 0, len(apply)-n+len(hold))
			pending = append(pending, apply[n:]...)
			pending = append(pending, hold...)
			t.pending = pending
			if err != nil {
				return in, err
			}
			return in, nil
		}
	}
	if opened {
		t.syncing = true
		t.syncBuf = append(t.syncBuf, hold...)
		t.drainSyncLocked()
		return in, nil
	}
	t.pending = hold
	return in, nil
}

func (t *terminal) drainSyncLocked() {
	for t.syncing {
		end := bytes.Index(t.syncBuf, []byte(syncClose))
		if end < 0 {
			if len(t.syncBuf) <= syncHoldMax {
				return
			}
			t.parseAndClearSync()
			return
		}
		cut := end + len(syncClose)
		blob := append([]byte(nil), t.syncBuf[:cut]...)
		rest := append([]byte(nil), t.syncBuf[cut:]...)
		t.syncBuf = nil
		t.syncing = false
		if _, err := t.parseLocked(blob); err != nil {
			t.logln(err.Error())
		}
		if len(rest) == 0 {
			return
		}
		apply, hold, opened := splitSyncOpen(rest)
		if len(apply) > 0 {
			if _, err := t.parseLocked(apply); err != nil {
				t.logln(err.Error())
			}
		}
		if !opened {
			t.pending = hold
			return
		}
		t.syncing = true
		t.syncBuf = append([]byte(nil), hold...)
	}
}

func (t *terminal) parseAndClearSync() {
	blob := t.syncBuf
	t.syncBuf = nil
	t.syncing = false
	if _, err := t.parseLocked(blob); err != nil {
		t.logln(err.Error())
	}
}

// splitSyncOpen reports the bytes before a synchronized-update open, and the
// open plus what follows it. A trailing prefix of the open is held so a
// sequence split across reads is not parsed as text.
func splitSyncOpen(p []byte) (apply, hold []byte, opened bool) {
	open := []byte(syncOpen)
	if i := bytes.Index(p, open); i >= 0 {
		return p[:i], p[i:], true
	}
	for n := len(open) - 1; n > 0; n-- {
		if len(p) >= n && bytes.Equal(p[len(p)-n:], open[:n]) {
			return p[:len(p)-n], p[len(p)-n:], false
		}
	}
	return p, nil, false
}

func (t *terminal) parseLocked(p []byte) (int, error) {
	var written int
	r := bytes.NewReader(p)
	for {
		c, sz, err := r.ReadRune()
		if err != nil {
			if err == io.EOF {
				break
			}
			return written, err
		}
		written += sz
		if c == unicode.ReplacementChar && sz == 1 {
			if r.Len() == 0 {
				// not enough bytes for a full rune
				return written - 1, nil
			}
			t.logln("invalid utf8 sequence")
			continue
		}
		t.put(c)
	}
	return written, nil
}
