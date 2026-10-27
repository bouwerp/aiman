package usage

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// Query selects usage buckets and how to roll them up.
type Query struct {
	SessionID       string // resolved by the caller
	Group           string
	Issue           string
	Day             string // "2006-01-02" in the query's zone
	By              string // "", "session", "issue", "day", "model"
	Days            int    // 0 = all time; otherwise hours in [now minus N days, now] in the query zone
	TZOffsetMinutes int
}

// Summary is priced token totals for one slice of usage.
type Summary struct {
	Tokens        domain.TokenCounts `json:"tokens"`
	CostUSD       float64            `json:"cost_usd"`
	Priced        bool               `json:"priced"`  // some tokens had a confirmed price
	Partial       bool               `json:"partial"` // some tokens had none
	CacheHitRatio *float64           `json:"cache_hit_ratio,omitempty"`
}

// Row is one grouped summary.
type Row struct {
	Key     string  `json:"key"`
	Summary Summary `json:"summary"`
}

// Report is the priced rollup for a query.
type Report struct {
	Total    Summary  `json:"total"`
	Rows     []Row    `json:"rows,omitempty"`
	Unpriced []string `json:"unpriced,omitempty"` // sorted model ids
	Known    bool     `json:"known"`              // false: no transcript we can read
	// Context fields are the latest turn for a one-session query.
	// A query that matches any other number of sessions leaves them zero.
	// ContextPct is ContextTokens/ContextWindow when the window is positive.
	ContextTokens int64   `json:"context_tokens"`
	ContextWindow int64   `json:"context_window"`
	ContextPct    float64 `json:"context_pct"`
	ContextSource string  `json:"context_source"`
}

// NoTicketKey is the issue row for a session whose issue is empty.
const NoTicketKey = "(no ticket)"

// Reporter prices buckets from Store.
type Reporter struct {
	Store  Store
	Prices PriceTable
	Now    func() time.Time
}

// Report filters sessions by group and issue, then prices the buckets in the window.
func (r Reporter) Report(ctx context.Context, q Query) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	metas, ids, err := r.sessionIDs(ctx, q)
	if err != nil {
		return Report{}, err
	}
	buckets, known, err := r.buckets(ctx, ids, q)
	if err != nil {
		return Report{}, err
	}
	summary, unpriced := Summarize(buckets, r.Prices)
	rep := Report{
		Total:    summary,
		Rows:     rollup(buckets, metas, r.Prices, q),
		Unpriced: unpriced,
		Known:    known,
	}
	return attachContext(rep, metas, ids), nil
}

// HealthFor aggregates stored per-session health over the query selection.
// Known is true only when at least one session is selected and every
// selected session is Known.
func (r Reporter) HealthFor(ctx context.Context, q Query) (Health, error) {
	metas, ids, err := r.sessionIDs(ctx, q)
	if err != nil {
		return Health{}, err
	}
	return aggregateHealth(metas, ids), nil
}

func aggregateHealth(metas map[string]domain.UsageSessionMeta, ids []string) Health {
	if len(ids) == 0 {
		return Health{}
	}
	var out Health
	out.Known = true
	for _, id := range ids {
		meta, ok := metas[id]
		if !ok || !meta.HealthKnown {
			out.Known = false
		}
		if !ok {
			continue
		}
		out.ToolCalls += meta.HealthToolCalls
		out.ReadCalls += meta.HealthReadCalls
		out.EditCalls += meta.HealthEditCalls
		out.EditsWithoutRead += meta.HealthEditsWithoutRead
		out.Retries += meta.HealthRetries
		if !meta.HealthFirstTestAt.IsZero() && (out.FirstTestAt.IsZero() || meta.HealthFirstTestAt.Before(out.FirstTestAt)) {
			out.FirstTestAt = meta.HealthFirstTestAt
		}
	}
	if out.EditCalls > 0 {
		out.ReadEditRatio = float64(out.ReadCalls) / float64(out.EditCalls)
	}
	return out
}

// attachContext copies the stored last turn onto a one-session report.
// Context is not a sum across sessions and is not taken from usage_buckets.
func attachContext(rep Report, metas map[string]domain.UsageSessionMeta, ids []string) Report {
	if len(ids) != 1 {
		return rep
	}
	meta, ok := metas[ids[0]]
	if !ok {
		return rep
	}
	rep.ContextTokens = meta.LastInputTokens
	rep.ContextWindow = meta.ContextWindow
	rep.ContextSource = meta.ContextSource
	if meta.ContextWindow > 0 {
		rep.ContextPct = float64(meta.LastInputTokens) / float64(meta.ContextWindow)
	}
	return rep
}

// Summarize prices each bucket. Unpriced models are a sorted unique list and add no cost.
// Priced is true when any tokens had a confirmed price. Partial is true when any tokens had none.
func Summarize(buckets []domain.UsageBucket, prices PriceTable) (Summary, []string) {
	var summary Summary
	seen := map[string]struct{}{}
	var unpriced []string
	for _, bucket := range buckets {
		summary.Tokens = summary.Tokens.Add(bucket.Tokens)
		if bucket.Tokens.IsZero() {
			continue
		}
		price, ok := prices.Lookup(bucket.Model)
		if !ok {
			summary.Partial = true
			if _, dup := seen[bucket.Model]; dup {
				continue
			}
			seen[bucket.Model] = struct{}{}
			unpriced = append(unpriced, bucket.Model)
			continue
		}
		summary.Priced = true
		summary.CostUSD += price.Cost(bucket.Tokens)
	}
	if ratio, ok := summary.Tokens.CacheHitRatio(); ok {
		summary.CacheHitRatio = &ratio
	}
	slices.Sort(unpriced)
	return summary, unpriced
}

func (r Reporter) sessionIDs(ctx context.Context, q Query) (map[string]domain.UsageSessionMeta, []string, error) {
	listed, err := r.Store.UsageSessions(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("listing usage sessions: %w", err)
	}
	metas := make(map[string]domain.UsageSessionMeta, len(listed))
	var ids []string
	for _, meta := range listed {
		metas[meta.SessionID] = meta
		if sessionSelected(meta, q) {
			ids = append(ids, meta.SessionID)
		}
	}
	// A caller-resolved id with no meta still counts when group and issue are unset.
	if q.SessionID != "" && q.Group == "" && q.Issue == "" && !slices.Contains(ids, q.SessionID) {
		ids = append(ids, q.SessionID)
	}
	return metas, ids, nil
}

func sessionSelected(meta domain.UsageSessionMeta, q Query) bool {
	if q.SessionID != "" && meta.SessionID != q.SessionID {
		return false
	}
	if q.Group != "" && meta.Group != q.Group {
		return false
	}
	if q.Issue != "" && meta.IssueKey != q.Issue {
		return false
	}
	return true
}

func (r Reporter) buckets(ctx context.Context, ids []string, q Query) ([]domain.UsageBucket, bool, error) {
	if len(ids) == 0 {
		return nil, false, nil
	}
	raw, err := r.Store.UsageBuckets(ctx, ids, time.Time{})
	if err != nil {
		return nil, false, fmt.Errorf("listing usage buckets: %w", err)
	}
	known := len(raw) > 0
	if !known {
		ok, err := r.anyCursor(ctx, ids)
		if err != nil {
			return nil, false, err
		}
		known = ok
	}
	var kept []domain.UsageBucket
	for _, bucket := range raw {
		if !r.inWindow(bucket.Hour, q) || !inDay(bucket.Hour, q) {
			continue
		}
		kept = append(kept, bucket)
	}
	return kept, known, nil
}

func (r Reporter) anyCursor(ctx context.Context, ids []string) (bool, error) {
	for _, id := range ids {
		cursors, err := r.Store.UsageCursors(ctx, id)
		if err != nil {
			return false, fmt.Errorf("listing usage cursors: %w", err)
		}
		if len(cursors) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// inWindow keeps hours in the trailing window [now minus N days, now] in the query zone.
// A zero or negative Days is all time. An hour after now is outside the window.
func (r Reporter) inWindow(hour time.Time, q Query) bool {
	if q.Days <= 0 {
		return true
	}
	now := r.now().In(queryLocation(q.TZOffsetMinutes))
	start := now.AddDate(0, 0, -q.Days)
	h := hour.UTC()
	return !h.Before(start) && !h.After(now)
}

func inDay(hour time.Time, q Query) bool {
	if q.Day == "" {
		return true
	}
	return dayKey(hour, q.TZOffsetMinutes) == q.Day
}

func (r Reporter) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func queryLocation(offsetMin int) *time.Location {
	return time.FixedZone("usage", offsetMin*60)
}

func dayKey(hour time.Time, offsetMin int) string {
	return hour.In(queryLocation(offsetMin)).Format(time.DateOnly)
}

func rollup(buckets []domain.UsageBucket, metas map[string]domain.UsageSessionMeta, prices PriceTable, q Query) []Row {
	if q.By != "session" && q.By != "issue" && q.By != "day" && q.By != "model" {
		return nil
	}
	grouped := map[string][]domain.UsageBucket{}
	for _, bucket := range buckets {
		key := bucketKey(bucket, metas, q)
		grouped[key] = append(grouped[key], bucket)
	}
	if len(grouped) == 0 {
		return nil
	}
	rows := make([]Row, 0, len(grouped))
	for key, group := range grouped {
		summary, _ := Summarize(group, prices)
		rows = append(rows, Row{Key: key, Summary: summary})
	}
	sortRows(rows, q.By)
	return rows
}

func bucketKey(bucket domain.UsageBucket, metas map[string]domain.UsageSessionMeta, q Query) string {
	switch q.By {
	case "session":
		return bucket.SessionID
	case "issue":
		if issue := metas[bucket.SessionID].IssueKey; issue != "" {
			return issue
		}
		return NoTicketKey
	case "day":
		return dayKey(bucket.Hour, q.TZOffsetMinutes)
	default:
		return bucket.Model
	}
}

func sortRows(rows []Row, by string) {
	slices.SortFunc(rows, func(a, b Row) int {
		if by == "day" {
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

// FormatUsageCost renders a priced estimate. An unpriced summary is empty so
// the caller can show tokens instead. A partial mix keeps the priced amount
// and suffixes "+". A positive amount under one cent stays "~$<0.01". An
// amount of $100 or more drops the cents.
func FormatUsageCost(s Summary) string {
	if !s.Priced {
		return ""
	}
	text := formatUSD(s.CostUSD)
	if s.Partial {
		text += "+"
	}
	return text
}

func formatUSD(cost float64) string {
	if cost > 0 && cost < 0.01 {
		return "~$<0.01"
	}
	if cost >= 100 {
		return fmt.Sprintf("~$%.0f", cost)
	}
	return fmt.Sprintf("~$%.2f", cost)
}

// FormatTokens renders a count with one decimal digit. The suffixes start at
// 1000 ("k") and 1000000 ("M"). Smaller counts stay plain integers.
func FormatTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return formatScaled(n, 1_000_000, "M")
	case n >= 1_000:
		return formatScaled(n, 1_000, "k")
	default:
		return strconv.FormatInt(n, 10)
	}
}

func formatScaled(n, unit int64, suffix string) string {
	scaled := math.Round(float64(n)/float64(unit)*10) / 10
	return strconv.FormatFloat(scaled, 'f', 1, 64) + suffix
}
