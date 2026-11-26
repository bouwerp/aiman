//go:build feedaccept || feedaccept_p3c

package feedcollect_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/bouwerp/aiman/internal/feedcollect"
)

// enrichGH answers notifications in pages of 50 (by the page= query) and
// the PR and PR-files reads used for enrichment. Any other call fails.
type enrichGH struct {
	mu        sync.Mutex
	notes     []map[string]any
	failPulls bool
	calls     []string
}

func (f *enrichGH) run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	joined := strings.Join(args, " ")
	f.calls = append(f.calls, joined)
	header := "HTTP/2.0 200 OK\r\nContent-Type: application/json\r\nEtag: W/\"x\"\r\n\r\n"
	switch {
	case strings.Contains(joined, "notifications"):
		page := 1
		if i := strings.Index(joined, "page="); i >= 0 {
			fmt.Sscanf(joined[i+len("page="):], "%d", &page)
		}
		lo, hi := (page-1)*50, page*50
		if lo > len(f.notes) {
			lo = len(f.notes)
		}
		if hi > len(f.notes) {
			hi = len(f.notes)
		}
		b, _ := json.Marshal(f.notes[lo:hi])
		return []byte(header + string(b)), nil
	case strings.Contains(joined, "repos/acme/widgets/pulls/42/files"):
		if f.failPulls {
			return nil, fmt.Errorf("gh: HTTP 502")
		}
		return []byte(`[{"filename":"evm-settler/config.ts"},{"filename":"README.md"}]`), nil
	case strings.Contains(joined, "repos/acme/widgets/pulls/42"):
		if f.failPulls {
			return nil, fmt.Errorf("gh: HTTP 502")
		}
		return []byte(`{"title":"Settler threshold","body":"Moves the redemption threshold.","user":{"login":"ana"},"html_url":"https://github.com/acme/widgets/pull/42"}`), nil
	}
	return nil, fmt.Errorf("unexpected gh call %q", joined)
}

func enrichNote(id, updated string) map[string]any {
	return map[string]any{
		"id": id, "unread": true, "reason": "review_requested", "updated_at": updated,
		"subject":    map[string]any{"title": "Settler threshold", "url": "https://api.github.com/repos/acme/widgets/pulls/42", "type": "PullRequest"},
		"repository": map[string]any{"full_name": "acme/widgets", "html_url": "https://github.com/acme/widgets"},
	}
}

func TestGitHubSourceEnrichesPullRequests(t *testing.T) {
	gh := &enrichGH{notes: []map[string]any{enrichNote("901", "2026-11-20T09:00:00Z")}}
	evs, _, err := feedcollect.NewGitHubSource(gh.run).Poll(context.Background(), map[string]string{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("%d events, want 1", len(evs))
	}
	ev := evs[0]
	for _, want := range []string{"Moves the redemption threshold.", "evm-settler/config.ts"} {
		if !strings.Contains(ev.Enrichment, want) {
			t.Errorf("enrichment %q lacks %q", ev.Enrichment, want)
		}
	}
	if ev.Actor != "ana" {
		t.Errorf("actor = %q, want the PR author ana", ev.Actor)
	}
}

func TestGitHubSourceEnrichmentFailureKeepsTheEvent(t *testing.T) {
	gh := &enrichGH{notes: []map[string]any{enrichNote("901", "2026-11-20T09:00:00Z")}, failPulls: true}
	evs, _, err := feedcollect.NewGitHubSource(gh.run).Poll(context.Background(), map[string]string{}, nil)
	if err != nil {
		t.Fatalf("a failed enrichment failed the poll: %v", err)
	}
	if len(evs) != 1 || evs[0].Enrichment != "" {
		t.Fatalf("events = %+v, want one event with no enrichment", evs)
	}
}

func TestGitHubSourceReadsEveryPage(t *testing.T) {
	var notes []map[string]any
	for i := 0; i < 53; i++ {
		n := enrichNote(fmt.Sprintf("%d", 1000+i), "2026-11-20T09:00:00Z")
		n["reason"] = "comment"
		n["subject"] = map[string]any{"title": "Issue", "url": fmt.Sprintf("https://api.github.com/repos/acme/widgets/issues/%d", i+1), "type": "Issue"}
		notes = append(notes, n)
	}
	gh := &enrichGH{notes: notes}
	evs, _, err := feedcollect.NewGitHubSource(gh.run).Poll(context.Background(), map[string]string{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 53 {
		t.Fatalf("%d events, want all 53 notifications across two pages", len(evs))
	}
}
