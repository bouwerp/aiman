package sqlite

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestEventsSinceListAfterRowID(t *testing.T) {
	repo := eventRepo(t)
	ctx := context.Background()
	appends := []struct {
		session string
		kind    domain.EventKind
	}{
		{"s1", domain.EventSessionCreated},
		{"s2", domain.EventChildLanded},
		{"s1", domain.EventHookReported},
		{"s3", domain.EventChildFinished},
		{"s2", domain.EventChildLanded},
	}
	for _, a := range appends {
		if _, err := repo.AppendSessionEvent(ctx, domain.SessionEvent{
			SessionID: a.session, Actor: domain.EventActorAiman, Kind: a.kind,
			Payload: json.RawMessage(`{"n":1}`),
		}); err != nil {
			t.Fatal(err)
		}
	}

	all, err := repo.ListEventsAfterRowID(ctx, 0, nil, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(appends) {
		t.Fatalf("all rows = %d, want %d", len(all), len(appends))
	}
	for i, row := range all {
		if row.SessionID != appends[i].session || row.Kind != appends[i].kind {
			t.Errorf("row %d = %s %s, want %s %s (insert order across sessions)", i, row.SessionID, row.Kind, appends[i].session, appends[i].kind)
		}
		if i > 0 && row.RowID <= all[i-1].RowID {
			t.Errorf("row %d rowid %d not above %d", i, row.RowID, all[i-1].RowID)
		}
		if row.Seq == 0 || row.At.IsZero() || string(row.Payload) != `{"n":1}` {
			t.Errorf("row %d = %+v", i, row)
		}
	}

	for _, tc := range []struct {
		name  string
		after int64
		kinds []domain.EventKind
		limit int
		want  []int // indexes into all
	}{
		{"after a rowid", all[1].RowID, nil, 100, []int{2, 3, 4}},
		{"limit", 0, nil, 2, []int{0, 1}},
		{"one kind", 0, []domain.EventKind{domain.EventChildLanded}, 100, []int{1, 4}},
		{"two kinds after", all[1].RowID, []domain.EventKind{domain.EventChildLanded, domain.EventChildFinished}, 100, []int{3, 4}},
		{"kind and limit", 0, []domain.EventKind{domain.EventChildLanded, domain.EventChildFinished}, 1, []int{1}},
		{"past the end", all[4].RowID, nil, 100, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.ListEventsAfterRowID(ctx, tc.after, tc.kinds, tc.limit)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("rows = %+v, want indexes %v", got, tc.want)
			}
			for i, idx := range tc.want {
				if got[i].RowID != all[idx].RowID {
					t.Errorf("row %d rowid = %d, want %d", i, got[i].RowID, all[idx].RowID)
				}
			}
		})
	}
}
