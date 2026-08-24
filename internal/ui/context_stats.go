package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/contextstore"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type contextStatsRow struct {
	host  string
	stats domain.ContextStats
	err   string
	busy  bool
}

type ContextStatsModel struct {
	rows   []contextStatsRow
	cursor int
}

type contextStatsMsg struct {
	host  string
	stats domain.ContextStats
	err   error
}

func NewContextStatsModel(cfg *config.Config) ContextStatsModel {
	var rows []contextStatsRow
	if cfg != nil {
		for _, r := range cfg.Remotes {
			rows = append(rows, contextStatsRow{host: r.Host, busy: true})
		}
	}
	return ContextStatsModel{rows: rows}
}

func (m ContextStatsModel) Init() tea.Cmd { return nil }

func probeContextStatsCmd(cfg *config.Config, host string) tea.Cmd {
	return func() tea.Msg {
		st, err := fetchRemoteContextStats(cfg, host)
		return contextStatsMsg{host: host, stats: st, err: err}
	}
}

func fetchRemoteContextStats(cfg *config.Config, host string) (domain.ContextStats, error) {
	r, ok := remoteForHost(cfg, host)
	if !ok {
		return domain.ContextStats{}, fmt.Errorf("remote not found")
	}
	mgr := ssh.NewManager(ssh.Config{Host: r.Host, User: r.User, Root: r.Root})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := mgr.Execute(ctx, `PATH="$HOME/.local/bin:$PATH" aiman context stats`)
	if err != nil {
		return domain.ContextStats{}, err
	}
	raw := []byte(strings.TrimSpace(out))
	if i := strings.Index(out, "{"); i >= 0 {
		raw = []byte(out[i:])
	}
	st, perr := contextstore.ParseStatsJSON(raw)
	if perr != nil {
		var wrap struct {
			Result json.RawMessage `json:"result"`
			Stats  json.RawMessage `json:"stats"`
		}
		if json.Unmarshal(raw, &wrap) == nil && len(wrap.Stats) > 0 {
			return contextstore.ParseStatsJSON(wrap.Stats)
		}
		return domain.ContextStats{}, perr
	}
	return st, nil
}

func (m *Model) enterContextStats() tea.Cmd {
	m.contextStats = NewContextStatsModel(m.cfg)
	m.state = viewStateContextStats
	var cmds []tea.Cmd
	for _, r := range m.contextStats.rows {
		cmds = append(cmds, probeContextStatsCmd(m.cfg, r.host))
	}
	return tea.Batch(cmds...)
}

func (m *Model) handleContextStatsUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if got, ok := msg.(contextStatsMsg); ok {
		for i := range m.contextStats.rows {
			if m.contextStats.rows[i].host == got.host {
				m.contextStats.rows[i].busy = false
				m.contextStats.rows[i].stats = got.stats
				if got.err != nil {
					m.contextStats.rows[i].err = got.err.Error()
				} else {
					m.contextStats.rows[i].err = ""
				}
			}
		}
		return m, nil
	}
	km, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch km.String() {
	case "esc", "q":
		m.state = viewStateMenu
		return m, nil
	case "up", "k":
		if m.contextStats.cursor > 0 {
			m.contextStats.cursor--
		}
	case "down", "j":
		if m.contextStats.cursor < len(m.contextStats.rows)-1 {
			m.contextStats.cursor++
		}
	case "r":
		if len(m.contextStats.rows) == 0 {
			return m, nil
		}
		var cmds []tea.Cmd
		for i := range m.contextStats.rows {
			m.contextStats.rows[i].busy = true
			m.contextStats.rows[i].err = ""
			cmds = append(cmds, probeContextStatsCmd(m.cfg, m.contextStats.rows[i].host))
		}
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

func (m ContextStatsModel) View() string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	var b strings.Builder
	b.WriteString("Shared context\n")
	b.WriteString("Notes live on each remote in ~/.aiman/context/. Agents see a pack at session start and the aiman skill.\n\n")
	if len(m.rows) == 0 {
		b.WriteString(dim.Render("  No remotes configured.\n"))
		b.WriteString("\n  " + dim.Render("esc back") + "\n")
		return docStyle.Render(b.String())
	}
	fmt.Fprintf(&b, "  %-22s  %6s  %8s  %5s  %5s  %5s  %8s  %s\n",
		"Host", "Notes", "Size", "Get", "Find", "Pack", "Get p50", "Status")
	b.WriteString("  " + dim.Render(strings.Repeat("─", 86)) + "\n")
	for i, r := range m.rows {
		host := fmt.Sprintf("%-22s", r.host)
		if i == m.cursor {
			host = activeStyle.Render(host)
		}
		if r.busy {
			fmt.Fprintf(&b, "  %s  %s\n", host, dim.Render("probing…"))
			continue
		}
		if r.err != "" {
			fmt.Fprintf(&b, "  %s  %s\n", host, failStyle.Render(truncateRunes(r.err, 50)))
			continue
		}
		st := r.stats
		fmt.Fprintf(&b, "  %s  %6d  %8s  %5d  %5d  %5d  %7sms  ok\n",
			host, st.Notes, formatBytes(st.Bytes),
			st.Ops["get"].Count, st.Ops["find"].Count, st.Ops["pack"].Count,
			formatMs(st.Ops["get"].P50Ms),
		)
	}
	if i := m.cursor; i >= 0 && i < len(m.rows) && !m.rows[i].busy && m.rows[i].err == "" {
		b.WriteString("\n")
		b.WriteString(m.rows[i].detail())
	}
	b.WriteString("\n  " + dim.Render("r refresh  esc back") + "\n")
	return docStyle.Render(b.String())
}

func (r contextStatsRow) detail() string {
	st := r.stats
	var b strings.Builder
	b.WriteString(fmt.Sprintf("  Namespaces:"))
	if len(st.Namespaces) == 0 {
		b.WriteString(" none\n")
	} else {
		for ns, n := range st.Namespaces {
			b.WriteString(fmt.Sprintf(" %s=%d", ns, n))
		}
		b.WriteString("\n")
	}
	order := []string{"get", "find", "list", "pack", "put"}
	for _, name := range order {
		op, ok := st.Ops[name]
		if !ok || op.Count == 0 {
			continue
		}
		fmt.Fprintf(&b, "  %-4s  n=%d  err=%d  last=%.2fms  avg=%.2fms  p50=%.2fms  p95=%.2fms",
			name, op.Count, op.Errors, op.LastMs, op.AvgMs, op.P50Ms, op.P95Ms)
		if op.LastAt != "" {
			b.WriteString("  " + op.LastAt)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func formatBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKiB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMiB", float64(n)/(1024*1024))
	}
}

func formatMs(ms float64) string {
	return fmt.Sprintf("%.2f", ms)
}
