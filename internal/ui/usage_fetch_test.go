package ui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/bouwerp/aiman/internal/usage"
	"github.com/charmbracelet/x/ansi"
)

func TestApplyUsageToExtras(t *testing.T) {
	ratio := 0.81
	rep := usage.Report{
		Known: true,
		Total: usage.Summary{
			CostUSD:       1.234,
			Priced:        true,
			CacheHitRatio: &ratio,
		},
	}
	in := detailMetaExtras{Reviewing: true, WatchingPR: 12}
	got := applyUsageToExtras(in, rep)
	if !got.HasCost || got.CostUSD != 1.234 {
		t.Fatalf("cost: HasCost=%v CostUSD=%v", got.HasCost, got.CostUSD)
	}
	if !got.HasCache || got.CacheRatio != 0.81 {
		t.Fatalf("cache: HasCache=%v CacheRatio=%v", got.HasCache, got.CacheRatio)
	}
	if !got.Reviewing || got.WatchingPR != 12 {
		t.Fatalf("review state changed: %+v", got)
	}
}

func TestUsageMetaUnpricedShowsTokens(t *testing.T) {
	ratio := 0.5
	rep := usage.Report{
		Known: true,
		Total: usage.Summary{
			CostUSD: 4.5,
			Priced:  false,
			Tokens: domain.TokenCounts{
				Input:        200_000,
				Output:       12_000,
				CacheRead:    150_000,
				CacheWrite5m: 30_000,
				CacheWrite1h: 20_000,
			},
			CacheHitRatio: &ratio,
		},
	}
	got := applyUsageToExtras(detailMetaExtras{}, rep)
	if got.HasCost {
		t.Fatal("unpriced report set HasCost")
	}
	const wantTokens = int64(200_000 + 12_000 + 150_000 + 30_000 + 20_000)
	if got.Tokens != wantTokens {
		t.Fatalf("Tokens = %d, want %d", got.Tokens, wantTokens)
	}
	if !got.HasCache || got.CacheRatio != ratio {
		t.Fatalf("cache: HasCache=%v CacheRatio=%v", got.HasCache, got.CacheRatio)
	}
	line := ansi.Strip(renderMetaLine(domain.Session{AgentName: "claude"}, got, time.Now(), 200))
	if !strings.Contains(line, "412k tok") {
		t.Fatalf("meta line %q, want 412k tok", line)
	}
	if strings.Contains(line, "$") {
		t.Fatalf("unpriced meta line shows a dollar amount: %q", line)
	}
}

func TestUsageMetaUnknownShowsNothing(t *testing.T) {
	ratio := 0.81
	rep := usage.Report{
		Known: false,
		Total: usage.Summary{
			CostUSD:       1.234,
			Priced:        true,
			Tokens:        domain.TokenCounts{Input: 412_000},
			CacheHitRatio: &ratio,
		},
	}
	got := applyUsageToExtras(detailMetaExtras{
		HasCost: true, CostUSD: 9, Tokens: 412_000, HasCache: true, CacheRatio: 0.5, Reviewing: true,
	}, rep)
	if got.HasCost || got.HasCache {
		t.Fatalf("flags: HasCost=%v HasCache=%v", got.HasCost, got.HasCache)
	}
	if got.Tokens != 0 || got.CostUSD != 0 {
		t.Fatalf("stale usage left in place: %+v", got)
	}
	if !got.Reviewing {
		t.Fatal("review state was cleared")
	}
	line := ansi.Strip(renderMetaLine(domain.Session{}, got, time.Now(), 80))
	if strings.Contains(line, "$") || strings.Contains(line, "tok") {
		t.Fatalf("unknown usage rendered %q", line)
	}
}

func TestUsageEventFetchesSelectedOnly(t *testing.T) {
	selected := domain.Session{ID: "sel", RemoteHost: "regent0"}
	other := domain.Session{ID: "other", RemoteHost: "regent0"}
	m := eventModel(selected, other)
	m.list = list.New([]list.Item{item{session: selected}}, list.NewDefaultDelegate(), 40, 20)
	m.list.Select(0)
	m.ensureSessionStreams()
	m.streams["regent0"].todoSwept = true
	recent := time.Now()
	m.usageFetchedAt = map[string]time.Time{"sel": recent, "other": recent}

	// A lone stream wait or a live usage.get must not run: closing the channel
	// lets the wait return, and the deadline stops an SSH fetch from hanging.
	close(m.streams["regent0"].events)
	cmd := m.applySessionEvent(sessionEventMsg{
		host:  "regent0",
		event: server.SessionEvent{Type: "usage", ID: "sel"},
	})
	if cmd == nil {
		t.Fatal("selected usage event returned no command")
	}
	msg := runCmdMsg(t, cmd)
	batch, ok := msg.(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("selected usage event should batch the stream wait and a fetch, got %T len %d", msg, len(asBatch(msg)))
	}
	if !m.usageInFlight["sel"] {
		t.Fatal("selected usage event did not start a fetch")
	}
	if _, seen := m.eventSeen["sel"]; seen {
		t.Fatal("usage event wrote eventSeen, so it fell through to queuePreview")
	}

	cmd = m.applySessionEvent(sessionEventMsg{
		host:  "regent0",
		event: server.SessionEvent{Type: "usage", ID: "other"},
	})
	if cmd == nil {
		t.Fatal("non-selected usage event must still keep the stream")
	}
	if _, fetched := m.usageFetchedAt["other"]; fetched {
		t.Fatal("non-selected usage event left usageFetchedAt in place")
	}
	if m.usageInFlight["other"] {
		t.Fatal("non-selected usage event started a fetch")
	}
	if _, seen := m.eventSeen["other"]; seen {
		t.Fatal("non-selected usage event wrote eventSeen")
	}
}

func asBatch(msg tea.Msg) tea.BatchMsg {
	batch, _ := msg.(tea.BatchMsg)
	return batch
}

func runCmdMsg(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("command blocked; expected a batch, not a live fetch or stream wait")
	}
	return nil
}

func TestUsageFetchThrottled(t *testing.T) {
	s := domain.Session{ID: "s1", RemoteHost: "regent0"}
	m := eventModel(s)

	if m.maybeFetchUsage(s, false) == nil {
		t.Fatal("first fetch should start")
	}
	// The flight is cleared so the second call is refused by the interval.
	delete(m.usageInFlight, s.ID)
	if m.maybeFetchUsage(s, false) != nil {
		t.Fatal("second fetch inside 10s should be nil")
	}
	if m.maybeFetchUsage(s, true) == nil {
		t.Fatal("force should bypass the interval")
	}
	if m.maybeFetchUsage(s, true) != nil {
		t.Fatal("force must not start a fetch while one is in flight")
	}
}

func TestUsageMsgUpdatesExtras(t *testing.T) {
	m := eventModel(domain.Session{ID: "s", RemoteHost: "regent0"})
	m.state = viewStateMenu
	prev := detailMetaExtras{HasCost: true, CostUSD: 2, Tokens: 10, HasCache: true, CacheRatio: 0.2, Reviewing: true}
	m.metaExtras = map[string]detailMetaExtras{"s": prev}
	m.usageInFlight = map[string]bool{"s": true}

	updated, _ := m.Update(usageMsg{sessionID: "s", err: errUsageTest})
	m = updated.(*Model)
	if m.metaExtras["s"] != prev {
		t.Fatalf("error overwrote extras: %+v", m.metaExtras["s"])
	}
	if m.usageInFlight["s"] {
		t.Fatal("error left the fetch in flight")
	}
	if !strings.Contains(strings.Join(m.consoleLog, "\n"), "usage failed") {
		t.Fatalf("error was not logged: %#v", m.consoleLog)
	}

	m.usageInFlight["s"] = true
	ratio := 0.81
	updated, _ = m.Update(usageMsg{
		sessionID: "s",
		rep: usage.Report{
			Known: true,
			Total: usage.Summary{CostUSD: 1.234, Priced: true, CacheHitRatio: &ratio},
		},
	})
	m = updated.(*Model)
	got := m.metaExtras["s"]
	if !got.HasCost || got.CostUSD != 1.234 || !got.HasCache || got.CacheRatio != 0.81 {
		t.Fatalf("extras: %+v", got)
	}
	if m.usageInFlight["s"] {
		t.Fatal("success left the fetch in flight")
	}
	if !got.Reviewing {
		t.Fatal("success cleared review state")
	}
}

var errUsageTest = usageTestError("usage failed")

type usageTestError string

func (e usageTestError) Error() string { return string(e) }
