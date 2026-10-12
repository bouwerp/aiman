package ui

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

func previewLines(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("row-%02d", i)
	}
	return strings.Join(lines, "\n")
}

func lastPreviewLine(m *Model) string {
	lines := strings.Split(m.viewport.View(), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}

// Shrinking the window moves the offset off the exact bottom. The next capture
// has to keep following, or the preview stays on the rows it had before the
// resize and never shows the latest output.
func TestPreviewKeepsUpWithTheTailAfterAResize(t *testing.T) {
	m := previewModel(t, "grok", "")
	m.viewport.SetWidth(40)
	m.viewport.SetHeight(10)
	_, _ = m.applyTmuxOutput(tmuxOutputMsg{session: "grok", output: previewLines(40)}, nil)
	if !m.viewport.AtBottom() {
		t.Fatal("the first paint should sit on the latest row")
	}

	m.viewport.SetHeight(6)
	_, _ = m.applyTmuxOutput(tmuxOutputMsg{session: "grok", output: previewLines(41)}, nil)
	if !strings.Contains(lastPreviewLine(m), "row-40") {
		t.Fatalf("preview stuck above the latest row, last visible %q", lastPreviewLine(m))
	}
}

// Scrolling up is a request to stay put. New output must not yank the view
// back to the tail until the user returns there.
func TestPreviewStaysPutAfterScrollingUp(t *testing.T) {
	m := previewModel(t, "grok", "")
	m.viewport.SetWidth(40)
	m.viewport.SetHeight(10)
	_, _ = m.applyTmuxOutput(tmuxOutputMsg{session: "grok", output: previewLines(40)}, nil)
	if _, handled := m.handlePreviewKey(pressKey("[")); !handled {
		t.Fatal("scroll up should be handled")
	}
	parked := m.viewport.YOffset()

	_, _ = m.applyTmuxOutput(tmuxOutputMsg{session: "grok", output: previewLines(45)}, nil)
	if m.viewport.YOffset() != parked {
		t.Fatalf("scrolled preview jumped from %d to %d", parked, m.viewport.YOffset())
	}
	if strings.Contains(lastPreviewLine(m), "row-44") {
		t.Fatal("a scrolled preview was pulled to the new tail")
	}
}

// List navigation shares keys with the viewport's pager. Those keys move the
// session list; they must not leave the preview parked off the latest row.
func TestListNavigationDoesNotFreezeThePreview(t *testing.T) {
	m := previewModel(t, "grok", "")
	m.viewport.SetWidth(40)
	m.viewport.SetHeight(10)
	m.state = viewStateMain
	_, _ = m.applyTmuxOutput(tmuxOutputMsg{session: "grok", output: previewLines(40)}, nil)
	bottom := m.viewport.YOffset()

	updated, _ := m.Update(pressKey("k"))
	got := updated.(*Model)
	if got.viewport.YOffset() != bottom {
		t.Fatalf("k moved the preview from %d to %d", bottom, got.viewport.YOffset())
	}
}

// A capture shorter than the panel, or one whose tail is only blank rows,
// leaves a hole under the live UI. The last real row belongs at the bottom
// of the preview.
func TestPreviewSitsOnTheLastRealRow(t *testing.T) {
	m := previewModel(t, "grok", "")
	m.viewport.SetWidth(40)
	m.viewport.SetHeight(14)
	m.tmuxOutput = "LIVE\n   \n   \n\x1b[48;5;236m            \x1b[0m"
	m.setPreviewContent()

	lines := strings.Split(m.viewport.View(), "\n")
	idx := -1
	for i, line := range lines {
		if strings.Contains(line, "LIVE") {
			idx = i
		}
	}
	if idx < 0 || idx < len(lines)-2 {
		t.Fatalf("LIVE is row %d of %d, want it on the bottom of the preview", idx, len(lines))
	}
}

// Padding between a long transcript and Grok's short composer must not survive
// into the preview, or the panel ends on empty rows.
func TestPreviewDropsTheGapAboveTheComposer(t *testing.T) {
	transcript := strings.Repeat("a", 50)
	m := previewModel(t, "grok", "")
	m.viewport.SetWidth(40)
	m.viewport.SetHeight(14)
	m.tmuxOutput = strings.Join([]string{
		transcript,
		"",
		"",
		"\x1b[48;5;236m" + strings.Repeat(" ", 12) + "\x1b[0m",
		strings.Repeat("─", 12),
		"│ prompt",
		strings.Repeat("─", 12),
		"",
		"status",
	}, "\n")
	m.setPreviewContent()

	content := m.viewport.GetContent()
	idx := strings.Index(content, transcript)
	if idx < 0 {
		t.Fatalf("transcript missing from preview:\n%s", content)
	}
	body := content[idx:]
	if strings.Contains(body, "\n\n") {
		t.Fatalf("composer padding stayed in the preview:\n%s", content)
	}
	if !strings.Contains(content, transcript) || !strings.Contains(content, "status") || !strings.Contains(content, "prompt") {
		t.Fatalf("transcript and composer should stay, got %q", content)
	}
}

// The embedded terminal is the thing the user is typing into. Keeping preview
// keys off the viewport must not swallow those keystrokes.
func TestTerminalPanelStillReceivesKeys(t *testing.T) {
	rw := &captureKeyRW{}
	term := NewTerminalModel(rw, 40, 10)
	m := previewModel(t, "grok", "screen")
	m.panelMode = panelModeTerminal
	m.terminal = &term
	m.state = viewStateMain

	if _, _ = m.Update(pressKey("x")); rw.text() != "x" {
		t.Fatalf("terminal panel got %q, want the key", rw.text())
	}
}

type captureKeyRW struct {
	mu  sync.Mutex
	buf []byte
}

func (c *captureKeyRW) Read([]byte) (int, error) { return 0, io.EOF }

func (c *captureKeyRW) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.buf = append(c.buf, p...)
	return len(p), nil
}

func (c *captureKeyRW) text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.buf)
}
