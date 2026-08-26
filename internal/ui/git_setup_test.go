package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestGitSetupTogglesSelectedOrganization(t *testing.T) {
	m := NewGitSetupModel(&config.Config{})
	updated, _ := m.Update(orgsMsg{orgs: []string{"acme"}})
	m = updated.(GitSetupModel)
	m.focusIndex = 1

	updated, _ = m.Update(pressKey("space"))
	m = updated.(GitSetupModel)

	item, ok := m.orgsList.Items()[0].(orgItem)
	if !ok || !item.selected {
		t.Fatalf("organization was not selected: %#v", m.orgsList.Items()[0])
	}
}

func TestGitSetupNavigatesAndTogglesOrganizationsWithEnter(t *testing.T) {
	m := NewGitSetupModel(&config.Config{})
	updated, _ := m.Update(orgsMsg{orgs: []string{"acme", "example"}})
	m = updated.(GitSetupModel)
	m.focusIndex = 1

	updated, _ = m.Update(pressKey("down"))
	m = updated.(GitSetupModel)
	if got := m.orgsList.Index(); got != 1 {
		t.Fatalf("organization cursor = %d, want 1", got)
	}

	updated, _ = m.Update(pressKey("enter"))
	m = updated.(GitSetupModel)
	item, ok := m.orgsList.Items()[1].(orgItem)
	if !ok || !item.selected {
		t.Fatalf("organization was not selected: %#v", m.orgsList.Items()[1])
	}
}
