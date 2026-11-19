package usage

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

// TestSeenModelsNewestFirst lists each model once, newest use first, without
// the fast marker, synthetic rows or other prefixes, and only inside since.
func TestSeenModelsNewestFirst(t *testing.T) {
	store := newMemStore()
	h := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	store.bucketRows = []domain.UsageBucket{
		{SessionID: "a", Hour: h.Add(-2 * time.Hour), Model: "claude-fable-5-1"},
		{SessionID: "a", Hour: h, Model: "claude-fable-5-2"},
		{SessionID: "b", Hour: h.Add(-time.Hour), Model: "claude-opus-5-5@fast"},
		{SessionID: "b", Hour: h.Add(-time.Hour), Model: "<synthetic>"},
		{SessionID: "c", Hour: h, Model: "gpt-6.2-sol"},
		{SessionID: "d", Hour: h.Add(-40 * 24 * time.Hour), Model: "claude-sonnet-4-5"},
	}
	got, err := Reporter{Store: store}.SeenModels(context.Background(), h.Add(-30*24*time.Hour), "claude-")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"claude-fable-5-2", "claude-opus-5-5", "claude-fable-5-1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("seen = %v, want %v", got, want)
	}
}

// TestReportCarriesLatestModel puts the latest turn's model on a
// one-session report, so a client can show what an alias resolved to.
func TestReportCarriesLatestModel(t *testing.T) {
	store := newMemStore()
	if err := store.UpsertUsageSession(context.Background(), domain.UsageSessionMeta{
		SessionID: "s1", LastInputTokens: 10, ContextWindow: 200_000, LastModel: "claude-fable-5-2", HasContext: true,
	}); err != nil {
		t.Fatal(err)
	}
	if got := report(t, store, Query{SessionID: "s1"}).Model; got != "claude-fable-5-2" {
		t.Fatalf("model = %q", got)
	}
}
