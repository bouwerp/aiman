package usage

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestSummarizeClaudeFixture(t *testing.T) {
	buckets := []domain.UsageBucket{
		{
			SessionID: "s1",
			Hour:      time.Date(2026, 10, 21, 7, 0, 0, 0, time.UTC),
			Model:     "claude-opus-5-5",
			Tokens:    domain.TokenCounts{Input: 4, Output: 325, CacheRead: 97159, CacheWrite5m: 0, CacheWrite1h: 36005},
		},
		{
			SessionID: "s1",
			Hour:      time.Date(2026, 10, 21, 7, 0, 0, 0, time.UTC),
			Model:     "claude-sonnet-5",
			Tokens:    domain.TokenCounts{Input: 3, Output: 50, CacheRead: 2000, CacheWrite5m: 1000},
		},
	}
	sum, unpriced := Summarize(buckets, DefaultPrices())
	near(t, sum.CostUSD, 0.3173938)
	if !sum.Priced || sum.Partial {
		t.Fatalf("Priced=%v Partial=%v, want priced and not partial", sum.Priced, sum.Partial)
	}
	if sum.CacheHitRatio == nil {
		t.Fatal("CacheHitRatio is nil")
	}
	near(t, *sum.CacheHitRatio, 0.7281946963744116)
	wantTokens := domain.TokenCounts{Input: 7, Output: 375, CacheRead: 99159, CacheWrite5m: 1000, CacheWrite1h: 36005}
	if sum.Tokens != wantTokens {
		t.Fatalf("tokens = %+v, want %+v", sum.Tokens, wantTokens)
	}
	if len(unpriced) != 0 {
		t.Fatalf("unpriced = %v", unpriced)
	}
}

func TestReportMarksPartial(t *testing.T) {
	store := newMemStore()
	seedReport(t, store, "s1", "g", "PROJ-1", claudeFixtureBuckets("s1")...)
	seedReport(t, store, "s1", "g", "PROJ-1", domain.UsageBucket{
		SessionID: "s1",
		Hour:      time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC),
		Model:     "gpt-9-unknown",
		Tokens:    domain.TokenCounts{Input: 1_000_000},
	})
	got := report(t, store, Query{SessionID: "s1"})
	if !got.Total.Partial || !got.Total.Priced {
		t.Fatalf("Partial=%v Priced=%v, want both", got.Total.Partial, got.Total.Priced)
	}
	if len(got.Unpriced) != 1 || got.Unpriced[0] != "gpt-9-unknown" {
		t.Fatalf("Unpriced = %v", got.Unpriced)
	}
	near(t, got.Total.CostUSD, 0.3173938)
	if got.Total.Tokens.Input != 1_000_007 {
		t.Fatalf("input = %d, want priced and unpriced tokens", got.Total.Tokens.Input)
	}
}

func TestReportPricesCodex(t *testing.T) {
	terra := domain.TokenCounts{Input: 12696, Output: 433, CacheRead: 29184}
	sol := domain.TokenCounts{Input: 3000, Output: 300, CacheRead: 20000}
	store := newMemStore()
	hour := time.Date(2026, 10, 22, 6, 0, 0, 0, time.UTC)
	seedReport(t, store, "cx", "g", "PROJ-9",
		domain.UsageBucket{SessionID: "cx", Hour: hour, Model: "gpt-5.6-terra", Tokens: terra},
		domain.UsageBucket{SessionID: "cx", Hour: hour.Add(time.Hour), Model: "gpt-5.6-sol", Tokens: sol},
	)
	prices := DefaultPrices()
	want := prices["gpt-5.6-terra"].Cost(terra) + prices["gpt-5.6-sol"].Cost(sol)
	if want <= 0 {
		t.Fatal("DefaultPrices cost of the Codex fixture is not positive")
	}
	got := report(t, store, Query{SessionID: "cx", By: "model"})
	if !got.Total.Priced || got.Total.CostUSD <= 0 || got.Total.Partial {
		t.Fatalf("total = %+v, want priced, not partial, cost > 0", got.Total)
	}
	near(t, got.Total.CostUSD, want)
	if len(got.Rows) != 2 || got.Rows[0].Key != "gpt-5.6-terra" || got.Rows[1].Key != "gpt-5.6-sol" {
		t.Fatalf("rows = %+v, want terra then sol", got.Rows)
	}
	plain := report(t, store, Query{SessionID: "cx"})
	if len(plain.Rows) != 0 {
		t.Fatalf("By empty rows = %+v, want none", plain.Rows)
	}
	near(t, plain.Total.CostUSD, want)
}

func TestReportOnlyUnpriced(t *testing.T) {
	store := newMemStore()
	seedReport(t, store, "s1", "g", "PROJ-1",
		domain.UsageBucket{
			SessionID: "s1",
			Hour:      time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC),
			Model:     "gpt-9-unknown",
			Tokens:    domain.TokenCounts{Output: 10},
		},
		domain.UsageBucket{
			SessionID: "s1",
			Hour:      time.Date(2026, 10, 22, 2, 0, 0, 0, time.UTC),
			Model:     "gpt-9-unknown",
			Tokens:    domain.TokenCounts{Output: 5},
		},
	)
	got := report(t, store, Query{SessionID: "s1", By: "model"})
	if got.Total.Priced || got.Total.CostUSD != 0 || !got.Total.Partial {
		t.Fatalf("total = %+v, want unpriced, zero cost, partial", got.Total)
	}
	if got.Total.CacheHitRatio != nil {
		t.Fatalf("ratio = %v, want nil", *got.Total.CacheHitRatio)
	}
	if len(got.Unpriced) != 1 || got.Unpriced[0] != "gpt-9-unknown" {
		t.Fatalf("Unpriced = %v", got.Unpriced)
	}
	if !got.Known {
		t.Fatal("Known = false, want true")
	}
}

func TestReportByIssueKeepsForgottenSession(t *testing.T) {
	store := newMemStore()
	hour := time.Date(2026, 10, 22, 4, 0, 0, 0, time.UTC)
	// No live session list: meta plus buckets is enough for a forgotten session.
	seedReport(t, store, "s0", "g", "", domain.UsageBucket{
		SessionID: "s0", Hour: hour, Model: "claude-haiku-4-5",
		Tokens: domain.TokenCounts{Input: 3_000_000},
	})
	seedReport(t, store, "s1", "g", "PROJ-1", domain.UsageBucket{
		SessionID: "s1", Hour: hour, Model: "claude-haiku-4-5",
		Tokens: domain.TokenCounts{Input: 1_000_000},
	})
	seedReport(t, store, "s2", "g", "PROJ-2", domain.UsageBucket{
		SessionID: "s2", Hour: hour, Model: "claude-haiku-4-5",
		Tokens: domain.TokenCounts{Input: 1_000_000},
	})
	seedReport(t, store, "s9", "other", "AAA", domain.UsageBucket{
		SessionID: "s9", Hour: hour, Model: "claude-haiku-4-5",
		Tokens: domain.TokenCounts{Input: 9_000_000},
	})
	seedBucket(store, domain.UsageBucket{
		SessionID: "orphan", Hour: hour, Model: "claude-haiku-4-5",
		Tokens: domain.TokenCounts{Input: 8_000_000},
	})

	got := report(t, store, Query{Group: "g", By: "issue"})
	wantKeys := []string{NoTicketKey, "PROJ-1", "PROJ-2"}
	if len(got.Rows) != len(wantKeys) {
		t.Fatalf("rows = %+v, want %v", got.Rows, wantKeys)
	}
	wantCost := []float64{3, 1, 1}
	for i, key := range wantKeys {
		if got.Rows[i].Key != key {
			t.Fatalf("row %d key = %s, want %s (rows %+v)", i, got.Rows[i].Key, key, got.Rows)
		}
		near(t, got.Rows[i].Summary.CostUSD, wantCost[i])
	}
	near(t, got.Total.CostUSD, 5)

	only := report(t, store, Query{Group: "g", Issue: "PROJ-1", By: "issue"})
	if len(only.Rows) != 1 || only.Rows[0].Key != "PROJ-1" {
		t.Fatalf("issue filter rows = %+v", only.Rows)
	}
	near(t, only.Total.CostUSD, 1)

	bySession := report(t, store, Query{Group: "g", By: "session"})
	wantIDs := []string{"s0", "s1", "s2"}
	if len(bySession.Rows) != len(wantIDs) {
		t.Fatalf("session rows = %+v", bySession.Rows)
	}
	for i, id := range wantIDs {
		if bySession.Rows[i].Key != id {
			t.Fatalf("session row %d = %s, want %s", i, bySession.Rows[i].Key, id)
		}
	}
}

func TestReportByDayUsesOffset(t *testing.T) {
	store := newMemStore()
	seedReport(t, store, "s1", "g", "PROJ-1",
		domain.UsageBucket{
			SessionID: "s1",
			Hour:      time.Date(2026, 10, 21, 23, 0, 0, 0, time.UTC),
			Model:     "claude-haiku-4-5",
			Tokens:    domain.TokenCounts{Input: 2_000_000},
		},
		domain.UsageBucket{
			SessionID: "s1",
			Hour:      time.Date(2026, 10, 23, 1, 0, 0, 0, time.UTC),
			Model:     "claude-haiku-4-5",
			Tokens:    domain.TokenCounts{Input: 1_000_000},
		},
	)
	got := report(t, store, Query{SessionID: "s1", By: "day", TZOffsetMinutes: 120})
	want := []string{"2026-10-23", "2026-10-22"}
	if len(got.Rows) != len(want) {
		t.Fatalf("rows = %+v, want days %v", got.Rows, want)
	}
	for i, day := range want {
		if got.Rows[i].Key != day {
			t.Fatalf("row %d = %s, want %s (rows %+v)", i, got.Rows[i].Key, day, got.Rows)
		}
	}
	if got.Rows[1].Summary.Tokens.Input != 2_000_000 {
		t.Fatalf("2026-10-22 tokens = %+v", got.Rows[1].Summary.Tokens)
	}

	one := report(t, store, Query{SessionID: "s1", By: "day", Day: "2026-10-22", TZOffsetMinutes: 120})
	if len(one.Rows) != 1 || one.Rows[0].Key != "2026-10-22" || one.Total.Tokens.Input != 2_000_000 {
		t.Fatalf("day filter = %+v", one)
	}
	if !one.Known {
		t.Fatal("Known = false, want true")
	}
}

func TestReportDaysWindow(t *testing.T) {
	store := newMemStore()
	model := "claude-haiku-4-5"
	seedReport(t, store, "s1", "g", "PROJ-1",
		domain.UsageBucket{SessionID: "s1", Hour: time.Date(2026, 10, 20, 15, 0, 0, 0, time.UTC), Model: model, Tokens: domain.TokenCounts{Input: 100}},
		domain.UsageBucket{SessionID: "s1", Hour: time.Date(2026, 10, 21, 23, 0, 0, 0, time.UTC), Model: model, Tokens: domain.TokenCounts{Input: 10}},
		domain.UsageBucket{SessionID: "s1", Hour: time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC), Model: model, Tokens: domain.TokenCounts{Input: 4}},
		domain.UsageBucket{SessionID: "s1", Hour: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC), Model: model, Tokens: domain.TokenCounts{Input: 1}},
	)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	rep := Reporter{Store: store, Prices: DefaultPrices(), Now: func() time.Time { return now }}
	got, err := rep.Report(context.Background(), Query{SessionID: "s1", Days: 1, By: "day"})
	if err != nil {
		t.Fatal(err)
	}
	// Last 1 day of the zone is the local calendar day that contains Now.
	if got.Total.Tokens.Input != 4 {
		t.Fatalf("input = %d, want 4 (2026-10-20 is outside the day)", got.Total.Tokens.Input)
	}
	if len(got.Rows) != 1 || got.Rows[0].Key != "2026-10-22" {
		t.Fatalf("rows = %+v", got.Rows)
	}
	all, err := rep.Report(context.Background(), Query{SessionID: "s1", Days: 0})
	if err != nil {
		t.Fatal(err)
	}
	if all.Total.Tokens.Input != 115 {
		t.Fatalf("all-time input = %d, want 115", all.Total.Tokens.Input)
	}
}

func TestReportUnknownSession(t *testing.T) {
	store := newMemStore()
	store.seedCursor(domain.UsageCursor{SessionID: "watched", Path: "/tmp/watched.jsonl"})
	if err := store.UpsertUsageSession(context.Background(), domain.UsageSessionMeta{SessionID: "watched", IssueKey: "PROJ-8"}); err != nil {
		t.Fatal(err)
	}
	seedReport(t, store, "other", "g", "PROJ-9", domain.UsageBucket{
		SessionID: "other",
		Hour:      time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC),
		Model:     "claude-haiku-4-5",
		Tokens:    domain.TokenCounts{Input: 1_000_000},
	})
	got := report(t, store, Query{SessionID: "missing"})
	if got.Known {
		t.Fatal("Known = true, want false")
	}
	if got.Total.CostUSD != 0 || got.Total.Tokens != (domain.TokenCounts{}) || len(got.Rows) != 0 || got.Total.Priced || got.Total.Partial {
		t.Fatalf("unknown report = %+v", got)
	}
	watched := report(t, store, Query{SessionID: "watched"})
	if !watched.Known {
		t.Fatal("cursor without buckets: Known = false, want true")
	}
	if watched.Total.CostUSD != 0 || watched.Total.Priced || len(watched.Rows) != 0 {
		t.Fatalf("cursor-only report = %+v", watched)
	}
}

func claudeFixtureBuckets(sessionID string) []domain.UsageBucket {
	hour := time.Date(2026, 10, 21, 7, 0, 0, 0, time.UTC)
	return []domain.UsageBucket{
		{
			SessionID: sessionID,
			Hour:      hour,
			Model:     "claude-opus-5-5",
			Tokens:    domain.TokenCounts{Input: 4, Output: 325, CacheRead: 97159, CacheWrite5m: 0, CacheWrite1h: 36005},
		},
		{
			SessionID: sessionID,
			Hour:      hour,
			Model:     "claude-sonnet-5",
			Tokens:    domain.TokenCounts{Input: 3, Output: 50, CacheRead: 2000, CacheWrite5m: 1000},
		},
	}
}

func seedReport(t *testing.T, store *memStore, sessionID, group, issue string, buckets ...domain.UsageBucket) {
	t.Helper()
	if err := store.UpsertUsageSession(context.Background(), domain.UsageSessionMeta{
		SessionID: sessionID,
		Group:     group,
		IssueKey:  issue,
	}); err != nil {
		t.Fatal(err)
	}
	for _, bucket := range buckets {
		seedBucket(store, bucket)
	}
}

func seedBucket(store *memStore, bucket domain.UsageBucket) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.addBucket(bucket.SessionID, domain.UsageRecord{
		Model:  bucket.Model,
		At:     bucket.Hour,
		Tokens: bucket.Tokens,
	})
}

func report(t *testing.T, store *memStore, q Query) Report {
	t.Helper()
	got, err := (Reporter{Store: store, Prices: DefaultPrices()}).Report(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func near(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("got %.10f, want %.10f", got, want)
	}
}
