package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/agent"
)

// TestAgentPickerHostListingWithoutProviders shows a flat host listing (Codex)
// as one model list, with Default and Other, and no provider step.
func TestAgentPickerHostListingWithoutProviders(t *testing.T) {
	m := NewAgentPickerModel([]domain.Agent{{Name: "Codex", Command: "codex"}})
	m.SetModelSource("dev-box", fakeLister([]agent.ModelOption{{ID: "gpt-6.2-sol", Label: "gpt-6.2-sol  GPT-6.2-Sol"}}, nil), nil)
	m = sendPicker(t, m, pressKey("enter"))
	if m.step != agentPickModel {
		t.Fatalf("step = %v, want the model step", m.step)
	}
	rows := m.modelRows()
	if len(rows) != 3 || rows[0].kind != rowDefault || rows[2].kind != rowOther {
		t.Fatalf("rows = %+v", rows)
	}
	m = pickPickerRow(t, m, "gpt-6.2-sol")
	if m.selected == nil || m.selected.LaunchModel != "gpt-6.2-sol" {
		t.Fatalf("selected = %+v", m.selected)
	}
}

// TestAgentPickerFailedListingShowsCatalog falls back to aiman's list when the
// host cannot list, and says why.
func TestAgentPickerFailedListingShowsCatalog(t *testing.T) {
	m := NewAgentPickerModel([]domain.Agent{{Name: "Grok", Command: "grok"}})
	m.SetModelSource("dev-box", fakeLister(nil, errors.New("grok: not logged in")), nil)
	m = sendPicker(t, m, pressKey("enter"))
	if m.step != agentPickModel || !strings.Contains(m.viewModelPick(), "grok: not logged in") {
		t.Fatalf("step = %v view:\n%s", m.step, m.viewModelPick())
	}
	m = pickPickerRow(t, m, "grok-4.5")
	if m.selected == nil || m.selected.LaunchModel != "grok-4.5" {
		t.Fatalf("selected = %+v", m.selected)
	}
}
