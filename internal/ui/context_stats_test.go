package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
)

func TestContextStatsView(t *testing.T) {
	m := NewContextStatsModel(&config.Config{Remotes: []config.Remote{{Name: "dev", Host: "10.0.1.5"}}})
	out := m.View()
	if !strings.Contains(out, "Shared context") || !strings.Contains(out, "10.0.1.5") {
		t.Fatalf("%s", out)
	}
	m.rows[0].busy = false
	m.rows[0].stats = domain.ContextStats{
		Notes: 4,
		Bytes: 2048,
		Ops:   map[string]domain.ContextOpStat{"get": {Count: 9, P50Ms: 1.2}},
	}
	out = m.View()
	if !strings.Contains(out, "4") || !strings.Contains(out, "9") {
		t.Fatalf("%s", out)
	}
}
