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
	//
	// cfg.Save() resolves ~/.aiman/config.yaml from the real HOME env var —
	// it has no test-injectable path. Without this sandbox, running this
	// test on a developer machine silently overwrites that developer's real
	// aiman config with this test's fixture data.
	t.Setenv("HOME", t.TempDir())
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
