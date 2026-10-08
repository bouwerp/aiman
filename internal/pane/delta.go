package pane

import (
	"encoding/hex"
	"hash/fnv"
	"slices"
	"strings"
)

// RowPatch is one changed row of a rendered screen. The short JSON names keep
// a quiet poll small on a slow link.
type RowPatch struct {
	Index int    `json:"i"`
	Line  string `json:"t"`
}

// CaptureReply is what a pane capture owes a client that already has a screen.
// Text is a full replace. Rows is an in-place patch. Drop and Append move a
// scrolling window: Drop lines leave the top and Append is joined onto the
// bottom. None of those are set when the screen is unchanged. Hash is the hash
// of the screen after this reply.
type CaptureReply struct {
	Unchanged bool
	Hash      string
	Text      string
	Rows      []RowPatch
	Drop      int
	Append    string
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

// ApplyScroll drops lines from the top of prev and appends appendText.
// ok is false when drop runs past the cached screen, so the caller can discard
// its cache and ask for a full screen.
func ApplyScroll(prev string, drop int, appendText string) (string, bool) {
	if drop < 0 {
		return "", false
	}
	lines := strings.Split(prev, "\n")
	if drop > len(lines) {
		return "", false
	}
	lines = lines[drop:]
	if appendText != "" {
		lines = append(lines, strings.Split(appendText, "\n")...)
	}
	return strings.Join(lines, "\n"), true
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
	if drop, appended, ok := scrollPatch(prevText, curText); ok {
		return CaptureReply{Hash: curHash, Drop: drop, Append: appended}
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

// scrollPatch reports a screen that is the previous one with lines removed
// from the top, lines added at the bottom, or both. The shared run has to be
// at least half of the new screen; a shorter overlap is about as large as
// sending the screen. A blank line on its own is not a scroll: the joined
// suffix would be empty and the client could not tell it from no new lines.
func scrollPatch(prev, next string) (int, string, bool) {
	prevLines := strings.Split(prev, "\n")
	nextLines := strings.Split(next, "\n")
	for k := 0; k < len(prevLines); k++ {
		overlap := len(prevLines) - k
		if overlap > len(nextLines) || !slices.Equal(prevLines[k:], nextLines[:overlap]) {
			continue
		}
		if overlap*2 < len(nextLines) {
			return 0, "", false
		}
		added := nextLines[overlap:]
		if len(added) == 1 && added[0] == "" {
			return 0, "", false
		}
		if k == 0 && len(added) == 0 {
			continue
		}
		return k, strings.Join(added, "\n"), true
	}
	return 0, "", false
}
