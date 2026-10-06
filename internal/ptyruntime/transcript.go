package ptyruntime

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/hinshun/vt10x"

	"github.com/bouwerp/aiman/internal/ptyhold"
)

// transcriptFeedStep is how much PTY output is applied between transcript
// snapshots. A Muse frame fits in a couple of kilobytes; a coarser step would
// hide a line that was painted and replaced inside it.
const transcriptFeedStep = 2048

// scrollbackName is the history that must outlive the spool. The spool keeps
// two 8 MiB segments of raw bytes, and Muse spends those on cursor addressing,
// so the bytes that painted a transcript line are gone while the line is
// still something the preview has to scroll to.
const scrollbackName = "scrollback"

func (s *screen) feed(data []byte, cols, rows int, record bool) {
	for off := 0; off < len(data); off += transcriptFeedStep {
		end := off + transcriptFeedStep
		if end > len(data) {
			end = len(data)
		}
		before := snapshotRows(s.term, cols, rows)
		from := len(s.back.lines)
		_, _ = s.term.Write(data[off:end])
		// The alternate screen is the agent's own buffer. Lines that leave it
		// are not the preview's history.
		if s.term.Mode()&vt10x.ModeAltScreen != 0 {
			continue
		}
		fresh := map[string]int{}
		for _, line := range s.back.lines[from:] {
			fresh[line]++
		}
		for _, line := range linesScrolledOff(before, snapshotRows(s.term, cols, rows)) {
			if fresh[line] > 0 {
				fresh[line]--
				continue
			}
			s.back.add(line)
			if record {
				s.noteRedraw(line)
			}
		}
	}
}

func (s *screen) noteRedraw(line string) {
	s.redrawn = append(s.redrawn, line)
	if extra := len(s.redrawn) - maxScrollbackLines; extra > 0 {
		s.redrawn = append([]string(nil), s.redrawn[extra:]...)
	}
}

// takeRedrawn returns transcript lines overwritten since the last call.
func (s *screen) takeRedrawn() []string {
	out := s.redrawn
	s.redrawn = nil
	return out
}

func snapshotRows(term vt10x.Terminal, cols, rows int) []string {
	out := make([]string, rows)
	for y := 0; y < rows; y++ {
		out[y] = renderRow(term, y, cols)
	}
	return out
}

// linesScrolledOff is the transcript prefix that a cursor-addressed redraw
// replaced with the lines below it. Muse does this instead of a full-screen
// linefeed, so the row never reaches OnScrollOff.
func linesScrolledOff(before, after []string) []string {
	oldBand := transcriptBand(before)
	newBand := transcriptBand(after)
	if len(oldBand) < 2 || len(newBand) < 2 {
		return nil
	}
	for k := 1; k < len(oldBand); k++ {
		head := oldBand[k:]
		if len(head) < 2 || len(head) > len(newBand) {
			continue
		}
		if sameLines(head, newBand[:len(head)]) {
			return oldBand[:k]
		}
	}
	return nil
}

func sameLines(a, b []string) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// transcriptBand is the screen above the composer. The rule under the
// transcript, and the status row beneath it, rewrite constantly; including
// them makes a real scroll look like an unrelated repaint.
func transcriptBand(rows []string) []string {
	limit := len(rows)
	for i := len(rows) - 1; i >= 0; i-- {
		text := strings.TrimSpace(stripANSI(rows[i]))
		if text == "" {
			continue
		}
		if isRule(text) || strings.Contains(strings.ToLower(text), "muse-spark") {
			limit = i
			break
		}
	}
	var out []string
	for i := 0; i < limit; i++ {
		if volatileRow(rows[i]) {
			continue
		}
		out = append(out, rows[i])
	}
	return out
}

func volatileRow(row string) bool {
	text := strings.TrimSpace(stripANSI(row))
	if text == "" || isRule(text) {
		return true
	}
	low := strings.ToLower(text)
	for _, skip := range []string{"running command", "esc to interrupt", "to send to background"} {
		if strings.Contains(low, skip) {
			return true
		}
	}
	return false
}

func isRule(text string) bool {
	for _, r := range text {
		switch r {
		case '─', '━', '═', '-', '_', ' ', '\t':
			continue
		default:
			return false
		}
	}
	return text != ""
}

func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b || i+1 >= len(s) || s[i+1] != '[' {
			b.WriteByte(s[i])
			continue
		}
		i += 2
		for i < len(s) && !isCSIFinal(s[i]) {
			i++
		}
	}
	return b.String()
}

func isCSIFinal(c byte) bool {
	return (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}

// mergeScroll keeps history the retained spool can no longer reproduce, and
// appends lines the replay just discovered that are not already the tail.
func mergeScroll(saved, detected []string) []string {
	if len(saved) == 0 {
		return detected
	}
	if len(detected) == 0 {
		return append([]string(nil), saved...)
	}
	window := map[string]int{}
	start := len(saved) - len(detected)
	if start < 0 {
		start = 0
	}
	for _, line := range saved[start:] {
		window[line]++
	}
	out := append([]string(nil), saved...)
	for _, line := range detected {
		if window[line] > 0 {
			window[line]--
			continue
		}
		out = append(out, line)
	}
	if extra := len(out) - maxScrollbackLines; extra > 0 {
		out = append([]string(nil), out[extra:]...)
	}
	return out
}

func (s *screen) persistScrollback(root, id string) {
	n := len(s.backLines())
	if n == s.stored {
		return
	}
	s.stored = n
	_ = writeScrollback(root, id, s.backLines())
}

func scrollbackPath(root, id string) string {
	return filepath.Join(ptyhold.Dir(root, id), scrollbackName)
}

func loadScrollback(root, id string) []string {
	b, err := os.ReadFile(scrollbackPath(root, id))
	if err != nil || len(b) == 0 {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(string(b), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func writeScrollback(root, id string, lines []string) error {
	path := scrollbackPath(root, id)
	if len(lines) == 0 {
		return os.Remove(path)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}
