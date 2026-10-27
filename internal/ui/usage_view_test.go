package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/usage"
	"github.com/charmbracelet/x/ansi"
)

func TestDollarOpensUsageView(t *testing.T) {
	cfg := twoRemoteCfg()
	m := NewModel(cfg, nil, nil, nil, nil, nil, nil)
	m.state = viewStateMain
	m.currentTab = tabSessions

	updated, cmd := m.Update(pressRune('$'))
	m = updated.(*Model)
	if m.state != viewStateUsage {
		t.Fatalf("state = %v, want viewStateUsage", m.state)
	}
	if cmd == nil {
		t.Fatal("expected one fetch per remote")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != len(cfg.Remotes) {
		t.Fatalf("fetch cmds = %T (%d remotes)", cmd(), len(cfg.Remotes))
	}
}

func TestDollarIgnoredOnDaemonsTab(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, nil, nil, nil, nil)
	m.state = viewStateMain
	m.currentTab = tabDaemons

	updated, _ := m.Update(pressRune('$'))
	m = updated.(*Model)
	if m.state != viewStateMain {
		t.Fatalf("state = %v, want viewStateMain", m.state)
	}
}

func TestMergeUsageRowsAcrossHosts(t *testing.T) {
	wrong := 0.1
	hosts := []usageHost{
		{
			host: "a",
			rows: map[usageViewMode][]usage.Row{
				usageByIssue: {{
					Key: "PROJ-1",
					Summary: usage.Summary{
						Tokens:        domain.TokenCounts{Input: 27, CacheRead: 73, Output: 10},
						CostUSD:       1.0,
						Priced:        true,
						CacheHitRatio: &wrong,
					},
				}},
				usageByDay: {{Key: "2026-10-01", Summary: usage.Summary{CostUSD: 9, Priced: true}}},
			},
		},
		{
			host: "b",
			rows: map[usageViewMode][]usage.Row{
				usageByIssue: {{
					Key: "PROJ-1",
					Summary: usage.Summary{
						Tokens:        domain.TokenCounts{Input: 10, CacheRead: 10, Output: 5},
						CostUSD:       0.5,
						Priced:        true,
						Partial:       true,
						CacheHitRatio: &wrong,
					},
				}},
			},
		},
	}

	rows := mergeUsageRows(hosts, usageByIssue)
	if len(rows) != 1 || rows[0].Key != "PROJ-1" {
		t.Fatalf("rows = %+v", rows)
	}
	got := rows[0].Summary
	if got.CostUSD != 1.5 {
		t.Fatalf("cost = %v, want 1.5", got.CostUSD)
	}
	if !got.Priced || !got.Partial {
		t.Fatalf("priced=%v partial=%v", got.Priced, got.Partial)
	}
	wantTokens := hosts[0].rows[usageByIssue][0].Summary.Tokens.Add(hosts[1].rows[usageByIssue][0].Summary.Tokens)
	if got.Tokens != wantTokens {
		t.Fatalf("tokens = %+v, want %+v", got.Tokens, wantTokens)
	}
	wantRatio, ok := wantTokens.CacheHitRatio()
	if !ok || got.CacheHitRatio == nil || *got.CacheHitRatio != wantRatio {
		t.Fatalf("ratio = %v, want %v", got.CacheHitRatio, wantRatio)
	}
}

func TestUsageViewRendersRows(t *testing.T) {
	m := UsageViewModel{
		mode: usageByIssue,
		hosts: []usageHost{
			{
				host:     "alpha",
				extra:    "rtk: 12k tokens saved",
				unpriced: []string{"gpt-5.6-terra", "codex-auto-review"},
				rows: map[usageViewMode][]usage.Row{
					usageByIssue: {
						{
							Key: "PROJ-1",
							Summary: usage.Summary{
								Tokens:  domain.TokenCounts{Input: 27, CacheRead: 73, Output: 100},
								CostUSD: 1.5,
								Priced:  true,
							},
						},
						{
							Key: usage.NoTicketKey,
							Summary: usage.Summary{
								Tokens:  domain.TokenCounts{Input: 4, Output: 1},
								CostUSD: 0.25,
								Priced:  true,
							},
						},
						{
							Key: "CODEX",
							Summary: usage.Summary{
								Tokens:  domain.TokenCounts{Input: 1500, Output: 42},
								CostUSD: 4.2,
								Priced:  false,
							},
						},
					},
				},
			},
			{
				host: "beta",
				rows: map[usageViewMode][]usage.Row{
					usageByIssue: {},
				},
			},
		},
	}

	plain := ansi.Strip(m.viewString())
	for _, want := range []string{
		"Usage · last 30 days by ticket",
		"PROJ-1",
		"~$1.50",
		"73%",
		"(no ticket)",
		"~ estimates · unpriced:",
		"gpt-5.6-terra",
		"codex-auto-review",
		"rtk: 12k tokens saved",
		"beta",
		"tab by ticket/day  r refresh  esc back",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in:\n%s", want, plain)
		}
	}
	if strings.Count(plain, "rtk: 12k tokens saved") != 1 {
		t.Fatalf("extra line count = %d\n%s", strings.Count(plain, "rtk: 12k tokens saved"), plain)
	}
	var codex string
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "CODEX") {
			codex = line
			break
		}
	}
	if codex == "" {
		t.Fatalf("CODEX row missing:\n%s", plain)
	}
	if strings.Contains(codex, "~$") || !strings.Contains(codex, "-") || !strings.Contains(codex, "1.5k") || !strings.Contains(codex, "42") {
		t.Fatalf("codex row = %q", codex)
	}
}

func TestUsageViewRendersHealthOnHostLine(t *testing.T) {
	m := UsageViewModel{
		mode: usageByIssue,
		hosts: []usageHost{
			{
				host:  "alpha",
				extra: healthExtra(usage.Health{
					Known:        true,
					ReadCalls:    5,
					EditCalls:    2,
					FirstTestAt:  time.Date(2026, time.October, 22, 14, 23, 0, 0, time.UTC),
				}),
				rows: map[usageViewMode][]usage.Row{
					usageByIssue: {},
				},
			},
		},
	}
	plain := ansi.Strip(m.viewString())
	// READ:EDIT must appear on the same line as the host name, not below it.
	if !strings.Contains(plain, "alpha") || !strings.Contains(plain, "READ:EDIT 5:2") {
		t.Fatalf("health not on host line:\n%s", plain)
	}
	// It must not appear as a separate indented line below the host.
	lines := strings.Split(plain, "\n")
	for _, line := range lines {
		if strings.Contains(line, "READ:EDIT 5:2") && strings.Contains(line, "alpha") {
			// Found on same line - correct
		}
		if strings.TrimSpace(line) == "READ:EDIT 5:2 · first test 14:23" {
			t.Fatalf("health on separate line below host:\n%s", plain)
		}
	}
}

func TestUsageViewTabSwitchesMode(t *testing.T) {
	cfg := twoRemoteCfg()
	m := NewModel(cfg, nil, nil, nil, nil, nil, nil)
	m.state = viewStateUsage
	m.usageView.mode = usageByIssue
	m.usageView.hosts = []usageHost{
		{host: cfg.Remotes[0].Host, rows: map[usageViewMode][]usage.Row{usageByIssue: {}}},
		{host: cfg.Remotes[1].Host, rows: map[usageViewMode][]usage.Row{usageByIssue: {}}},
	}

	updated, cmd := m.Update(pressKey("tab"))
	m = updated.(*Model)
	if m.usageView.mode != usageByDay {
		t.Fatalf("mode after first tab = %v", m.usageView.mode)
	}
	if cmd == nil {
		t.Fatal("first switch to day should fetch")
	}

	updated, cmd = m.Update(pressKey("tab"))
	m = updated.(*Model)
	if m.usageView.mode != usageByIssue {
		t.Fatalf("mode after second tab = %v", m.usageView.mode)
	}
	if cmd != nil {
		t.Fatal("cached ticket mode should not refetch")
	}

	updated, cmd = m.Update(pressKey("r"))
	m = updated.(*Model)
	if m.usageView.mode != usageByIssue {
		t.Fatalf("r changed mode to %v", m.usageView.mode)
	}
	if cmd == nil {
		t.Fatal("r should refetch")
	}
}

func TestUsageViewEscReturnsToMain(t *testing.T) {
	m := NewModel(twoRemoteCfg(), nil, nil, nil, nil, nil, nil)
	m.state = viewStateUsage

	updated, _ := m.Update(pressKey("esc"))
	m = updated.(*Model)
	if m.state != viewStateMain {
		t.Fatalf("esc state = %v, want main", m.state)
	}

	m.state = viewStateUsage
	updated, _ = m.Update(pressKey("q"))
	m = updated.(*Model)
	if m.state != viewStateMain {
		t.Fatalf("q state = %v, want main", m.state)
	}
}
