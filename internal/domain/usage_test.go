package domain

import (
	"math"
	"testing"
	"time"
)

func TestCacheHitRatio(t *testing.T) {
	got, ok := (TokenCounts{Input: 4, CacheRead: 97159, CacheWrite1h: 36005}).CacheHitRatio()
	if !ok {
		t.Fatal("CacheHitRatio() ok = false, want true")
	}
	const want = 0.7295972005286555
	if math.Abs(got-want) > 1e-15 {
		t.Fatalf("CacheHitRatio() = %v, want %v", got, want)
	}
	if _, ok := (TokenCounts{}).CacheHitRatio(); ok {
		t.Fatal("zero TokenCounts CacheHitRatio() ok = true, want false")
	}
}

func TestTokenCountsAdd(t *testing.T) {
	a := TokenCounts{Input: 1, Output: 2, CacheRead: 3, CacheWrite5m: 4, CacheWrite1h: 5}
	b := TokenCounts{Input: 10, Output: 20, CacheRead: 30, CacheWrite5m: 40, CacheWrite1h: 50}
	got := a.Add(b)
	want := TokenCounts{Input: 11, Output: 22, CacheRead: 33, CacheWrite5m: 44, CacheWrite1h: 55}
	if got != want {
		t.Fatalf("Add() = %+v, want %+v", got, want)
	}
	if got.CacheCreation() != 99 {
		t.Fatalf("CacheCreation() = %d, want 99", got.CacheCreation())
	}
	if !(TokenCounts{}).IsZero() {
		t.Fatal("zero TokenCounts IsZero() = false, want true")
	}
	if got.IsZero() {
		t.Fatal("sum IsZero() = true, want false")
	}
	if (TokenCounts{Output: 1}).IsZero() {
		t.Fatal("output-only IsZero() = true, want false")
	}
}

func TestUsageShapes(t *testing.T) {
	at := time.Unix(0, 0).UTC()
	rec := UsageRecord{ID: "", Model: "m", At: at, Tokens: TokenCounts{Input: 1}}
	if rec.ID != "" || rec.Model != "m" || !rec.At.Equal(at) || rec.Tokens.Input != 1 {
		t.Fatalf("UsageRecord = %+v", rec)
	}
	bucket := UsageBucket{SessionID: "s", Hour: at, Model: "m", Tokens: rec.Tokens}
	if bucket.SessionID != "s" || !bucket.Hour.Equal(at) || bucket.Model != "m" || bucket.Tokens != rec.Tokens {
		t.Fatalf("UsageBucket = %+v", bucket)
	}
	cur := UsageCursor{
		SessionID: "s", Path: "p", Fingerprint: "f",
		Offset: 1, Size: 2, ModTime: at, State: "",
	}
	if cur.Path != "p" || cur.Fingerprint != "f" || cur.Offset != 1 || cur.Size != 2 || cur.State != "" || !cur.ModTime.Equal(at) {
		t.Fatalf("UsageCursor = %+v", cur)
	}
	meta := UsageSessionMeta{SessionID: "s", Name: "n", Group: "g", IssueKey: "i", AgentName: "a"}
	if meta.Name != "n" || meta.Group != "g" || meta.IssueKey != "i" || meta.AgentName != "a" {
		t.Fatalf("UsageSessionMeta = %+v", meta)
	}
}
