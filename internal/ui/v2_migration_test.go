package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestNewViewSetsFullscreenModes(t *testing.T) {
	v := newView("hello")
	if !v.AltScreen {
		t.Fatal("dashboard views must run on the alt screen")
	}
	if v.MouseMode != tea.MouseModeAllMotion {
		t.Fatalf("expected all-motion mouse tracking, got %v", v.MouseMode)
	}
	if got := v.Content; got != "hello" {
		t.Fatalf("content lost: %q", got)
	}
}

func TestDashboardViewHonoursMouseToggle(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.mouseEnabled = true
	if got := m.View().MouseMode; got != tea.MouseModeAllMotion {
		t.Fatalf("mouse enabled: expected all-motion, got %v", got)
	}
	m.mouseEnabled = false
	if got := m.View().MouseMode; got != tea.MouseModeNone {
		t.Fatalf("mouse disabled: expected none, got %v", got)
	}
}

func TestKeypressBytesEncodesTerminalSequences(t *testing.T) {
	cases := []struct {
		key  tea.KeyPressMsg
		want string
	}{
		{pressKey("enter"), "\r"},
		{pressKey("backspace"), "\x7f"},
		{pressKey("tab"), "\t"},
		{pressKey("esc"), "\x1b"},
		{pressKey("up"), "\x1b[A"},
		{pressKey("down"), "\x1b[B"},
		{pressKey("right"), "\x1b[C"},
		{pressKey("left"), "\x1b[D"},
		{pressKey("pgup"), "\x1b[5~"},
		{pressKey("pgdown"), "\x1b[6~"},
		// Ctrl+<letter> clears the upper three bits.
		{pressKey("ctrl+a"), "\x01"},
		{pressKey("ctrl+c"), "\x03"},
		{pressKey("ctrl+z"), "\x1a"},
		{pressKey("?"), "?"},
		{pressRune('x'), "x"},
	}
	for _, tc := range cases {
		if got := string(keypressBytes(tc.key)); got != tc.want {
			t.Errorf("keypressBytes(%q) = %q, want %q", tc.key.String(), got, tc.want)
		}
	}
	if b := keypressBytes(tea.KeyPressMsg{}); b != nil {
		t.Errorf("empty key must encode to nil, got %q", string(b))
	}
}

// TestPressKeyMatchesProductionBindings guards the harness itself: every
// keystroke name used in tests must produce a KeyPressMsg whose String()
// matches what bubbletea v2 delivers at runtime.
func TestPressKeyMatchesProductionBindings(t *testing.T) {
	for _, name := range []string{
		"enter", "esc", "tab", "up", "down", "left", "right", "space",
		"ctrl+a", "ctrl+k", "ctrl+r", "ctrl+s", "ctrl+y", "ctrl+m",
		"?", "/", "a", "N", "G",
	} {
		if got := pressKey(name).String(); got != name {
			t.Errorf("pressKey(%q).String() = %q", name, got)
		}
	}
}

func TestSetupModelSavesOnEnterViaHarness(t *testing.T) {
	// Smoke-test the v2 wiring end to end at the model level: type into the
	// focused first field, then confirm through the save button.
	cfg := &config.Config{}
	m := NewSetupModel(cfg)
	m.inputs[0].SetValue("https://example.atlassian.net")
	m.focusIndex = len(m.inputs) // focus the save button
	updated, _ := m.Update(pressKey("enter"))
	saved := updated.(SetupModel)
	if !saved.saved {
		t.Fatal("enter on save button must persist the config")
	}
	view := saved.viewString()
	if !strings.Contains(view, "Configuration saved") {
		t.Fatalf("unexpected view: %q", view)
	}
}
