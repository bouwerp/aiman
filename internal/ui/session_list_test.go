package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestGroupedSessionItemsHeadersAndRollup(t *testing.T) {
	flat := []item{
		{session: domain.Session{ID: "2", Name: "reviewer", Group: "WTB-1925"}, needsInput: true, remoteName: "regent0"},
		{session: domain.Session{ID: "1", Name: "impl", Group: "WTB-1925"}, activity: "busy", remoteName: "regent0"},
		{session: domain.Session{ID: "3", Name: "q1", Group: "quick"}, activity: "idle", remoteName: "regent0"},
	}
	got := groupedSessionItems(flat)
	if len(got) != 5 {
		t.Fatalf("len=%d want 5 (2 headers + 3 sessions)", len(got))
	}
	h1, ok := got[0].(item)
	if !ok || !h1.header || h1.session.Group != "WTB-1925" {
		t.Fatalf("first header: %+v", got[0])
	}
	if h1.activity != "waiting" {
		t.Fatalf("rollup %q want waiting", h1.activity)
	}
	a, _ := got[1].(item)
	b, _ := got[2].(item)
	if a.session.Name != "impl" || b.session.Name != "reviewer" {
		t.Fatalf("order %q %q", a.session.Name, b.session.Name)
	}
}
