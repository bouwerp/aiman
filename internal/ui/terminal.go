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
		// Forward keys to the writer
		if m.rw != nil {
			_, _ = m.rw.Write([]byte(msg.String()))
		}
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
