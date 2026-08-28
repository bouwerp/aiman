package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestChildBadgeMarksParentsAndCounts(t *testing.T) {
	parent := item{session: domain.Session{Name: "review-prs"}, childN: 2, hasChildren: true}
	if got := parent.plainTitle(); !strings.Contains(got, "▾2") {
		t.Errorf("expanded parent should show its child count, got %q", got)
	}
	parent.collapsed = true
	if got := parent.plainTitle(); !strings.Contains(got, "▸2") {
		t.Errorf("collapsed parent should show a collapsed glyph, got %q", got)
	}
}

func TestChildBadgeAbsentForLeafSessions(t *testing.T) {
	leaf := item{session: domain.Session{Name: "solo"}}
	got := leaf.plainTitle()
	if strings.Contains(got, "▾") || strings.Contains(got, "▸") {
		t.Errorf("a session with no children must carry no badge, got %q", got)
	}
}

// The badge counts every descendant, not just the direct children: its job is to
// say how much a collapsed row is hiding, and deeper levels are hidden too.
func TestCountDescendantsIsRecursive(t *testing.T) {
	children := map[string][]item{
		"a": {{session: domain.Session{ID: "b"}}, {session: domain.Session{ID: "c"}}},
		"b": {{session: domain.Session{ID: "d"}}},
	}
	if got := countDescendants("a", children); got != 3 {
		t.Errorf("got %d, want 3", got)
	}
	if got := countDescendants("b", children); got != 1 {
		t.Errorf("got %d, want 1", got)
	}
	if got := countDescendants("d", children); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

// Parent ids come off the wire and may point anywhere, so a cycle must not hang
// the render.
func TestCountDescendantsSurvivesACycle(t *testing.T) {
	children := map[string][]item{
		"a": {{session: domain.Session{ID: "b"}}},
		"b": {{session: domain.Session{ID: "a"}}},
	}
	if got := countDescendants("a", children); got != 1 {
		t.Errorf("got %d, want 1", got)
	}
}
