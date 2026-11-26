package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/bouwerp/aiman/internal/domain"
)

// Feed page modes: tab cycles the first three, m opens memory and mutes.
const (
	feedModeActions = iota
	feedModeDigest
	feedModeBatches
	feedModeMemory
)

var feedModeNames = []string{"actions", "digest", "batches", "memory"}

// feedPageView is the Feed page opened with F: one host's actions in every
// state, its digest, the batches they came from, and its memory and mutes.
type feedPageView struct {
	host    string
	mode    int
	actions []domain.FeedAction
	digest  []domain.FeedEvent
	bullets []feedMemoryBullet
	mutes   []domain.FeedMute
	cursor  int
	loading bool
	err     string
}

// feedPageMsg answers one page read.
type feedPageMsg struct {
	host    string
	mode    int
	actions []domain.FeedAction
	digest  []domain.FeedEvent
	err     error
}

// feedMemoryMsg is one feed.memory answer (also forget_memory and unmute).
type feedMemoryMsg struct {
	host    string
	bullets []feedMemoryBullet
	mutes   []domain.FeedMute
	err     error
}

func feedMemoryResult(host string, raw json.RawMessage, err error) feedMemoryMsg {
	if err != nil {
		return feedMemoryMsg{host: host, err: err}
	}
	var res struct {
		Bullets []feedMemoryBullet `json:"bullets"`
		Mutes   []domain.FeedMute  `json:"mutes"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return feedMemoryMsg{host: host, err: fmt.Errorf("feed.memory: %w", err)}
	}
	return feedMemoryMsg{host: host, bullets: res.Bullets, mutes: res.Mutes}
}

// feedPageHost is the selected session's host when its feed is on, else the
// first host with a feed.
func (m *Model) feedPageHost() (string, bool) {
	hosts := m.feedHosts()
	if it, ok := m.selectedSessionItem(); ok {
		if r, ok := resolveRemote(m.cfg, it.session); ok {
			for _, h := range hosts {
				if h == r.Host {
					return h, true
				}
			}
		}
	}
	if len(hosts) == 0 {
		return "", false
	}
	return hosts[0], true
}

// enterFeedPage opens the Feed page.
func (m *Model) enterFeedPage() tea.Cmd {
	host, ok := m.feedPageHost()
	if !ok {
		return m.showToast("feed: no host has the feed on (Admin Menu → Feed)", false, 4*time.Second)
	}
	m.feedPage = &feedPageView{host: host, loading: true}
	m.state = viewStateFeed
	return m.feedPageReadCmd(m.feedPage)
}

// feedPageReadCmd reads what the page's mode shows, waiting for a
// foreground slot.
func (m *Model) feedPageReadCmd(p *feedPageView) tea.Cmd {
	r, ok := m.feedRemote(p.host)
	if !ok {
		return nil
	}
	budget := m.hostBudget
	mode := p.mode
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), feedRPCTimeout)
		defer cancel()
		release, err := budgetAcquire(ctx, budget, r.Host)
		if err != nil {
			return feedPageMsg{host: r.Host, mode: mode, err: err}
		}
		defer func() { release(callTimedOut(err)) }()
		switch mode {
		case feedModeMemory:
			var raw json.RawMessage
			raw, err = feedRPC(ctx, r, "feed.memory", map[string]any{})
			return feedMemoryResult(r.Host, raw, err)
		case feedModeDigest:
			var raw json.RawMessage
			raw, err = feedRPC(ctx, r, "feed.events", map[string]any{"digest": true, "limit": 200})
			if err != nil {
				return feedPageMsg{host: r.Host, mode: mode, err: err}
			}
			var res struct {
				Events []domain.FeedEvent `json:"events"`
			}
			if uerr := json.Unmarshal(raw, &res); uerr != nil {
				return feedPageMsg{host: r.Host, mode: mode, err: fmt.Errorf("feed.events: %w", uerr)}
			}
			return feedPageMsg{host: r.Host, mode: mode, digest: res.Events}
		default:
			var raw json.RawMessage
			raw, err = feedRPC(ctx, r, "feed.actions", map[string]any{"limit": 200})
			if err != nil {
				return feedPageMsg{host: r.Host, mode: mode, err: err}
			}
			var res struct {
				Actions []domain.FeedAction `json:"actions"`
			}
			if uerr := json.Unmarshal(raw, &res); uerr != nil {
				return feedPageMsg{host: r.Host, mode: mode, err: fmt.Errorf("feed.actions: %w", uerr)}
			}
			return feedPageMsg{host: r.Host, mode: mode, actions: res.Actions}
		}
	}
}

func (m *Model) applyFeedPage(msg feedPageMsg) {
	p := m.feedPage
	if p == nil || p.host != msg.host {
		return
	}
	p.loading = false
	if msg.err != nil {
		p.err = msg.err.Error()
		return
	}
	p.err = ""
	switch msg.mode {
	case feedModeDigest:
		p.digest = msg.digest
	default:
		p.actions = msg.actions
		sort.SliceStable(p.actions, func(i, j int) bool { return p.actions[i].ID > p.actions[j].ID })
	}
	p.cursor = min(p.cursor, max(0, m.feedPageLen()-1))
}

func (m *Model) applyFeedMemory(msg feedMemoryMsg) {
	p := m.feedPage
	if p == nil || p.host != msg.host {
		return
	}
	p.loading = false
	if msg.err != nil {
		p.err = msg.err.Error()
		return
	}
	p.err = ""
	p.bullets, p.mutes = msg.bullets, msg.mutes
	p.cursor = min(p.cursor, max(0, m.feedPageLen()-1))
}

// feedPageLen is the number of selectable rows in the current mode.
func (m *Model) feedPageLen() int {
	p := m.feedPage
	switch p.mode {
	case feedModeDigest:
		return len(p.digest)
	case feedModeBatches:
		return len(feedBatchRows(p.actions))
	case feedModeMemory:
		return len(p.bullets) + len(p.mutes)
	}
	return len(p.actions)
}

func (m *Model) handleFeedPageUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	km, ok := msg.(tea.KeyPressMsg)
	p := m.feedPage
	if !ok || p == nil {
		return m, nil
	}
	switch km.String() {
	case "esc", "q":
		if p.mode == feedModeMemory {
			p.mode, p.cursor = feedModeActions, 0
			return m, nil
		}
		m.state = viewStateMain
		return m, nil
	case "tab":
		p.mode = (p.mode + 1) % feedModeMemory
		p.cursor, p.loading = 0, true
		return m, m.feedPageReadCmd(p)
	case "m":
		p.mode, p.cursor, p.loading = feedModeMemory, 0, true
		return m, m.feedPageReadCmd(p)
	case "r", "R":
		p.loading = true
		return m, m.feedPageReadCmd(p)
	case "j", "down":
		if p.cursor < m.feedPageLen()-1 {
			p.cursor++
		}
	case "k", "up":
		if p.cursor > 0 {
			p.cursor--
		}
	case "d", "x", "delete":
		if p.mode != feedModeMemory {
			return m, nil
		}
		if p.cursor < len(p.bullets) {
			b := p.bullets[p.cursor]
			return m, m.feedActCmd(p.host, map[string]any{"op": "forget_memory", "memory_id": b.ID})
		}
		if i := p.cursor - len(p.bullets); i >= 0 && i < len(p.mutes) {
			mu := p.mutes[i]
			return m, m.feedActCmd(p.host, map[string]any{"op": "unmute", "scope": string(mu.Scope), "key": mu.Key})
		}
	}
	return m, nil
}

// feedBatchRow is one batch as the page lists it.
type feedBatchRow struct {
	id      int64
	actions int
	open    int
	first   time.Time
}

// feedBatchRows groups the page's actions by batch, newest first. serve has
// no batch listing, so the history is the batches that proposed actions.
func feedBatchRows(actions []domain.FeedAction) []feedBatchRow {
	byID := map[int64]*feedBatchRow{}
	for _, a := range actions {
		r := byID[a.BatchID]
		if r == nil {
			r = &feedBatchRow{id: a.BatchID, first: a.CreatedAt}
			byID[a.BatchID] = r
		}
		r.actions++
		if a.State == "proposed" || a.State == "snoozed" {
			r.open++
		}
		if a.CreatedAt.Before(r.first) {
			r.first = a.CreatedAt
		}
	}
	out := make([]feedBatchRow, 0, len(byID))
	for _, r := range byID {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id > out[j].id })
	return out
}

func (m *Model) renderFeedPage() string {
	p := m.feedPage
	if p == nil {
		return docStyle.Render("\n  No feed.\n")
	}
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	errStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	width := max(m.width-8, 40)

	var tabs []string
	for i, name := range feedModeNames {
		if i == p.mode {
			tabs = append(tabs, lipgloss.NewStyle().Bold(true).Render("["+name+"]"))
		} else {
			tabs = append(tabs, dim.Render(name))
		}
	}
	var b strings.Builder
	b.WriteString("\n  " + title.Render("Feed on "+remoteNameForHost(m.cfg, p.host)) + "\n")
	b.WriteString("  " + m.feedStatusLine(p.host) + "\n")
	b.WriteString("  " + strings.Join(tabs, "  ") + "\n\n")

	lines := m.feedPageLines(p, width)
	if p.loading && len(lines) == 0 {
		lines = []string{dim.Render("Reading…")}
	}
	room := max(m.height-12, 5)
	start := 0
	if p.cursor >= room {
		start = p.cursor - room + 1
	}
	if start > len(lines) {
		start = 0
	}
	end := min(len(lines), start+room)
	for _, l := range lines[start:end] {
		b.WriteString("  " + l + "\n")
	}
	b.WriteString("\n")
	if p.err != "" {
		b.WriteString("  " + errStyle.Render(p.err) + "\n")
	}
	help := "tab actions/digest/batches · m memory · j/k move · r reload · esc back"
	if p.mode == feedModeMemory {
		help = "d delete bullet or mute · j/k move · r reload · esc back"
	}
	b.WriteString("  " + dim.Render(help) + "\n")
	return docStyle.Render(b.String())
}

// feedStatusLine is the host's collector state, last batch and today's runs.
func (m *Model) feedStatusLine(host string) string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	st := m.feed[host]
	if st == nil || !st.read {
		return dim.Render("status not read yet")
	}
	s := st.status
	parts := []string{"collector " + s.Collector.State}
	if b := s.LastBatch; b != nil {
		line := fmt.Sprintf("last batch %d %s %s", b.ID, b.State, overviewTime(feedBatchTime(*b)))
		if b.Agent != "" {
			line += " · " + strings.TrimSpace(b.Agent+" "+b.Model)
		}
		parts = append(parts, line)
	}
	parts = append(parts, fmt.Sprintf("%d batches today", s.BatchesToday), fmt.Sprintf("%d pending events", s.Pending))
	return dim.Render(strings.Join(parts, " · "))
}

func (m *Model) feedPageLines(p *feedPageView, width int) []string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	cur := func(i int) string {
		if i == p.cursor {
			return "▸ "
		}
		return "  "
	}
	var lines []string
	switch p.mode {
	case feedModeDigest:
		if len(p.digest) == 0 && !p.loading {
			return []string{dim.Render("Nothing in the digest.")}
		}
		for i, e := range p.digest {
			line := fmt.Sprintf("%s%s %-7s %s", cur(i), overviewTime(e.At), e.Source, firstLineOf(e.Title))
			lines = append(lines, truncatePlain(line, width))
		}
	case feedModeBatches:
		rows := feedBatchRows(p.actions)
		if st := m.feed[p.host]; st != nil && st.status.LastBatch != nil {
			b := st.status.LastBatch
			lb := fmt.Sprintf("newest batch %d · %s · %d attempts · %d events", b.ID, b.State, b.Attempts, len(b.EventIDs))
			lines = append(lines, dim.Render(lb))
			if b.Summary != "" {
				lines = append(lines, dim.Render(truncatePlain("  "+firstLineOf(b.Summary), width)))
			}
			if b.Error != "" {
				lines = append(lines, dim.Render(truncatePlain("  error: "+firstLineOf(b.Error), width)))
			}
			lines = append(lines, "")
		}
		if len(rows) == 0 && !p.loading {
			lines = append(lines, dim.Render("No batch has proposed an action yet."))
		}
		for i, r := range rows {
			line := fmt.Sprintf("%sbatch %-5d %s  %d actions, %d open", cur(i), r.id, overviewTime(r.first), r.actions, r.open)
			lines = append(lines, line)
		}
	case feedModeMemory:
		lines = append(lines, lipgloss.NewStyle().Bold(true).Render("Memory"))
		if len(p.bullets) == 0 && !p.loading {
			lines = append(lines, dim.Render("  no bullets"))
		}
		for i, b := range p.bullets {
			lines = append(lines, truncatePlain(fmt.Sprintf("%s%s  %s", cur(i), b.ID, b.Text), width))
		}
		lines = append(lines, "", lipgloss.NewStyle().Bold(true).Render("Mutes"))
		if len(p.mutes) == 0 && !p.loading {
			lines = append(lines, dim.Render("  no mutes"))
		}
		for i, mu := range p.mutes {
			line := fmt.Sprintf("%s%-7s %s  until %s", cur(len(p.bullets)+i), mu.Scope, mu.Key, mu.Until.Local().Format("2006-01-02 15:04"))
			lines = append(lines, truncatePlain(line, width))
		}
	default:
		if len(p.actions) == 0 && !p.loading {
			return []string{dim.Render("No actions yet.")}
		}
		for i, a := range p.actions {
			prio := " "
			if a.Priority == "high" {
				prio = "!"
			}
			line := fmt.Sprintf("%s%s %-5d %-10s %-13s %s", cur(i), prio, a.ID, a.State, a.Kind, firstLineOf(a.Rationale))
			if a.Result != "" {
				line += dim.Render(" → " + firstLineOf(a.Result))
			}
			lines = append(lines, truncatePlain(line, width))
		}
	}
	return lines
}
