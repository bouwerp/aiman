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
		io.Copy(term, rw)
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
			m.rw.Write([]byte(msg.String()))
		}
	case tea.WindowSizeMsg:
		m.w = msg.Width
		m.h = msg.Height
		m.term.Resize(m.w, m.h)
	}
	return m, nil
}

func (m TerminalModel) View() string {
	var b strings.Builder
	rows, cols := m.term.Size()
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			cell := m.term.Cell(x, y)
			b.WriteRune(cell.Char)
		}
		if y < rows-1 {
			b.WriteRune('\n')
		}
	}
	return b.String()
}
