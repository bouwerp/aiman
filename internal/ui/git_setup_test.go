package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestGitSetupTogglesSelectedOrganization(t *testing.T) {
	m := NewGitSetupModel(&config.Config{})
	m.SetSize(120, 40)
	updated, _ := m.Update(orgsMsg{orgs: []string{"acme"}})
	m = updated.(GitSetupModel)
	m.focusIndex = 1

	updated, _ = m.Update(pressKey("space"))
	m = updated.(GitSetupModel)

	item, ok := m.orgsList.Items()[0].(orgItem)
	if !ok || !item.selected {
		t.Fatalf("organization was not selected: %#v", m.orgsList.Items()[0])
	}
	if !strings.Contains(m.viewString(), "Organizations (1 selected)") || !strings.Contains(m.viewString(), "[x] acme") {
		t.Fatalf("selected organization is not rendered as checked:\n%s", m.viewString())
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

func TestOpeningGitSetupKeepsOrganizationListSized(t *testing.T) {
	m := NewModel(&config.Config{}, nil, nil, &mockSessionRepo{}, nil, nil, nil)
	m.SetSize(120, 40)
	m.state = viewStateMenu
	for i, item := range m.menu.Items() {
		if menu, ok := item.(menuItem); ok && menu.action == viewStateGitSetup {
			m.menu.Select(i)
			break
		}
	}

	updated, _ := m.handleMenuUpdate(pressKey("enter"))
	m = updated.(*Model)
	updated, _ = m.handleGitSetupUpdate(orgsMsg{orgs: []string{"acme"}})
	m = updated.(*Model)

	if !strings.Contains(m.gitSetup.viewString(), "acme") {
		t.Fatalf("organization list is not visible after opening Git Configuration:\n%s", m.gitSetup.viewString())
	}
	if !strings.Contains(m.gitSetup.viewString(), "Organizations (0 selected)") || !strings.Contains(m.gitSetup.viewString(), "[ ] acme") {
		t.Fatalf("organization list must render checkbox selection state:\n%s", m.gitSetup.viewString())
	}
}
