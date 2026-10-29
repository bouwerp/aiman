package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func openUsageRepo(t *testing.T) *Repository {
	t.Helper()
	repo, err := NewRepository(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	return repo
}

func TestApplyUsageDedupesIDs(t *testing.T) {
	repo := openUsageRepo(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 22, 6, 30, 0, 0, time.UTC)
	a := domain.UsageRecord{
		ID: "A", Model: "claude", At: at,
		Tokens: domain.TokenCounts{Input: 10, Output: 1, CacheRead: 2, CacheWrite5m: 3, CacheWrite1h: 4},
	}
	b := domain.UsageRecord{
		ID: "B", Model: "claude", At: at,
		Tokens: domain.TokenCounts{Input: 5, Output: 6, CacheRead: 7, CacheWrite5m: 8, CacheWrite1h: 9},
	}
	cur := domain.UsageCursor{
		SessionID: "s1", Path: "/t.jsonl", Fingerprint: "fp1",
		Offset: 10, Size: 100, ModTime: at, State: `{"k":1}`,
	}
	added, err := repo.ApplyUsage(ctx, cur, []domain.UsageRecord{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("first apply added nothing")
	}

	cur.Offset = 50
	cur.Fingerprint = "fp2"
	cur.Size = 200
	cur.ModTime = at.Add(time.Minute)
	cur.State = `{"k":2}`
	a.Tokens.Input = 999
	added, err = repo.ApplyUsage(ctx, cur, []domain.UsageRecord{a})
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Fatal("duplicate id was applied again")
	}

	buckets, err := repo.UsageBuckets(ctx, []string{"s1"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 {
		t.Fatalf("buckets = %+v, want one", buckets)
	}
	want := domain.TokenCounts{Input: 15, Output: 7, CacheRead: 9, CacheWrite5m: 11, CacheWrite1h: 13}
	if buckets[0].Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", buckets[0].Tokens, want)
	}
	if !buckets[0].Hour.Equal(at.Truncate(time.Hour)) {
		t.Fatalf("hour = %s", buckets[0].Hour)
	}

	cursors, err := repo.UsageCursors(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(cursors) != 1 {
		t.Fatalf("cursors = %+v", cursors)
	}
	got := cursors[0]
	if got.Offset != 50 || got.Fingerprint != "fp2" || got.Size != 200 || got.State != `{"k":2}` {
		t.Fatalf("cursor = %+v", got)
	}
	if !got.ModTime.Equal(cur.ModTime) {
		t.Fatalf("mod_time = %s, want %s", got.ModTime, cur.ModTime)
	}
	if got.Path != cur.Path || got.SessionID != "s1" {
		t.Fatalf("cursor identity = %+v", got)
	}
}

func TestApplyUsageAddsUnnamedRecords(t *testing.T) {
	repo := openUsageRepo(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 22, 8, 15, 0, 0, time.UTC)
	tokens := domain.TokenCounts{Input: 4, Output: 5, CacheRead: 6, CacheWrite5m: 7, CacheWrite1h: 8}
	recs := []domain.UsageRecord{
		{Model: "m", At: at, Tokens: tokens},
		{Model: "m", At: at, Tokens: tokens},
	}
	added, err := repo.ApplyUsage(ctx, domain.UsageCursor{SessionID: "s1", Path: "/u.jsonl"}, recs)
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("unnamed records were not added")
	}
	buckets, err := repo.UsageBuckets(ctx, nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 {
		t.Fatalf("buckets = %+v", buckets)
	}
	want := domain.TokenCounts{Input: 8, Output: 10, CacheRead: 12, CacheWrite5m: 14, CacheWrite1h: 16}
	if buckets[0].Tokens != want {
		t.Fatalf("tokens = %+v, want %+v", buckets[0].Tokens, want)
	}
}

func TestApplyUsageBucketsByHourAndModel(t *testing.T) {
	repo := openUsageRepo(t)
	ctx := context.Background()
	day := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	early := day.Add(6*time.Hour + 59*time.Minute)
	late := day.Add(7*time.Hour + time.Minute)
	recs := []domain.UsageRecord{
		{ID: "e", Model: "opus", At: early, Tokens: domain.TokenCounts{Input: 1}},
		{ID: "l", Model: "opus", At: late, Tokens: domain.TokenCounts{Input: 2}},
	}
	if _, err := repo.ApplyUsage(ctx, domain.UsageCursor{SessionID: "s1", Path: "/h.jsonl"}, recs); err != nil {
		t.Fatal(err)
	}
	buckets, err := repo.UsageBuckets(ctx, nil, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("buckets = %+v, want two", buckets)
	}
	if buckets[0].Model != "opus" || buckets[1].Model != "opus" {
		t.Fatalf("models = %q %q", buckets[0].Model, buckets[1].Model)
	}
	if !buckets[0].Hour.Equal(early.UTC().Truncate(time.Hour)) || buckets[0].Tokens.Input != 1 {
		t.Fatalf("first = %+v", buckets[0])
	}
	if !buckets[1].Hour.Equal(late.UTC().Truncate(time.Hour)) || buckets[1].Tokens.Input != 2 {
		t.Fatalf("second = %+v", buckets[1])
	}
	if !buckets[0].Hour.Before(buckets[1].Hour) {
		t.Fatalf("hours not ordered: %s then %s", buckets[0].Hour, buckets[1].Hour)
	}
}

func TestUsageBucketsSince(t *testing.T) {
	repo := openUsageRepo(t)
	ctx := context.Background()
	day := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	early := day.Add(6*time.Hour + 59*time.Minute)
	late := day.Add(7*time.Hour + time.Minute)
	recs := []domain.UsageRecord{
		{ID: "e", Model: "opus", At: early, Tokens: domain.TokenCounts{Input: 1}},
		{ID: "l", Model: "opus", At: late, Tokens: domain.TokenCounts{Output: 2}},
	}
	cur := domain.UsageCursor{SessionID: "s1", Path: "/h.jsonl"}
	if _, err := repo.ApplyUsage(ctx, cur, recs); err != nil {
		t.Fatal(err)
	}
	other := domain.UsageCursor{SessionID: "s2", Path: "/o.jsonl"}
	if _, err := repo.ApplyUsage(ctx, other, []domain.UsageRecord{
		{ID: "o", Model: "opus", At: late, Tokens: domain.TokenCounts{Input: 9}},
	}); err != nil {
		t.Fatal(err)
	}

	since := late.UTC().Truncate(time.Hour)
	buckets, err := repo.UsageBuckets(ctx, nil, since)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("since hour start: %+v", buckets)
	}
	for _, b := range buckets {
		if b.Hour.Before(since) {
			t.Fatalf("included hour %s before %s", b.Hour, since)
		}
	}

	mid := since.Add(30 * time.Minute)
	buckets, err = repo.UsageBuckets(ctx, []string{"s1"}, mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 0 {
		t.Fatalf("hour start is before mid-hour since: %+v", buckets)
	}

	none, err := repo.UsageBuckets(ctx, []string{}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("empty id slice returned %+v", none)
	}

	only, err := repo.UsageBuckets(ctx, []string{"s2"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(only) != 1 || only[0].SessionID != "s2" || only[0].Tokens.Input != 9 {
		t.Fatalf("session filter = %+v", only)
	}
}

func TestUsageSessionsSurviveSessionDelete(t *testing.T) {
	repo := openUsageRepo(t)
	ctx := context.Background()
	if err := repo.UpsertUsageSession(ctx, domain.UsageSessionMeta{
		SessionID: "s1", Name: "impl", Group: "alpha", IssueKey: "PROJ-1", AgentName: "claude",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Save(ctx, &domain.Session{ID: "s1", IssueKey: "PROJ-1"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, "s1"); err == nil {
		t.Fatal("deleted session still loads")
	}
	got, err := repo.UsageSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].IssueKey != "PROJ-1" || got[0].SessionID != "s1" {
		t.Fatalf("usage sessions = %+v", got)
	}
}

func TestUsageContextRoundTrip(t *testing.T) {
	repo := openUsageRepo(t)
	ctx := context.Background()
	if _, err := repo.db.ExecContext(ctx, `DROP TABLE usage_sessions`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.ExecContext(ctx, `
		CREATE TABLE usage_sessions (
			session_id TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			group_name TEXT NOT NULL DEFAULT '',
			issue_key TEXT NOT NULL DEFAULT '',
			agent_name TEXT NOT NULL DEFAULT '',
			updated_at DATETIME
		)`); err != nil {
		t.Fatal(err)
	}
	if err := ensureUsageTables(repo.db); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertUsageSession(ctx, domain.UsageSessionMeta{
		SessionID: "s1", Name: "impl", Group: "alpha", IssueKey: "PROJ-1", AgentName: "claude",
		LastInputTokens: 27, ContextWindow: 200_000, ContextSource: "fallback", HasContext: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertUsageSession(ctx, domain.UsageSessionMeta{SessionID: "s1", Name: "rename"}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.UsageSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("sessions = %+v", got)
	}
	m := got[0]
	if m.Name != "rename" || m.LastInputTokens != 27 || m.ContextWindow != 200_000 || m.ContextSource != "fallback" || m.HasContext {
		t.Fatalf("label refresh wiped occupancy: %+v", m)
	}
	if err := repo.UpsertUsageSession(ctx, domain.UsageSessionMeta{
		SessionID: "s1", LastInputTokens: 9, ContextWindow: 50_000, ContextSource: "explicit", HasContext: true,
	}); err != nil {
		t.Fatal(err)
	}
	got, err = repo.UsageSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m = got[0]
	if m.Name != "rename" || m.Group != "alpha" || m.LastInputTokens != 9 || m.ContextWindow != 50_000 || m.ContextSource != "explicit" {
		t.Fatalf("smaller turn did not replace occupancy: %+v", m)
	}
}

func TestUpsertUsageSessionKeepsFields(t *testing.T) {
	repo := openUsageRepo(t)
	ctx := context.Background()
	if err := repo.UpsertUsageSession(ctx, domain.UsageSessionMeta{
		SessionID: "s1", Name: "impl", Group: "alpha", IssueKey: "PROJ-1", AgentName: "claude",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.ExecContext(ctx, `UPDATE usage_sessions SET updated_at = ? WHERE session_id = ?`, "2000-01-01T00:00:00Z", "s1"); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertUsageSession(ctx, domain.UsageSessionMeta{
		SessionID: "s1", Name: "rename",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.UsageSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("sessions = %+v", got)
	}
	m := got[0]
	if m.Name != "rename" || m.Group != "alpha" || m.IssueKey != "PROJ-1" || m.AgentName != "claude" {
		t.Fatalf("meta = %+v", m)
	}
	var updated string
	if err := repo.db.QueryRowContext(ctx, `SELECT updated_at FROM usage_sessions WHERE session_id = ?`, "s1").Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if updated == "" || updated == "2000-01-01T00:00:00Z" {
		t.Fatalf("updated_at = %q", updated)
	}
}

func TestUsageSessionRoleRoundTrip(t *testing.T) {
	repo := openUsageRepo(t)
	ctx := context.Background()
	if err := repo.UpsertUsageSession(ctx, domain.UsageSessionMeta{
		SessionID: "s1", Role: "consult", ParentID: "p1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertUsageSession(ctx, domain.UsageSessionMeta{SessionID: "s1", Name: "n"}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.UsageSessions(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("sessions = %+v, err %v", got, err)
	}
	if got[0].Role != "consult" || got[0].ParentID != "p1" {
		t.Fatalf("meta = %+v", got[0])
	}
}
