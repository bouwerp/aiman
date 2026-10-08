package pane

import (
	"encoding/hex"
	"hash/fnv"
	"strings"
)

// RowPatch is one changed row of a rendered screen. The short JSON names keep
// a quiet poll small on a slow link.
type RowPatch struct {
	Index int    `json:"i"`
	Line  string `json:"t"`
}

// CaptureReply is what a pane capture owes a client that already has a screen.
// Text is set for a full replace. Rows is set for a patch. Neither is set when
// the screen is unchanged. Hash is the hash of the screen after this reply.
type CaptureReply struct {
	Unchanged bool
	Hash      string
	Text      string
	Rows      []RowPatch
}

// ScreenHash is the fingerprint a PTY capture stores and the client sends back.
// It is not a security hash. tmux probes use the remote's sha256 instead, and
// the client stores that string as-is rather than recomputing it.
func ScreenHash(text string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil))
}

// DiffScreen compares two rendered screens. A different line count, or a change
// to more than half the rows, is a full replace: a full-screen TUI repaint is
// not a list of patches.
func DiffScreen(prev, next string) (same bool, replace string, rows []RowPatch) {
	if prev == next {
		return true, "", nil
	}
	prevLines := strings.Split(prev, "\n")
	nextLines := strings.Split(next, "\n")
	if len(prevLines) != len(nextLines) {
		return false, next, nil
	}
	var patches []RowPatch
	for i := range nextLines {
		if prevLines[i] != nextLines[i] {
			patches = append(patches, RowPatch{Index: i, Line: nextLines[i]})
		}
	}
	if len(patches)*2 > len(nextLines) {
		return false, next, nil
	}
	return false, "", patches
}

// ApplyScreen applies a full replace or a row patch. ok is false when a patch
// names a row the previous screen does not have, so the caller can discard its
// cache and ask for a full screen.
func ApplyScreen(prev, replace string, rows []RowPatch) (string, bool) {
	if replace != "" {
		return replace, true
	}
	lines := strings.Split(prev, "\n")
	for _, row := range rows {
		if row.Index < 0 || row.Index >= len(lines) {
			return "", false
		}
		lines[row.Index] = row.Line
	}
	return strings.Join(lines, "\n"), true
}

// ReplyFor builds the capture a client should receive. haveHash is the hash of
// the screen that client last applied. A hash of the current screen skips the
// body. A hash of the remembered screen may be a row patch. Anything else is a
// full copy, because this process remembers one screen per session, not one
// cursor per client.
func ReplyFor(prevText, curText, haveHash string) CaptureReply {
	curHash := ScreenHash(curText)
	if haveHash == curHash {
		return CaptureReply{Unchanged: true, Hash: curHash}
	}
	if haveHash != ScreenHash(prevText) {
		return CaptureReply{Hash: curHash, Text: curText}
	}
	same, replace, rows := DiffScreen(prevText, curText)
	if same {
		return CaptureReply{Unchanged: true, Hash: curHash}
	}
	if replace != "" {
		return CaptureReply{Hash: curHash, Text: replace}
	}
	return CaptureReply{Hash: curHash, Rows: rows}
}
