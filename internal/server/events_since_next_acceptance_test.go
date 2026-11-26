package server

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

// With a kind filter, next is the last row scanned, not the last row that
// matched, so the collector's cursor does not scan the same rows again.
func TestEventsSinceNextIsLastScannedRow(t *testing.T) {
	srv := eventsSinceServer(t)
	appendLedger(t, srv, "s1", domain.EventChildLanded)
	appendLedger(t, srv, "s1", domain.EventSessionCreated)
	appendLedger(t, srv, "s2", domain.EventSessionCreated)

	all := eventsSince(t, srv, map[string]any{"after_rowid": 0})
	if len(all.Events) != 3 {
		t.Fatalf("events = %+v, want 3", all.Events)
	}
	got := eventsSince(t, srv, map[string]any{"after_rowid": 0, "kinds": []string{string(domain.EventChildLanded)}})
	if len(got.Events) != 1 || got.Events[0].Kind != domain.EventChildLanded {
		t.Fatalf("filtered = %+v", got.Events)
	}
	if got.Next != all.Events[2].RowID {
		t.Fatalf("next = %d, want the last scanned rowid %d", got.Next, all.Events[2].RowID)
	}
}
