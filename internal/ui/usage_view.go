package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/infra/ssh"
	"github.com/bouwerp/aiman/internal/usage"
	"github.com/bouwerp/aiman/internal/usecase"
)

// usageViewMode selects which rollup the $ view is showing.
type usageViewMode int

const (
	usageByIssue usageViewMode = iota
	usageByDay
)

const usageViewFooter = "tab by ticket/day  r refresh  esc back"

// usageHost is one remote's rollup. extra is one dim line under the host
// name; this view leaves it empty. unpriced is the visible mode's model ids.
// unpricedBy keeps the other mode so a late fetch does not overwrite the
// list on screen.
type usageHost struct {
	host       string
	rows       map[usageViewMode][]usage.Row
	unpriced   []string
	unpricedBy map[usageViewMode][]string
	err        string
	busy       bool
	extra      string
}

// UsageViewModel is the $ screen: per-ticket or per-day cost across remotes.
type UsageViewModel struct {
	mode   usageViewMode
	hosts  []usageHost
	cursor int
}

// usageRollupMsg is one host's usage.get rollup for the $ view.
type usageRollupMsg struct {
	host string
	mode usageViewMode
	rep  usage.Report
	err  error
}

// usageRollupParams is the usage.get body for a host-wide rollup.
// Session is omitted so serve includes every session on that host.
type usageRollupParams struct {
	By              string `json:"by"`
	Days            int    `json:"days"`
	TZOffsetMinutes int    `json:"tz_offset_minutes"`
}

func usageRollupQuery(mode usageViewMode) usageRollupParams {
	_, offsetSec := time.Now().Zone()
	params := usageRollupParams{
		By:              "issue",
		Days:            30,
		TZOffsetMinutes: offsetSec / 60,
	}
	if mode == usageByDay {
		params.By = "day"
		params.Days = 14
	}
	return params
}

func newUsageView(cfg *config.Config) UsageViewModel {
	var hosts []usageHost
	if cfg != nil {
		for _, remote := range cfg.Remotes {
			hosts = append(hosts, usageHost{
				host: remote.Host,
				rows: map[usageViewMode][]usage.Row{},
				busy: true,
			})
		}
	}
	return UsageViewModel{hosts: hosts}
}

// fetchUsageRollupCmd loads one host's ticket (30 day) or day (14 day) rollup.
// The laptop's current offset is the zone the day window is reported in.
func fetchUsageRollupCmd(cfg *config.Config, host string, mode usageViewMode) tea.Cmd {
	return func() tea.Msg {
		rep, err := fetchUsageRollup(cfg, host, mode)
		return usageRollupMsg{host: host, mode: mode, rep: rep, err: err}
	}
}

func fetchUsageRollup(cfg *config.Config, host string, mode usageViewMode) (usage.Report, error) {
	remote, ok := remoteForHost(cfg, host)
	if !ok {
		return usage.Report{}, fmt.Errorf("remote not found")
	}
	mgr := ssh.NewManager(ssh.Config{Host: remote.Host, User: remote.User, Root: remote.Root})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := usecase.ServeRPC(ctx, mgr, "usage.get", usageRollupQuery(mode))
	if err != nil {
		return usage.Report{}, err
	}
	var body struct {
		Usage usage.Report `json:"usage"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return usage.Report{}, fmt.Errorf("decode usage: %w", err)
	}
	return body.Usage, nil
}

func fetchUsageRollups(cfg *config.Config, hosts []usageHost, mode usageViewMode) tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(hosts))
	for _, host := range hosts {
		if host.rows[mode] != nil {
			continue
		}
		cmds = append(cmds, fetchUsageRollupCmd(cfg, host.host, mode))
	}
	return tea.Batch(cmds...)
}

func (m *Model) enterUsageView() tea.Cmd {
	m.usageView = newUsageView(m.cfg)
	m.state = viewStateUsage
	return fetchUsageRollups(m.cfg, m.usageView.hosts, usageByIssue)
}

func (m *Model) applyUsageRollup(msg usageRollupMsg) {
	for i := range m.usageView.hosts {
		if m.usageView.hosts[i].host != msg.host {
			continue
		}
		m.usageView.hosts[i].applyRollup(msg, m.usageView.mode)
		m.usageView.clampCursor()
		return
	}
}

func (h *usageHost) applyRollup(msg usageRollupMsg, current usageViewMode) {
	if h.rows == nil {
		h.rows = map[usageViewMode][]usage.Row{}
	}
	if msg.err != nil {
		if msg.mode == current {
			h.busy = false
			h.err = msg.err.Error()
		}
		return
	}
	rows := msg.rep.Rows
	if rows == nil {
		rows = []usage.Row{}
	}
	h.rows[msg.mode] = rows
	h.rememberUnpriced(msg.mode, msg.rep.Unpriced)
	if msg.mode != current {
		return
	}
	h.busy = false
	h.err = ""
	h.unpriced = h.unpricedBy[msg.mode]
}

func (h *usageHost) rememberUnpriced(mode usageViewMode, ids []string) {
	if h.unpricedBy == nil {
		h.unpricedBy = map[usageViewMode][]string{}
	}
	if ids == nil {
		ids = []string{}
	}
	h.unpricedBy[mode] = ids
}

func (h *usageHost) showUnpriced(mode usageViewMode) {
	if ids, ok := h.unpricedBy[mode]; ok {
		h.unpriced = ids
	}
}

func (m *Model) handleUsageViewUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch km.String() {
	case "esc", "q":
		m.state = viewStateMain
		return m, nil
	case "tab":
		return m, m.toggleUsageMode()
	case "r":
		return m, m.refreshUsageView()
	case "up", "k", "left":
		m.moveUsageCursor(-1)
	case "down", "j", "right":
		m.moveUsageCursor(1)
	}
	return m, nil
}

func (m *Model) toggleUsageMode() tea.Cmd {
	if m.usageView.mode == usageByDay {
		m.usageView.mode = usageByIssue
	} else {
		m.usageView.mode = usageByDay
	}
	return m.showUsageMode()
}

// showUsageMode fetches the current mode when any host has not stored it.
// A stored slice, including an empty one, is a finished fetch, so tabbing
// back does not refetch.
func (m *Model) showUsageMode() tea.Cmd {
	mode := m.usageView.mode
	if usageModeCached(m.usageView.hosts, mode) {
		for i := range m.usageView.hosts {
			m.usageView.hosts[i].busy = false
			m.usageView.hosts[i].err = ""
			m.usageView.hosts[i].showUnpriced(mode)
		}
		m.usageView.clampCursor()
		return nil
	}
	for i := range m.usageView.hosts {
		m.usageView.hosts[i].showUnpriced(mode)
		if m.usageView.hosts[i].rows[mode] != nil {
			continue
		}
		m.usageView.hosts[i].busy = true
		m.usageView.hosts[i].err = ""
		m.usageView.hosts[i].unpriced = nil
	}
	m.usageView.clampCursor()
	return fetchUsageRollups(m.cfg, m.usageView.hosts, mode)
}

func (m *Model) refreshUsageView() tea.Cmd {
	mode := m.usageView.mode
	for i := range m.usageView.hosts {
		delete(m.usageView.hosts[i].rows, mode)
		delete(m.usageView.hosts[i].unpricedBy, mode)
		m.usageView.hosts[i].busy = true
		m.usageView.hosts[i].err = ""
		m.usageView.hosts[i].unpriced = nil
	}
	m.usageView.clampCursor()
	return fetchUsageRollups(m.cfg, m.usageView.hosts, mode)
}

func usageModeCached(hosts []usageHost, mode usageViewMode) bool {
	if len(hosts) == 0 {
		return true
	}
	for _, host := range hosts {
		if host.rows[mode] == nil {
			return false
		}
	}
	return true
}

func (m *Model) moveUsageCursor(delta int) {
	m.usageView.cursor += delta
	m.usageView.clampCursor()
}

func (m *UsageViewModel) clampCursor() {
	n := len(mergeUsageRows(m.hosts, m.mode))
	if n == 0 || m.cursor < 0 {
		m.cursor = 0
		return
	}
	if m.cursor > n-1 {
		m.cursor = n - 1
	}
}

// mergeUsageRows sums the same key across hosts. The cache ratio is the
// ratio of the summed tokens, not an average of the inputs. Priced and
// Partial are true when any host's row is.
func mergeUsageRows(hosts []usageHost, mode usageViewMode) []usage.Row {
	merged := map[string]usage.Summary{}
	var keys []string
	for _, host := range hosts {
		for _, row := range host.rows[mode] {
			sum, ok := merged[row.Key]
			if !ok {
				keys = append(keys, row.Key)
			}
			sum.Tokens = sum.Tokens.Add(row.Summary.Tokens)
			sum.CostUSD += row.Summary.CostUSD
			sum.Priced = sum.Priced || row.Summary.Priced
			sum.Partial = sum.Partial || row.Summary.Partial
			merged[row.Key] = rolledSummary(sum)
		}
	}
	rows := make([]usage.Row, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, usage.Row{Key: key, Summary: merged[key]})
	}
	sortUsageRows(rows, mode)
	return rows
}

func rolledSummary(sum usage.Summary) usage.Summary {
	ratio, ok := sum.Tokens.CacheHitRatio()
	if !ok {
		sum.CacheHitRatio = nil
		return sum
	}
	sum.CacheHitRatio = &ratio
	return sum
}

func sortUsageRows(rows []usage.Row, mode usageViewMode) {
	slices.SortFunc(rows, func(a, b usage.Row) int {
		if mode == usageByDay {
			return strings.Compare(b.Key, a.Key)
		}
		if a.Summary.CostUSD != b.Summary.CostUSD {
			if a.Summary.CostUSD > b.Summary.CostUSD {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Key, b.Key)
	})
}

func (m UsageViewModel) viewString() string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	var b strings.Builder
	b.WriteString(usageViewTitle(m.mode) + "\n\n")
	if len(m.hosts) == 0 {
		b.WriteString(dim.Render("  No remotes configured.") + "\n")
		b.WriteString("\n  " + dim.Render(usageViewFooter) + "\n")
		return docStyle.Render(b.String())
	}
	b.WriteString(usageColumnHeader(m.mode))
	for i, row := range mergeUsageRows(m.hosts, m.mode) {
		b.WriteString(usageRowLine(row, i == m.cursor))
	}
	b.WriteString("\n")
	for _, host := range m.hosts {
		b.WriteString(host.statusLine(dim, m.mode))
	}
	if ids := usageUnpricedIDs(m.hosts); len(ids) > 0 {
		b.WriteString(dim.Render("  ~ estimates · unpriced: "+strings.Join(ids, ", ")) + "\n")
	}
	b.WriteString("  " + dim.Render(usageViewFooter) + "\n")
	return docStyle.Render(b.String())
}

func usageViewTitle(mode usageViewMode) string {
	if mode == usageByDay {
		return "Usage · last 14 days by day"
	}
	return "Usage · last 30 days by ticket"
}

func usageColumnHeader(mode usageViewMode) string {
	label := "TICKET"
	if mode == usageByDay {
		label = "DAY"
	}
	return fmt.Sprintf("  %-16s  %8s  %6s  %8s  %8s\n", label, "~COST", "CACHE", "INPUT", "OUTPUT")
}

func usageRowLine(row usage.Row, selected bool) string {
	key := fmt.Sprintf("%-16s", row.Key)
	if selected {
		key = activeStyle.Render(key)
	}
	return fmt.Sprintf("  %s  %8s  %6s  %8s  %8s\n",
		key,
		usageCostCell(row.Summary),
		usageCacheCell(row.Summary),
		usage.FormatTokens(row.Summary.Tokens.Input),
		usage.FormatTokens(row.Summary.Tokens.Output),
	)
}

func usageCostCell(s usage.Summary) string {
	if text := usage.FormatUsageCost(s); text != "" {
		return text
	}
	return "-"
}

func usageCacheCell(s usage.Summary) string {
	if s.CacheHitRatio == nil {
		return "-"
	}
	pct := math.Round(*s.CacheHitRatio * 100)
	return strconv.FormatFloat(pct, 'f', 0, 64) + "%"
}

func (h usageHost) statusLine(dim lipgloss.Style, mode usageViewMode) string {
	if h.rows[mode] == nil && h.busy {
		return dim.Render("  "+h.host+" loading…") + "\n"
	}
	if h.rows[mode] == nil && h.err != "" {
		return "  " + h.host + "  " + failStyle.Render(truncateRunes(h.err, 80)) + "\n"
	}
	line := "  " + h.host + "\n"
	if h.extra != "" {
		line += dim.Render("  "+h.extra) + "\n"
	}
	return line
}

func usageUnpricedIDs(hosts []usageHost) []string {
	seen := map[string]struct{}{}
	var ids []string
	for _, host := range hosts {
		for _, id := range host.unpriced {
			if _, ok := seen[id]; ok || id == "" {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}
