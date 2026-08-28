package main

import (
	"strings"
	"testing"
)

// Codex draws inline and never enables mouse reporting. Forcing the alternate
// screen on takes away the scrollback it relies on, and forcing mouse tracking
// on turns the wheel into escape sequences it discards — between them the pane
// cannot be scrolled at all.
func TestAttachOpenLeavesAnInlineAgentAlone(t *testing.T) {
	got := attachOpenFor(attachModes{})
	if got != "" {
		t.Errorf("an inline agent needs no terminal setup, got %q", got)
	}
	if got := attachCloseFor(attachModes{}); got != "" {
		t.Errorf("nothing was set, so nothing should be unset, got %q", got)
	}
}

func TestAttachOpenMirrorsAFullScreenMouseAgent(t *testing.T) {
	got := attachOpenFor(attachModes{altScreen: true, mouse: true})
	for _, want := range []string{"\x1b[?1049h", "\x1b[?1000h", "\x1b[?1006h"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

// Each mode is mirrored on its own: an agent that paints a full screen without
// reading mouse events must keep the wheel with the terminal.
func TestAttachOpenMirrorsEachModeIndependently(t *testing.T) {
	altOnly := attachOpenFor(attachModes{altScreen: true})
	if !strings.Contains(altOnly, "\x1b[?1049h") {
		t.Error("alt screen not applied")
	}
	if strings.Contains(altOnly, "\x1b[?1000h") {
		t.Errorf("mouse must not be forced on: %q", altOnly)
	}

	mouseOnly := attachOpenFor(attachModes{mouse: true})
	if strings.Contains(mouseOnly, "\x1b[?1049h") {
		t.Errorf("alt screen must not be forced on: %q", mouseOnly)
	}
	if !strings.Contains(mouseOnly, "\x1b[?1006h") {
		t.Error("mouse not applied")
	}
}

// Detach has to undo exactly what attach set, in reverse.
func TestAttachCloseUndoesWhatAttachSet(t *testing.T) {
	m := attachModes{altScreen: true, mouse: true}
	got := attachCloseFor(m)
	mouseAt := strings.Index(got, "\x1b[?1006l")
	altAt := strings.Index(got, "\x1b[?1049l")
	if mouseAt < 0 || altAt < 0 {
		t.Fatalf("close did not undo both modes: %q", got)
	}
	if mouseAt > altAt {
		t.Errorf("mouse should be released before leaving the alt screen: %q", got)
	}
}

// An agent can enter the alternate screen after attach began — the relay passes
// it through and the terminal obeys — so detach must reset it even though attach
// never set it, or the user is handed back a terminal stuck on the alt screen.
func TestAttachModesUnionCoversModesTurnedOnMidSession(t *testing.T) {
	atAttach := attachModes{}
	atDetach := attachModes{altScreen: true}

	got := attachCloseFor(atAttach.union(atDetach))
	if !strings.Contains(got, "\x1b[?1049l") {
		t.Errorf("detach must leave the alt screen, got %q", got)
	}
}

func TestAttachModesUnionKeepsWhatAttachSet(t *testing.T) {
	// The agent has since dropped mouse reporting, but attach turned it on and
	// still has to turn it back off.
	got := attachModes{mouse: true}.union(attachModes{})
	if !got.mouse {
		t.Error("union must keep a mode attach set")
	}
}
