package feedcollect

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// RPCFunc calls one serve method as the operator and decodes the result into
// out.
type RPCFunc func(ctx context.Context, method string, params any, out any) error

// AimanEvery is how often the aiman source polls serve.
const AimanEvery = 15 * time.Second

const (
	aimanNotifyCursor = "notify"
	aimanLedgerCursor = "ledger"
	aimanPageLimit    = 500
	aimanMaxPages     = 10
)

// aimanNotifyKinds are the notification kinds the feed reads.
var aimanNotifyKinds = map[string]bool{
	"waiting_input": true,
	"ci_escalation": true,
	"ended":         true,
	"error":         true,
	"review_done":   true,
}

// aimanLedgerKinds are the ledger kinds the feed reads. A child's facts reach
// the feed through these rows on its parent's ledger.
var aimanLedgerKinds = []string{
	"child.landed", "child.finished", "design.published",
	"consult.finished", "review.posted", "harness.verdict",
}

type aimanSource struct {
	rpc      RPCFunc
	rootOnly bool

	mu sync.Mutex
	// keep remembers, per session seen in session.list, whether its events
	// count, so a child whose row is gone by the time its last notification
	// is read is still left out.
	keep  map[string]bool
	names map[string]string
}

// NewAimanSource reads serve's notifications and ledger. With rootOnly it
// keeps the events of root sessions only (no parent, not a hidden helper).
func NewAimanSource(rpc RPCFunc, rootOnly bool) Source {
	return &aimanSource{rpc: rpc, rootOnly: rootOnly, keep: map[string]bool{}, names: map[string]string{}}
}

func (a *aimanSource) Name() domain.FeedSource { return "aiman" }
func (a *aimanSource) Every() time.Duration    { return AimanEvery }

type aimanNotification struct {
	Seq       int64  `json:"seq"`
	SessionID string `json:"session_id"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Host      string `json:"host"`
	Text      string `json:"text"`
	At        string `json:"at"`
}

type aimanLedgerRow struct {
	RowID     int64           `json:"rowid"`
	SessionID string          `json:"session_id"`
	Seq       int64           `json:"seq"`
	At        time.Time       `json:"at"`
	Actor     string          `json:"actor"`
	Kind      string          `json:"kind"`
	Payload   json.RawMessage `json:"payload"`
}

// Poll reads what serve added after the cursors. A missing cursor starts at
// serve's current tail, so a first start replays no history. Any RPC failure
// fails the whole poll, so the collector keeps both cursors.
func (a *aimanSource) Poll(ctx context.Context, cursors map[string]string, mutes []domain.FeedMute) ([]domain.FeedEvent, map[string]string, error) {
	next := map[string]string{}
	notifs, notifyCur, err := a.readNotifications(ctx, cursors[aimanNotifyCursor])
	if err != nil {
		return nil, nil, fmt.Errorf("notify.since: %w", err)
	}
	next[aimanNotifyCursor] = notifyCur
	rows, ledgerCur, err := a.readLedger(ctx, cursors[aimanLedgerCursor])
	if err != nil {
		return nil, nil, fmt.Errorf("session.events_since: %w", err)
	}
	next[aimanLedgerCursor] = ledgerCur

	if len(notifs) == 0 && len(rows) == 0 {
		return nil, next, nil
	}
	if a.rootOnly {
		if err := a.refreshSessions(ctx); err != nil {
			return nil, nil, fmt.Errorf("session.list: %w", err)
		}
	}
	var evs []domain.FeedEvent
	for _, n := range notifs {
		if !aimanNotifyKinds[n.Kind] || !a.counts(n.SessionID) {
			continue
		}
		evs = append(evs, finish(a.notifyEvent(n), a.Name()))
	}
	for _, r := range rows {
		if !slices.Contains(aimanLedgerKinds, r.Kind) || !a.counts(r.SessionID) {
			continue
		}
		evs = append(evs, finish(a.ledgerEvent(r), a.Name()))
	}
	return unmuted(evs, mutes), next, nil
}

func (a *aimanSource) readNotifications(ctx context.Context, cursor string) ([]aimanNotification, string, error) {
	var res struct {
		Entries []aimanNotification `json:"entries"`
		LastSeq int64               `json:"last_seq"`
	}
	after, ok := parseCursor(cursor)
	if !ok {
		if err := a.rpc(ctx, "notify.since", map[string]any{"after": -1}, &res); err != nil {
			return nil, "", err
		}
		return nil, strconv.FormatInt(res.LastSeq, 10), nil
	}
	var out []aimanNotification
	for range aimanMaxPages {
		res.Entries = nil
		if err := a.rpc(ctx, "notify.since", map[string]any{"after": after, "limit": aimanPageLimit}, &res); err != nil {
			return nil, "", err
		}
		for _, n := range res.Entries {
			if n.Seq > after {
				after = n.Seq
			}
		}
		out = append(out, res.Entries...)
		if len(res.Entries) < aimanPageLimit {
			break
		}
	}
	return out, strconv.FormatInt(after, 10), nil
}

func (a *aimanSource) readLedger(ctx context.Context, cursor string) ([]aimanLedgerRow, string, error) {
	var res struct {
		Events []aimanLedgerRow `json:"events"`
		Next   int64            `json:"next"`
	}
	after, ok := parseCursor(cursor)
	if !ok {
		if err := a.rpc(ctx, "session.events_since", map[string]any{"after_rowid": -1}, &res); err != nil {
			return nil, "", err
		}
		return nil, strconv.FormatInt(res.Next, 10), nil
	}
	var out []aimanLedgerRow
	for range aimanMaxPages {
		res.Events, res.Next = nil, 0
		params := map[string]any{"after_rowid": after, "kinds": aimanLedgerKinds, "limit": aimanPageLimit}
		if err := a.rpc(ctx, "session.events_since", params, &res); err != nil {
			return nil, "", err
		}
		out = append(out, res.Events...)
		if res.Next > after {
			after = res.Next
		}
		if len(res.Events) < aimanPageLimit {
			break
		}
	}
	return out, strconv.FormatInt(after, 10), nil
}

func parseCursor(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// refreshSessions reads session.list and remembers which sessions count.
func (a *aimanSource) refreshSessions(ctx context.Context) error {
	var res struct {
		Sessions []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			ParentID string `json:"parent_id"`
			Role     string `json:"role"`
		} `json:"sessions"`
	}
	if err := a.rpc(ctx, "session.list", map[string]any{}, &res); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, s := range res.Sessions {
		a.keep[s.ID] = s.ParentID == "" && !domain.SessionRole(s.Role).Hidden()
		if s.Name != "" {
			a.names[s.ID] = s.Name
		}
	}
	return nil
}

// counts reports whether a session's events go to the feed. Without
// rootOnly every session counts. With it, a session the source has seen as a
// child or a hidden helper does not; one it has never seen (a root removed
// before the source first listed it) does. An event with no session belongs
// to no thread and is left out.
func (a *aimanSource) counts(id string) bool {
	if id == "" {
		return false
	}
	if !a.rootOnly {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	keep, seen := a.keep[id]
	return !seen || keep
}

func (a *aimanSource) name(id, fallback string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n := a.names[id]; n != "" {
		return n
	}
	if fallback != "" {
		return fallback
	}
	return id
}

func (a *aimanSource) notifyEvent(n aimanNotification) domain.FeedEvent {
	at, err := time.Parse(time.RFC3339, n.At)
	if err != nil {
		at = time.Now()
	}
	name := a.name(n.SessionID, n.Label)
	return domain.FeedEvent{
		ExternalID: "notify:" + strconv.FormatInt(n.Seq, 10),
		At:         at,
		Actor:      n.SessionID,
		ActorName:  name,
		Kind:       n.Kind,
		Title:      fmt.Sprintf("%s: %s", name, n.Kind),
		Body:       n.Text,
		ThreadKey:  domain.FeedThreadKeySession(n.SessionID),
		Class:      "session",
		Urgent:     n.Kind == string(domain.NotifyWaitingInput),
	}
}

func (a *aimanSource) ledgerEvent(r aimanLedgerRow) domain.FeedEvent {
	at := r.At
	if at.IsZero() {
		at = time.Now()
	}
	name := a.name(r.SessionID, "")
	body := string(r.Payload)
	var compact bytes.Buffer
	if json.Compact(&compact, r.Payload) == nil {
		body = compact.String()
	}
	return domain.FeedEvent{
		ExternalID: "ledger:" + strconv.FormatInt(r.RowID, 10),
		At:         at,
		Actor:      r.SessionID,
		ActorName:  name,
		Kind:       r.Kind,
		Title:      fmt.Sprintf("%s: %s", name, r.Kind),
		Body:       body,
		ThreadKey:  domain.FeedThreadKeySession(r.SessionID),
		Class:      "session",
	}
}
