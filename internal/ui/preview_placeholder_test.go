package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
)

func previewModel(t *testing.T, active, output string) *Model {
	t.Helper()
	m := NewModel(&config.Config{}, nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.activeSession = active
	m.tmuxOutput = output
	return m
}

// Swallowing a transient failure only makes sense when there is a previous pane
// worth holding on screen. With nothing loaded, the placeholder is all the user
// ever sees — and a session pointed at the wrong backend fails this way on
// every tick, so the preview sat at "Loading…" forever with no hint of trouble.
func TestTransientPaneErrorSurfacesWhenNothingHasLoaded(t *testing.T) {
	m := previewModel(t, "fix-yield", "Loading...")
	_, _ = m.applyTmuxOutput(tmuxOutputMsg{
		session: "fix-yield",
		err:     errors.New("can't find pane: fix-yield"),
	}, nil)

	if strings.TrimSpace(m.tmuxOutput) == "Loading..." {
		t.Fatal("the preview is still showing the placeholder")
	}
	if !strings.Contains(m.tmuxOutput, "can't find pane") {
		t.Errorf("the failure should be visible, got %q", m.tmuxOutput)
	}
}

// The original intent stands: a session restarting must not blank the pane the
// user is reading.
func TestTransientPaneErrorKeepsAnAlreadyLoadedPane(t *testing.T) {
	m := previewModel(t, "fix-yield", "agent output worth keeping")
	_, _ = m.applyTmuxOutput(tmuxOutputMsg{
		session: "fix-yield",
		err:     errors.New("no server running on /tmp/tmux-1004/default"),
	}, nil)

	if m.tmuxOutput != "agent output worth keeping" {
		t.Errorf("a loaded pane must survive a transient error, got %q", m.tmuxOutput)
	}
}

func TestTransientPaneErrorSurfacesOnAnEmptyPreview(t *testing.T) {
	m := previewModel(t, "fix-yield", "")
	_, _ = m.applyTmuxOutput(tmuxOutputMsg{
		session: "fix-yield",
		err:     errors.New("can't find pane: fix-yield"),
	}, nil)

	if !strings.Contains(m.tmuxOutput, "can't find pane") {
		t.Errorf("the failure should be visible, got %q", m.tmuxOutput)
	}
}
