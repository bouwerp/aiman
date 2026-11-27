package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/charmbracelet/x/ansi"
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

// The A1 row draws the badge on every parent, expanded or not, after the name,
// and keeps it when a long name is cut to fit.
func TestA1RowShowsChildBadge(t *testing.T) {
	parent := item{session: domain.Session{Name: "coordinator-with-a-very-long-name-indeed"}, childN: 3, hasChildren: true}
	for _, collapsed := range []bool{false, true} {
		parent.collapsed = collapsed
		want := "▾3"
		if collapsed {
			want = "▸3"
		}
		for _, w := range []int{120, 40} {
			got := ansi.Strip(renderA1Row(parent, false, w))
			if !strings.Contains(got, want) {
				t.Errorf("collapsed=%v width=%d: want %q in %q", collapsed, w, want, got)
			}
		}
	}
	leaf := item{session: domain.Session{Name: "solo"}}
	if got := ansi.Strip(renderA1Row(leaf, false, 80)); strings.ContainsAny(got, "▾▸") {
		t.Errorf("leaf row must carry no badge, got %q", got)
	}
}
