package sqlite

import (
	"context"
	"path/filepath"
	"testing"
)

func TestForgottenSessionsRoundTrip(t *testing.T) {
	r, err := NewRepository(filepath.Join(t.TempDir(), "aiman.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	ctx := context.Background()

	if got, err := r.IsForgotten(ctx, "s1"); err != nil || got {
		t.Fatalf("fresh id: forgotten=%v err=%v", got, err)
	}
	if err := r.MarkForgotten(ctx, "s1", "terminate"); err != nil {
		t.Fatal(err)
	}
	// A second mark replaces the first instead of failing on the key.
	if err := r.MarkForgotten(ctx, "s1", "forget"); err != nil {
		t.Fatal(err)
	}
	if got, err := r.IsForgotten(ctx, "s1"); err != nil || !got {
		t.Fatalf("after mark: forgotten=%v err=%v", got, err)
	}
	if got, _ := r.IsForgotten(ctx, "s2"); got {
		t.Fatal("other id must not be forgotten")
	}
	if err := r.ClearForgotten(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	if got, err := r.IsForgotten(ctx, "s1"); err != nil || got {
		t.Fatalf("after clear: forgotten=%v err=%v", got, err)
	}
	if err := r.ClearForgotten(ctx, "missing"); err != nil {
		t.Fatalf("clearing a missing id: %v", err)
	}
}
