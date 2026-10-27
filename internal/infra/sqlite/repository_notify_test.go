package sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestAppendIfDueHonoursMinInterval(t *testing.T) {
	r, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	ctx := context.Background()
	at := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	n := domain.Notification{SessionID: "s1", Kind: domain.NotifyWaitingInput, Label: "PROJ-1", Host: "h", Text: "x", At: at}
	first, ok, err := r.AppendIfDue(ctx, n, 2*time.Minute)
	if err != nil || !ok || first.Seq == 0 {
		t.Fatalf("first: %+v %v %v", first, ok, err)
	}
	n.At = at.Add(time.Minute)
	if _, ok, _ := r.AppendIfDue(ctx, n, 2*time.Minute); ok {
		t.Fatal("within min_interval must be suppressed")
	}
	n.Kind = domain.NotifyError
	if _, ok, _ := r.AppendIfDue(ctx, n, 2*time.Minute); !ok {
		t.Fatal("other kind is independent")
	}
	n.Kind, n.At = domain.NotifyWaitingInput, at.Add(3*time.Minute)
	if _, ok, _ := r.AppendIfDue(ctx, n, 2*time.Minute); !ok {
		t.Fatal("after interval must append")
	}
	got, _ := r.NotificationsSince(ctx, first.Seq, 10)
	if len(got) != 2 {
		t.Fatalf("since: %d rows", len(got))
	}
	last, _ := r.LastNotificationSeq(ctx)
	if last != got[1].Seq {
		t.Fatalf("last seq %d", last)
	}
}

func TestNotifyCursorRoundTrip(t *testing.T) {
	r, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	ctx := context.Background()
	seq, ok, err := r.NotifyCursor(ctx, "serve")
	if err != nil || ok || seq != 0 {
		t.Fatalf("missing: %d %v %v", seq, ok, err)
	}
	if err := r.SetNotifyCursor(ctx, "serve", 7); err != nil {
		t.Fatal(err)
	}
	seq, ok, err = r.NotifyCursor(ctx, "serve")
	if err != nil || !ok || seq != 7 {
		t.Fatalf("set: %d %v %v", seq, ok, err)
	}
}
