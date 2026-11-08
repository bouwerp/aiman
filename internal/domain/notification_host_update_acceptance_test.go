//go:build acceptance

package domain_test

import (
	"slices"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestNotifyHostUpdateKind(t *testing.T) {
	if domain.NotifyHostUpdate != "host_update" {
		t.Fatalf("NotifyHostUpdate = %q, want host_update", domain.NotifyHostUpdate)
	}
	if !slices.Contains(domain.AllNotifyKinds(), domain.NotifyHostUpdate) {
		t.Fatalf("AllNotifyKinds() = %v, missing host_update", domain.AllNotifyKinds())
	}
	if domain.NeedsYou(domain.NotifyHostUpdate) {
		t.Fatal("NeedsYou(host_update) = true, want false")
	}
	if got := domain.EventLabel(domain.NotifyHostUpdate); got != "host update" {
		t.Fatalf("EventLabel(host_update) = %q, want %q", got, "host update")
	}
}
