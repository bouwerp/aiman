package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestAgentDefaultsModelListsKnownAgents(t *testing.T) {
	m := NewAgentDefaultsModel(&config.Config{})
	if len(m.rows) == 0 {
		t.Fatal("expected known agents")
	}
	out := m.View()
	if !strings.Contains(out, "Claude") || !strings.Contains(out, "Grok") {
		t.Fatalf("%s", out)
	}
}
