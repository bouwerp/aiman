package ptyhold

import (
	"strings"
	"testing"
)

func scan(chunks ...string) (bool, bool) {
	var m modeScanner
	for _, c := range chunks {
		m.Feed([]byte(c))
	}
	return m.Modes()
}

// Codex draws inline and never asks for mouse reporting, so attach must not
// assert either on its behalf — doing so leaves the pane unscrollable.
func TestModeScannerCodexUsesNeither(t *testing.T) {
	alt, mouse := scan("\x1b[?2004h\x1b[>4;0m\x1b[>7u\x1b[?1004h\x1b[6n")
	if alt || mouse {
		t.Errorf("codex's own sequences imply alt=%v mouse=%v, want both false", alt, mouse)
	}
}

// Codex 0.162 asks for the alt screen and mouse reporting after a private
// query that is not a DECSET (`ESC[?u`). That query shares the DEC prefix.
// Dropping the rest of the chunk leaves attach on the primary screen, where
// the wheel and the agent's cursor-addressed redraws fight each other.
func TestModeScannerCodexAltScreenSurvivesTheQuery(t *testing.T) {
	const startup = "\x1b[?u\x1b[c\x1b[?2026h\x1b[?25l\x1b[?1049h\x1b[>4;0m\x1b[>7u\x1b[?1007l\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?1006h"
	alt, mouse := scan(startup)
	if !alt || !mouse {
		t.Errorf("one chunk: alt=%v mouse=%v, want both true", alt, mouse)
	}

	// The query can end a read. The modes arrive in the next one, and a
	// carried copy of the query must not swallow them.
	split := strings.Index(startup, "\x1b[?u")
	if split < 0 {
		t.Fatal("startup has no query")
	}
	head := startup[:split+len("\x1b[?u")]
	alt, mouse = scan(head, startup[len(head):])
	if !alt || !mouse {
		t.Errorf("split after query: alt=%v mouse=%v, want both true", alt, mouse)
	}
}

func TestModeScannerDetectsAltScreenAndMouse(t *testing.T) {
	alt, mouse := scan("\x1b[?1049h\x1b[?1000h\x1b[?1006h")
	if !alt || !mouse {
		t.Errorf("alt=%v mouse=%v, want both true", alt, mouse)
	}
}

func TestModeScannerHonoursReset(t *testing.T) {
	alt, mouse := scan("\x1b[?1049h\x1b[?1002h", "some output", "\x1b[?1002l\x1b[?1049l")
	if alt || mouse {
		t.Errorf("alt=%v mouse=%v, want both false after reset", alt, mouse)
	}
}

// A DECSET can straddle a read boundary; losing it would leave attach guessing.
func TestModeScannerSpansChunkBoundaries(t *testing.T) {
	alt, mouse := scan("\x1b[?10", "49h", "\x1b[?100", "6h")
	if !alt || !mouse {
		t.Errorf("alt=%v mouse=%v, want both true across a split", alt, mouse)
	}
}

func TestModeScannerHandlesCombinedParameters(t *testing.T) {
	alt, mouse := scan("\x1b[?1002;1006h")
	if alt {
		t.Error("no alt screen was requested")
	}
	if !mouse {
		t.Error("a combined parameter list should still register mouse")
	}
}

func TestModeScannerLegacyAltScreenModes(t *testing.T) {
	for _, seq := range []string{"\x1b[?47h", "\x1b[?1047h", "\x1b[?1049h"} {
		if alt, _ := scan(seq); !alt {
			t.Errorf("%q should register as alt screen", seq)
		}
	}
}

// Other CSI sequences share the prefix but are not mode changes.
func TestModeScannerIgnoresNonModeSequences(t *testing.T) {
	alt, mouse := scan("\x1b[?25l\x1b[?u\x1b[?1049;2$p")
	if alt || mouse {
		t.Errorf("alt=%v mouse=%v, want both false", alt, mouse)
	}
}

// An unterminated CSI in a long stream must not grow the carry buffer forever.
func TestModeScannerBoundsItsCarry(t *testing.T) {
	var m modeScanner
	long := "\x1b[?"
	for i := 0; i < 500; i++ {
		long += "1"
	}
	m.Feed([]byte(long))
	if len(m.carry) > maxModeCarry {
		t.Errorf("carry grew to %d bytes, cap is %d", len(m.carry), maxModeCarry)
	}
}
