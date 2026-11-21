package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
)

func sharesRepo(t *testing.T) *Repository {
	t.Helper()
	r, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestHarnessSharesRoundTrip(t *testing.T) {
	r := sharesRepo(t)
	ctx := context.Background()
	created := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	capAt := created.Add(12 * time.Hour)
	in := domain.HarnessShare{ID: "share-a", Session: "s1", Run: "r1", CreatedAt: created, CapAt: &capAt}
	if err := r.SaveHarnessShare(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := r.GetHarnessShare(ctx, "share-a")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != in.ID || got.Session != "s1" || got.Run != "r1" || !got.CreatedAt.Equal(created) {
		t.Fatalf("share = %+v", got)
	}
	if got.CapAt == nil || !got.CapAt.Equal(capAt) {
		t.Fatalf("cap_at = %v", got.CapAt)
	}
	if got.RevokedAt != nil {
		t.Fatalf("fresh share revoked at %v", got.RevokedAt)
	}

	// No cap round-trips as nil.
	if err := r.SaveHarnessShare(ctx, domain.HarnessShare{ID: "share-b", Session: "s2", Run: "r2", CreatedAt: created}); err != nil {
		t.Fatal(err)
	}
	if got, err := r.GetHarnessShare(ctx, "share-b"); err != nil || got.CapAt != nil {
		t.Fatalf("uncapped share = %+v, err %v", got, err)
	}
}

func TestHarnessSharesMissingIsNotFound(t *testing.T) {
	r := sharesRepo(t)
	_, err := r.GetHarnessShare(context.Background(), "nope")
	if !errors.Is(err, ErrShareNotFound) {
		t.Fatalf("err = %v, want ErrShareNotFound", err)
	}
}

func TestHarnessSharesRevokeBySession(t *testing.T) {
	r := sharesRepo(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	for _, s := range []domain.HarnessShare{
		{ID: "a1", Session: "s1", Run: "r1", CreatedAt: now},
		{ID: "a2", Session: "s1", Run: "r1", CreatedAt: now.Add(time.Minute)},
		{ID: "b1", Session: "s2", Run: "r2", CreatedAt: now},
	} {
		if err := r.SaveHarnessShare(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	at := now.Add(time.Hour)
	n, err := r.RevokeHarnessShares(ctx, "s1", at)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("revoked %d, want 2", n)
	}
	for _, id := range []string{"a1", "a2"} {
		got, err := r.GetHarnessShare(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got.RevokedAt == nil || !got.RevokedAt.Equal(at) {
			t.Fatalf("%s revoked_at = %v", id, got.RevokedAt)
		}
	}
	if got, _ := r.GetHarnessShare(ctx, "b1"); got.RevokedAt != nil {
		t.Fatal("another session's share was revoked")
	}
	// Already revoked shares are not counted again.
	if n, err := r.RevokeHarnessShares(ctx, "s1", at.Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("second revoke = %d, %v", n, err)
	}
	if got, _ := r.GetHarnessShare(ctx, "a1"); got.RevokedAt == nil || !got.RevokedAt.Equal(at) {
		t.Fatalf("second revoke moved revoked_at: %v", got.RevokedAt)
	}
}

func TestHarnessSharesGatewayAnnounce(t *testing.T) {
	r := sharesRepo(t)
	ctx := context.Background()
	if _, ok, err := r.GetGatewayAnnounce(ctx); err != nil || ok {
		t.Fatalf("empty announce: ok=%v err=%v", ok, err)
	}
	at := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	first := domain.GatewayAnnounce{Base: "https://aiman-gateway-h.tail1.ts.net", ConsolePort: 8090, Scheme: "https", PID: 42, At: at}
	if err := r.SaveGatewayAnnounce(ctx, first); err != nil {
		t.Fatal(err)
	}
	got, ok, err := r.GetGatewayAnnounce(ctx)
	if err != nil || !ok {
		t.Fatalf("announce: ok=%v err=%v", ok, err)
	}
	if got.Base != first.Base || got.ConsolePort != 8090 || got.Scheme != "https" || got.PID != 42 || !got.At.Equal(at) {
		t.Fatalf("announce = %+v", got)
	}
	// One row: a later announce replaces it.
	second := domain.GatewayAnnounce{Base: "http://aiman-gateway-h", ConsolePort: 8091, Scheme: "http", PID: 43, At: at.Add(time.Minute)}
	if err := r.SaveGatewayAnnounce(ctx, second); err != nil {
		t.Fatal(err)
	}
	got, ok, err = r.GetGatewayAnnounce(ctx)
	if err != nil || !ok {
		t.Fatalf("announce: ok=%v err=%v", ok, err)
	}
	if got.Base != second.Base || got.ConsolePort != 8091 || got.Scheme != "http" || got.PID != 43 || !got.At.Equal(second.At) {
		t.Fatalf("announce after replace = %+v", got)
	}
}
