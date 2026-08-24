package ui

import (
	"io"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/hinshun/vt10x"
)

type TerminalModel struct {
	term vt10x.Terminal
	rw   io.ReadWriter
	w, h int
}

func NewTerminalModel(rw io.ReadWriter, w, h int) TerminalModel {
	term := vt10x.New()
	term.Resize(w, h)

	// Read from the reader into the terminal
	go func() {
		// Create a buffer to read into
		buf := make([]byte, 4096)
		for {
			n, err := rw.Read(buf)
			if n > 0 {
				term.Lock()
				_, _ = term.Write(buf[:n])
				term.Unlock()
			}
			if err != nil {
				break
			}
		}
	}()

	return TerminalModel{
		term: term,
		rw:   rw,
		w:    w,
		h:    h,
	}
}

func (m TerminalModel) Init() tea.Cmd {
	return nil
}

func (m TerminalModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if m.rw == nil {
			return m, nil
		}
		// Forward keys to the writer with correct control sequences
		if b := keypressBytes(msg); len(b) > 0 {
			_, _ = m.rw.Write(b)
		}
	case tea.MouseMsg:
		if m.rw != nil {
			var b []byte
			switch msg.Mouse().Button {
			case tea.MouseWheelUp:
				// Send 10 arrow up for wheel up
				b = []byte(strings.Repeat("\x1b[A", 10))
			case tea.MouseWheelDown:
				// Send 10 arrow down for wheel down
				b = []byte(strings.Repeat("\x1b[B", 10))
			}
			if len(b) > 0 {
				_, _ = m.rw.Write(b)
			}
		}
		return m, nil

	case tea.WindowSizeMsg:
		m.w = msg.Width
		m.h = msg.Height
		m.term.Lock()
		m.term.Resize(m.w, m.h)
		m.term.Unlock()
	}
	return m, nil
}

// keypressBytes encodes a key press as the byte sequence a remote PTY expects.
func keypressBytes(k tea.KeyPressMsg) []byte {
	switch k.String() {
	case "enter":
		return []byte("\r")
	case "backspace":
		return []byte("\x7f")
	case "tab":
		return []byte("\t")
	case "esc":
		return []byte("\x1b")
	case "up":
		return []byte("\x1b[A")
	case "down":
		return []byte("\x1b[B")
	case "right":
		return []byte("\x1b[C")
	case "left":
		return []byte("\x1b[D")
	case "pgup":
		return []byte("\x1b[5~")
	case "pgdown":
		return []byte("\x1b[6~")
	}
	// Ctrl+<letter> is the letter with the upper three bits cleared.
	if k.Mod&tea.ModCtrl != 0 && k.Code >= 'a' && k.Code <= 'z' {
		return []byte{byte(k.Code & 0x1f)}
	}
	if k.Text != "" {
		return []byte(k.Text)
	}
	return nil
}

func (m TerminalModel) viewString() string {
	var b strings.Builder
	m.term.Lock()
	defer m.term.Unlock()

	cols, rows := m.term.Size()
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			cell := m.term.Cell(x, y)
			ch := cell.Char
			if ch == 0 {
				ch = ' '
			}
			b.WriteRune(ch)
		}
		if y < rows-1 {
			b.WriteRune('\n')
		}
	}
	return b.String()
}

func (m TerminalModel) View() tea.View {
	return newView(m.viewString())
}
