package server

import (
	"context"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

type feedBatchesResult struct {
	Type    string `json:"type"`
	Batches []struct {
		domain.FeedBatch
		Actions int `json:"actions"`
	} `json:"batches"`
	NextBefore int64 `json:"next_before"`
}

// feed.batches lists batches newest first, pages with before, and counts the
// actions each batch produced.
func TestFeedBatchesListsNewestFirstWithActionCounts(t *testing.T) {
	r := newFeedRig(t)
	ctx := context.Background()
	var ids []int64
	for i := int64(1); i <= 3; i++ {
		b, err := r.repo.CutFeedBatch(ctx, domain.FeedBatch{EventIDs: []int64{i}, UpToEventID: i, State: "done", Attempts: 1, StartedAt: time.Now(), Summary: "s"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, b.ID)
	}
	for range 2 {
		if _, err := r.repo.InsertFeedAction(ctx, domain.FeedAction{BatchID: ids[1], Kind: domain.FeedRead, Priority: "normal", Answers: []int64{2},
			Options: []domain.FeedOption{{Label: "o", Params: []byte(`{}`)}}, State: "proposed", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
	}

	var all feedBatchesResult
	rpcOK(t, r.srv, "", "feed.batches", map[string]any{}, &all)
	if all.Type != "feed_batches" || len(all.Batches) != 3 {
		t.Fatalf("feed.batches = %+v", all)
	}
	if all.Batches[0].ID != ids[2] || all.Batches[2].ID != ids[0] {
		t.Fatalf("order = %d, %d, %d; want newest first", all.Batches[0].ID, all.Batches[1].ID, all.Batches[2].ID)
	}
	if all.Batches[1].Actions != 2 || all.Batches[0].Actions != 0 {
		t.Errorf("action counts = %d, %d; want 0 then 2", all.Batches[0].Actions, all.Batches[1].Actions)
	}
	if all.Batches[0].Summary != "s" || len(all.Batches[0].EventIDs) != 1 {
		t.Errorf("batch fields lost: %+v", all.Batches[0])
	}

	var page feedBatchesResult
	rpcOK(t, r.srv, "", "feed.batches", map[string]any{"limit": 2}, &page)
	if len(page.Batches) != 2 || page.NextBefore != ids[1] {
		t.Fatalf("first page = %d batches, next_before %d; want 2 and %d", len(page.Batches), page.NextBefore, ids[1])
	}
	rpcOK(t, r.srv, "", "feed.batches", map[string]any{"limit": 2, "before": page.NextBefore}, &page)
	if len(page.Batches) != 1 || page.Batches[0].ID != ids[0] || page.NextBefore != 0 {
		t.Fatalf("second page = %+v; want only the oldest batch and next_before 0", page)
	}
}

func TestFeedBatchesClampsLimit(t *testing.T) {
	r := newFeedRig(t)
	if resp := rpc(t, r.srv, "", "feed.batches", map[string]any{"limit": 5000}); resp.Error != nil {
		t.Fatalf("a limit over the maximum is clamped, not refused: %+v", resp.Error)
	}
}
