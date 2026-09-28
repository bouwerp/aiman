package vt10x

// SetScrollOff registers fn to observe rows that scroll off the top of the
// primary screen. fn runs on the terminal's own lock, so it must not call
// back into the terminal.
func SetScrollOff(t Terminal, fn func([]Glyph)) {
	term, ok := t.(*terminal)
	if !ok || term == nil {
		return
	}
	term.OnScrollOff = fn
}
