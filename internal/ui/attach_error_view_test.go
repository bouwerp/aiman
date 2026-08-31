package ui

import (
	"strings"
	"testing"
)

// Attach revive failures used to set viewStateVSCodeError, which appends
// "Open VS Code / Install code in PATH" instructions under an unrelated error.
func TestShowSessionActionErrorUsesGenericDialog(t *testing.T) {
	m := &Model{width: 80, height: 24}
	m.showSessionActionError("Cannot attach: session abc has no agent session id; use restart to relaunch")

	if m.state != viewStateError {
		t.Fatalf("state = %v, want viewStateError", m.state)
	}
	out := m.renderErrorDialog()
	if strings.Contains(out, "Open VS Code") || strings.Contains(out, "Install \"code\" command") {
		t.Fatalf("attach error must not show VS Code install tips, got:\n%s", out)
	}
	if !strings.Contains(out, "no agent session id") {
		t.Fatalf("expected the attach error text, got:\n%s", out)
	}
}
