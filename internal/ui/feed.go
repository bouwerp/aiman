package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/infra/config"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/usecase"
)

// feedRPCTimeout bounds every feed call.
const feedRPCTimeout = 30 * time.Second

// feedRPC calls one feed.* method on a remote's agent API. Tests replace it.
var feedRPC = func(ctx context.Context, r config.Remote, method string, params any) (json.RawMessage, error) {
	return usecase.ServeRPC(ctx, dialRemoteSSH(r), method, params)
}

// feedOpenStates are the states the home panel lists: what still waits for
// the operator or is being carried out. Snoozed actions come back on their
// own as proposed.
var feedOpenStates = []domain.FeedActionState{"proposed", "approved", "running"}

// feedStatus is serve's feed.status answer.
type feedStatus struct {
	Enabled   bool `json:"enabled"`
	Collector struct {
		State  string `json:"state"`
		Error  string `json:"error,omitempty"`
		Health []struct {
			Source   string    `json:"source"`
			LastPoll time.Time `json:"last_poll"`
			Error    string    `json:"error,omitempty"`
		} `json:"health"`
	} `json:"collector"`
	HighWater    int64             `json:"high_water"`
	Pending      int               `json:"pending"`
	OpenActions  int               `json:"open_actions"`
	HighActions  int               `json:"high_actions"`
	LastBatch    *domain.FeedBatch `json:"last_batch,omitempty"`
	BatchesToday int               `json:"batches_today"`
	Settings     json.RawMessage   `json:"settings,omitempty"`
}

// feedMemoryBullet is one line of the feed agent's memory.
type feedMemoryBullet struct {
	ID   string    `json:"id"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// feedHostState is what the TUI knows of one host's feed.
type feedHostState struct {
	read     bool
	inflight bool
	// off is true when serve has no feed (an older serve) or it is disabled.
	off     bool
	status  feedStatus
	actions []domain.FeedAction
	// events are the events the open actions answer, by id.
	events map[int64]domain.FeedEvent
	readAt time.Time
	err    string
}

// feedReadMsg is one read of a host's status, open actions and the events
// they answer.
type feedReadMsg struct {
	host    string
	status  feedStatus
	actions []domain.FeedAction
	events  map[int64]domain.FeedEvent
	off     bool
	err     error
}

// feedActedMsg is serve's answer to one feed.act.
type feedActedMsg struct {
	host   string
	op     string
	action *domain.FeedAction
	memory *feedMemoryMsg
	err    error
}

// feedRemote finds the configured remote for host.
func (m *Model) feedRemote(host string) (config.Remote, bool) {
	for _, r := range agentAPIRemotes(m.cfg) {
		if r.Host == host {
			return r, true
		}
	}
	return config.Remote{}, false
}

func (m *Model) feedHost(host string) *feedHostState {
	if m.feed == nil {
		m.feed = map[string]*feedHostState{}
	}
	st := m.feed[host]
	if st == nil {
		st = &feedHostState{}
		m.feed[host] = st
	}
	return st
}

// feedReadCmd reads one host's feed. A background read takes a slot only when
// one is free and returns nil otherwise; a foreground read waits for one.
func (m *Model) feedReadCmd(host string, foreground bool) tea.Cmd {
	r, ok := m.feedRemote(host)
	if !ok {
		return nil
	}
	st := m.feedHost(host)
	if st.inflight && !foreground {
		return nil
	}
	if foreground {
		budget := m.hostBudget
		st.inflight = true
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), feedRPCTimeout)
			defer cancel()
			release, err := budgetAcquire(ctx, budget, host)
			if err != nil {
				return feedReadMsg{host: host, err: err}
			}
			msg := readFeed(ctx, r)
			release(callTimedOut(msg.err))
			return msg
		}
	}
	release, ok := m.budgetTry(host)
	if !ok {
		return nil
	}
	st.inflight = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), feedRPCTimeout)
		defer cancel()
		msg := readFeed(ctx, r)
		release(callTimedOut(msg.err))
		return msg
	}
}

// readFeed calls feed.status, feed.actions and feed.events {ids} in turn.
func readFeed(ctx context.Context, r config.Remote) feedReadMsg {
	out := feedReadMsg{host: r.Host}
	raw, err := feedRPC(ctx, r, "feed.status", map[string]any{})
	if err != nil {
		if feedUnsupported(err) {
			out.off = true
			return out
		}
		out.err = err
		return out
	}
	if err := json.Unmarshal(raw, &out.status); err != nil {
		out.err = fmt.Errorf("feed.status: %w", err)
		return out
	}
	if !out.status.Enabled {
		out.off = true
		return out
	}
	raw, err = feedRPC(ctx, r, "feed.actions", map[string]any{"state": feedOpenStates})
	if err != nil {
		out.err = err
		return out
	}
	var acts struct {
		Actions []domain.FeedAction `json:"actions"`
	}
	if err := json.Unmarshal(raw, &acts); err != nil {
		out.err = fmt.Errorf("feed.actions: %w", err)
		return out
	}
	out.actions = acts.Actions
	var ids []int64
	for _, a := range acts.Actions {
		for _, id := range a.Answers {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	out.events = map[int64]domain.FeedEvent{}
	if len(ids) == 0 {
		return out
	}
	// The events only name sources and links: a failed read keeps the rows.
	if raw, err = feedRPC(ctx, r, "feed.events", map[string]any{"ids": ids}); err == nil {
		var evs struct {
			Events []domain.FeedEvent `json:"events"`
		}
		if json.Unmarshal(raw, &evs) == nil {
			for _, e := range evs.Events {
				out.events[e.ID] = e
			}
		}
	}
	return out
}

// feedUnsupported is an older serve (unknown method) or a feed that is off.
func feedUnsupported(err error) bool {
	var rpcErr *usecase.RPCError
	if errors.As(err, &rpcErr) {
		return rpcErr.Code == server.CodeInvalidParams || rpcErr.Code == server.CodeUnavailable
	}
	return errors.Is(err, usecase.ErrRPCUnsupported)
}

// fetchFeedAll reads every remote's feed once, in the background.
func (m *Model) fetchFeedAll() tea.Cmd {
	var cmds []tea.Cmd
	seen := map[string]bool{}
	for _, r := range agentAPIRemotes(m.cfg) {
		if seen[r.Host] || r.Host == "" {
			continue
		}
		seen[r.Host] = true
		if cmd := m.feedEventFetch(r.Host); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

// feedEventFetch rereads a host's feed when serve says something changed. A
// read that finds no slot, or one already in flight, is remembered and run
// by drainPending.
func (m *Model) feedEventFetch(host string) tea.Cmd {
	if _, ok := m.feedRemote(host); !ok {
		return nil
	}
	cmd := m.feedReadCmd(host, false)
	if cmd == nil {
		m.markPending("feed.read", host, "")
	}
	return cmd
}

// feedFocusFetch rereads every host with a feed, in the foreground.
func (m *Model) feedFocusFetch() tea.Cmd {
	var cmds []tea.Cmd
	for _, host := range m.feedHosts() {
		cmds = append(cmds, m.feedReadCmd(host, true))
	}
	return tea.Batch(cmds...)
}

// feedHosts are the hosts whose feed is on, sorted.
func (m *Model) feedHosts() []string {
	var hosts []string
	for host, st := range m.feed {
		if st != nil && st.read && !st.off {
			hosts = append(hosts, host)
		}
	}
	sort.Strings(hosts)
	return hosts
}

// applyFeedRead stores a read and keeps the focused panel on the same action.
func (m *Model) applyFeedRead(msg feedReadMsg) tea.Cmd {
	st := m.feedHost(msg.host)
	st.inflight = false
	cmds := []tea.Cmd{m.drainPending()}
	if msg.err != nil {
		st.err = msg.err.Error()
		return tea.Batch(cmds...)
	}
	keep := m.selectedFeedKey()
	rowsBefore := m.feedPanelHeight()
	st.err = ""
	st.read, st.readAt, st.off = true, time.Now(), msg.off
	st.status = msg.status
	st.actions, st.events = msg.actions, msg.events
	m.restoreFeedCursor(keep)
	if m.feedPanelHeight() != rowsBefore && m.width > 0 && m.height > 0 {
		m.applySessionChrome()
	}
	if p := m.feedPage; p != nil && p.host == msg.host && m.state == viewStateFeed {
		cmds = append(cmds, m.feedPageReadCmd(p))
	}
	return tea.Batch(cmds...)
}

// feedActCmd sends one feed.act.
func (m *Model) feedActCmd(host string, params map[string]any) tea.Cmd {
	r, ok := m.feedRemote(host)
	if !ok {
		return nil
	}
	budget := m.hostBudget
	op, _ := params["op"].(string)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), feedRPCTimeout)
		defer cancel()
		release, err := budgetAcquire(ctx, budget, host)
		if err != nil {
			return feedActedMsg{host: host, op: op, err: err}
		}
		raw, err := feedRPC(ctx, r, "feed.act", params)
		release(callTimedOut(err))
		if err != nil {
			return feedActedMsg{host: host, op: op, err: err}
		}
		if op == "forget_memory" || op == "unmute" {
			mem := feedMemoryResult(host, raw, nil)
			return feedActedMsg{host: host, op: op, memory: &mem}
		}
		var res struct {
			Action domain.FeedAction `json:"action"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return feedActedMsg{host: host, op: op, err: fmt.Errorf("feed.act: %w", err)}
		}
		return feedActedMsg{host: host, op: op, action: &res.Action}
	}
}

// applyFeedActed replaces the action with serve's answer; one that left the
// open states leaves the panel.
func (m *Model) applyFeedActed(msg feedActedMsg) tea.Cmd {
	if msg.err != nil {
		return m.showToast("feed: "+msg.err.Error(), true, 6*time.Second)
	}
	if msg.memory != nil {
		m.applyFeedMemory(*msg.memory)
		return nil
	}
	if msg.action == nil {
		return nil
	}
	a := *msg.action
	st := m.feedHost(msg.host)
	keep := m.selectedFeedKey()
	for i := range st.actions {
		if st.actions[i].ID != a.ID {
			continue
		}
		if slices.Contains(feedOpenStates, a.State) {
			st.actions[i] = a
		} else {
			st.actions = slices.Delete(st.actions, i, i+1)
		}
		break
	}
	m.restoreFeedCursor(keep)
	if m.width > 0 && m.height > 0 {
		m.applySessionChrome()
	}
	if p := m.feedPage; p != nil && p.host == msg.host {
		for i := range p.actions {
			if p.actions[i].ID == a.ID {
				p.actions[i] = a
			}
		}
	}
	return m.showToast(feedActToast(a), a.State == "failed", 5*time.Second)
}

func feedActToast(a domain.FeedAction) string {
	switch a.State {
	case "dismissed":
		return fmt.Sprintf("feed: action %d dismissed", a.ID)
	case "snoozed":
		return fmt.Sprintf("feed: action %d snoozed", a.ID)
	case "failed":
		return fmt.Sprintf("feed: action %d failed: %s", a.ID, a.Result)
	case "done":
		if a.Result != "" {
			return fmt.Sprintf("feed: action %d done: %s", a.ID, a.Result)
		}
		return fmt.Sprintf("feed: action %d done", a.ID)
	}
	return fmt.Sprintf("feed: action %d %s", a.ID, a.State)
}

// routeFeedMsg applies feed results on any view.
func (m *Model) routeFeedMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case feedReadMsg:
		return m, m.applyFeedRead(msg)
	case feedActedMsg:
		return m, tea.Batch(m.drainPending(), m.applyFeedActed(msg))
	case feedPageMsg:
		m.applyFeedPage(msg)
		return m, m.drainPending()
	case feedMemoryMsg:
		m.applyFeedMemory(msg)
		return m, m.drainPending()
	}
	return m, nil
}

// feedCounts are the open actions on every host and the high-priority ones
// still waiting for the operator.
func (m *Model) feedCounts() (open, high int) {
	for _, host := range m.feedHosts() {
		for _, a := range m.feed[host].actions {
			open++
			if a.Priority == "high" && a.State == "proposed" {
				high++
			}
		}
	}
	return open, high
}

// feedLastBatch is the newest last batch any host reports.
func (m *Model) feedLastBatch() *domain.FeedBatch {
	var last *domain.FeedBatch
	for _, host := range m.feedHosts() {
		b := m.feed[host].status.LastBatch
		if b == nil {
			continue
		}
		if last == nil || feedBatchTime(*b).After(feedBatchTime(*last)) {
			last = b
		}
	}
	return last
}

func feedBatchTime(b domain.FeedBatch) time.Time {
	if !b.EndedAt.IsZero() {
		return b.EndedAt
	}
	return b.StartedAt
}

// feedItem is one open action and the host it lives on.
type feedItem struct {
	host   string
	action domain.FeedAction
}

// feedItems are the open actions of every host, highest priority first, then
// oldest first.
func (m *Model) feedItems() []feedItem {
	var out []feedItem
	for _, host := range m.feedHosts() {
		for _, a := range m.feed[host].actions {
			out = append(out, feedItem{host: host, action: a})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := feedPriorityRank(out[i].action.Priority), feedPriorityRank(out[j].action.Priority)
		if pi != pj {
			return pi < pj
		}
		if !out[i].action.CreatedAt.Equal(out[j].action.CreatedAt) {
			return out[i].action.CreatedAt.Before(out[j].action.CreatedAt)
		}
		return out[i].action.ID < out[j].action.ID
	})
	return out
}

func feedPriorityRank(p domain.FeedPriority) int {
	switch p {
	case "high":
		return 0
	case "low":
		return 2
	}
	return 1
}

// feedAnswered is the first event the action answers, when the host read it.
func (m *Model) feedAnswered(it feedItem) (domain.FeedEvent, bool) {
	st := m.feed[it.host]
	if st == nil {
		return domain.FeedEvent{}, false
	}
	for _, id := range it.action.Answers {
		if ev, ok := st.events[id]; ok {
			return ev, true
		}
	}
	return domain.FeedEvent{}, false
}

// feedAnswersDigest reports whether any answered event is a channel digest.
func (m *Model) feedAnswersDigest(it feedItem) bool {
	st := m.feed[it.host]
	if st == nil {
		return false
	}
	for _, id := range it.action.Answers {
		if ev, ok := st.events[id]; ok && ev.Kind == "channel_digest" {
			return true
		}
	}
	return false
}

// feedOptionText is the option's text param, else its label.
func feedOptionText(o domain.FeedOption) string {
	var p map[string]any
	if json.Unmarshal(o.Params, &p) == nil {
		if t, ok := p["text"].(string); ok && t != "" {
			return t
		}
	}
	return o.Label
}

// feedEditedParams is the option's params with text replaced, keeping the
// rest.
func feedEditedParams(o domain.FeedOption, text string) json.RawMessage {
	p := map[string]any{}
	_ = json.Unmarshal(o.Params, &p)
	if p == nil {
		p = map[string]any{}
	}
	p["text"] = text
	b, _ := json.Marshal(p)
	return b
}

// feedURL is the link o opens: the answered event's, else a read option's,
// else the first trace entry's.
func (m *Model) feedURL(it feedItem) string {
	if ev, ok := m.feedAnswered(it); ok && ev.URL != "" {
		return ev.URL
	}
	for _, o := range it.action.Options {
		var p map[string]any
		if json.Unmarshal(o.Params, &p) == nil {
			if u, ok := p["url"].(string); ok && u != "" {
				return u
			}
		}
	}
	for _, t := range it.action.Trace {
		if t.URL != "" {
			return t.URL
		}
	}
	return ""
}
