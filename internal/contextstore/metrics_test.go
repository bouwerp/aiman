package contextstore

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestFilesStatsTracksSizeAndOps(t *testing.T) {
	ctx := context.Background()
	s := NewFiles(t.TempDir())
	put, err := s.Put(ctx, domain.ContextEntry{
		Namespace: domain.ContextNSGroup, Key: "G1", Title: "t", Abstract: "a", Body: "hello body",
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Notes != 1 || st.Bytes == 0 {
		t.Fatalf("size %+v", st)
	}
	if st.Namespaces["group"] != 1 {
		t.Fatalf("ns %+v", st.Namespaces)
	}
	if st.Ops["put"].Count != 1 {
		t.Fatalf("put %+v", st.Ops)
	}
	got, err := s.Get(ctx, put.ID)
	if err != nil || got == nil {
		t.Fatal(err)
	}
	st, _ = s.Stats(ctx)
	if st.Ops["get"].Count != 1 || st.Ops["get"].LastMs < 0 {
		t.Fatalf("get %+v", st.Ops["get"])
	}
	if _, err := s.Find(ctx, domain.ContextQuery{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pack(ctx, domain.ContextQuery{Namespace: domain.ContextNSGroup, Key: "G1"}); err != nil {
		t.Fatal(err)
	}
	st, _ = s.Stats(ctx)
	if st.Ops["find"].Count != 1 || st.Ops["pack"].Count != 1 {
		t.Fatalf("ops %+v", st.Ops)
	}
	if st.Ops["list"].Count != 0 {
		t.Fatalf("pack must not count as list: %+v", st.Ops)
	}
}

func TestPackSessionCountsAsPack(t *testing.T) {
	ctx := context.Background()
	s := NewFiles(t.TempDir())
	if _, err := s.Put(ctx, domain.ContextEntry{
		Namespace: domain.ContextNSGroup, Key: "G1", Title: "t", Abstract: "a",
	}); err != nil {
		t.Fatal(err)
	}
	_ = PackForSession(ctx, s, "G1", "")
	st, _ := s.Stats(ctx)
	if st.Ops["pack"].Count != 1 {
		t.Fatalf("%+v", st.Ops)
	}
	if st.Ops["list"].Count != 0 {
		t.Fatalf("session pack must not count list: %+v", st.Ops)
	}
}

func TestPercentileMs(t *testing.T) {
	if percentileMs(nil, 0.5) != 0 {
		t.Fatal("empty")
	}
	got := percentileMs([]int64{1e6, 2e6, 3e6, 4e6}, 0.50)
	if got < 1.9 || got > 3.1 {
		t.Fatalf("p50 %v", got)
	}
}

func TestParseStatsJSON(t *testing.T) {
	st := domain.ContextStats{Notes: 3, Bytes: 12, Ops: map[string]domain.ContextOpStat{"get": {Count: 4}}}
	raw, _ := json.Marshal(map[string]any{"type": "context_stats", "stats": st})
	got, err := ParseStatsJSON(raw)
	if err != nil || got.Notes != 3 || got.Ops["get"].Count != 4 {
		t.Fatalf("%+v %v", got, err)
	}
}
