package sqlite

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func newCIRepo(t *testing.T) *Repository {
	t.Helper()
	r, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestWatchPRSurvivesBlankSave(t *testing.T) {
	r := newCIRepo(t)
	ctx := context.Background()
	s := &domain.Session{ID: "s1", WatchPR: domain.WatchPROff, Status: domain.SessionStatusActive, CreatedAt: time.Now()}
	if err := r.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	s.WatchPR = ""
	if err := r.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	got, err := r.Get(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.WatchPR != domain.WatchPROff {
		t.Fatalf("watch_pr=%q", got.WatchPR)
	}
}

func TestRecordCheckFailureIsIdempotentPerSHA(t *testing.T) {
	r := newCIRepo(t)
	ctx := context.Background()
	steps := []struct {
		check, sha string
		want       int
	}{
		{"lint", "a", 1},
		{"lint", "a", 1},
		{"lint", "b", 2},
		{"test", "a", 1},
	}
	for _, st := range steps {
		n, err := r.RecordCheckFailure(ctx, "s1", st.check, st.sha)
		if err != nil {
			t.Fatal(err)
		}
		if n != st.want {
			t.Fatalf("%s@%s: got %d, want %d", st.check, st.sha, n, st.want)
		}
	}
}

func TestMarkCheckEscalatedOnce(t *testing.T) {
	r := newCIRepo(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	if _, err := r.RecordCheckFailure(ctx, "s1", "lint", "a"); err != nil {
		t.Fatal(err)
	}
	first, err := r.MarkCheckEscalated(ctx, "s1", "lint", at)
	if err != nil || !first {
		t.Fatalf("first: %v %v", first, err)
	}
	second, err := r.MarkCheckEscalated(ctx, "s1", "lint", at)
	if err != nil || second {
		t.Fatalf("second: %v %v", second, err)
	}
	st, err := r.LoadPRWatch(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st.Escalated, []string{"lint"}) {
		t.Fatalf("escalated=%v", st.Escalated)
	}
	if err := r.ClearCheck(ctx, "s1", "lint"); err != nil {
		t.Fatal(err)
	}
	st, err = r.LoadPRWatch(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Escalated) != 0 {
		t.Fatalf("escalated after clear=%v", st.Escalated)
	}
	if n, err := r.RecordCheckFailure(ctx, "s1", "lint", "c"); err != nil || n != 1 {
		t.Fatalf("after clear: %d %v", n, err)
	}
}

func TestSeenCommentsRoundTrip(t *testing.T) {
	r := newCIRepo(t)
	ctx := context.Background()
	if err := r.MarkCommentsSeen(ctx, "s1", []int64{1, 2}); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkCommentsSeen(ctx, "s1", []int64{2, 3}); err != nil {
		t.Fatal(err)
	}
	seen, err := r.SeenComments(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(seen, map[int64]bool{1: true, 2: true, 3: true}) {
		t.Fatalf("seen=%v", seen)
	}
}

func TestResetPRWatchClearsAll(t *testing.T) {
	r := newCIRepo(t)
	ctx := context.Background()
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	if err := r.SavePRWatch(ctx, domain.PRWatchState{SessionID: "s1", PRNumber: 7, PRState: "OPEN", HeadSHA: "a", WatchSince: at}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RecordCheckFailure(ctx, "s1", "lint", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.MarkCheckEscalated(ctx, "s1", "lint", at); err != nil {
		t.Fatal(err)
	}
	if err := r.MarkCommentsSeen(ctx, "s1", []int64{9}); err != nil {
		t.Fatal(err)
	}
	if err := r.ResetPRWatch(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	st, err := r.LoadPRWatch(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(st, domain.PRWatchState{SessionID: "s1"}) {
		t.Fatalf("state after reset=%+v", st)
	}
	seen, err := r.SeenComments(ctx, "s1")
	if err != nil || len(seen) != 0 {
		t.Fatalf("seen after reset=%v %v", seen, err)
	}
	if n, err := r.RecordCheckFailure(ctx, "s1", "lint", "a"); err != nil || n != 1 {
		t.Fatalf("failures after reset: %d %v", n, err)
	}
}

func TestPRWatchStateRoundTrip(t *testing.T) {
	r := newCIRepo(t)
	ctx := context.Background()
	since := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	polled := since.Add(2 * time.Minute)
	want := domain.PRWatchState{SessionID: "s1", PRNumber: 7, PRState: "OPEN", HeadSHA: "abc", WatchSince: since, LastPolledAt: polled, LastError: "x"}
	if err := r.SavePRWatch(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := r.LoadPRWatch(ctx, "s1")
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != want.SessionID || got.PRNumber != want.PRNumber || got.PRState != want.PRState ||
		got.HeadSHA != want.HeadSHA || got.LastError != want.LastError ||
		!got.WatchSince.Equal(want.WatchSince) || !got.LastPolledAt.Equal(want.LastPolledAt) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
