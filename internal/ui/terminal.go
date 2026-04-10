package ui

import (
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
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
	case tea.KeyMsg:
		if m.rw == nil {
			return m, nil
		}
		// Forward keys to the writer with correct control sequences
		var b []byte
		if msg.Type == tea.KeyRunes {
			b = []byte(string(msg.Runes))
		} else {
			switch msg.Type {
			case tea.KeyEnter:
				b = []byte("\r")
			case tea.KeyBackspace:
				b = []byte("\x7f")
			case tea.KeyTab:
				b = []byte("\t")
			case tea.KeyEsc:
				b = []byte("\x1b")
			case tea.KeyUp:
				b = []byte("\x1b[A")
			case tea.KeyDown:
				b = []byte("\x1b[B")
			case tea.KeyRight:
				b = []byte("\x1b[C")
			case tea.KeyLeft:
				b = []byte("\x1b[D")
			case tea.KeyPgUp:
				b = []byte("\x1b[5~")
			case tea.KeyPgDown:
				b = []byte("\x1b[6~")
			case tea.KeyCtrlA:
				b = []byte("\x01")
			case tea.KeyCtrlB:
				b = []byte("\x02")
			case tea.KeyCtrlC:
				b = []byte("\x03")
			case tea.KeyCtrlD:
				b = []byte("\x04")
			case tea.KeyCtrlE:
				b = []byte("\x05")
			case tea.KeyCtrlF:
				b = []byte("\x06")
			case tea.KeyCtrlG:
				b = []byte("\x07")
			case tea.KeyCtrlH:
				b = []byte("\x08")
			case tea.KeyCtrlJ:
				b = []byte("\x0a")
			case tea.KeyCtrlK:
				b = []byte("\x0b")
			case tea.KeyCtrlL:
				b = []byte("\x0c")
			case tea.KeyCtrlN:
				b = []byte("\x0e")
			case tea.KeyCtrlO:
				b = []byte("\x0f")
			case tea.KeyCtrlP:
				b = []byte("\x10")
			case tea.KeyCtrlQ:
				b = []byte("\x11")
			case tea.KeyCtrlR:
				b = []byte("\x12")
			case tea.KeyCtrlS:
				b = []byte("\x13")
			case tea.KeyCtrlT:
				b = []byte("\x14")
			case tea.KeyCtrlU:
				b = []byte("\x15")
			case tea.KeyCtrlV:
				b = []byte("\x16")
			case tea.KeyCtrlW:
				b = []byte("\x17")
			case tea.KeyCtrlX:
				b = []byte("\x18")
			case tea.KeyCtrlY:
				b = []byte("\x19")
			case tea.KeyCtrlZ:
				b = []byte("\x1a")
			case tea.KeySpace:
				b = []byte(" ")
			}
		}
		if len(b) > 0 {
			_, _ = m.rw.Write(b)
		}
	case tea.MouseMsg:
		if m.rw != nil {
			var b []byte
			switch msg.Type {
			case tea.MouseWheelUp:
				// Send 10 arrow up for wheel up
				b = []byte("\x1b[A\x1b[A\x1b[A\x1b[A\x1b[A\x1b[A\x1b[A\x1b[A\x1b[A\x1b[A")
			case tea.MouseWheelDown:
				// Send 10 arrow down for wheel down
				b = []byte("\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B\x1b[B")
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

func (m TerminalModel) View() string {
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
