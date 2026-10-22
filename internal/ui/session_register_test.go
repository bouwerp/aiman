package ui

import (
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestSessionsForHostFiltersHostAndPlaceholders(t *testing.T) {
	in := []domain.Session{
		{ID: "real-a", RemoteHost: "a"},
		{ID: "real-b", RemoteHost: "b"},
		{ID: "pending-x", RemoteHost: "a"},
	}
	got := sessionsForHost(in, "a")
	if len(got) != 1 || got[0].ID != "real-a" {
		t.Fatalf("got %+v, want only real-a", got)
	}
	got[0].ID = "mutated"
	if in[0].ID != "real-a" {
		t.Fatalf("input was mutated: %+v", in[0])
	}
}
