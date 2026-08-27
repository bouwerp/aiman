package ui

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestGroupedSessionItemsNestsChildrenUnderParent(t *testing.T) {
	flat := []item{
		{session: domain.Session{ID: "c", Name: "reviewer", Group: "WTB-1", ParentID: "p"}, remoteName: "box"},
		{session: domain.Session{ID: "p", Name: "impl", Group: "WTB-1"}, remoteName: "box"},
	}
	got := groupedSessionItems(flat, nil, nil)
	if len(got) != 3 {
		t.Fatalf("len=%d want 3 (header + parent + child)", len(got))
	}
	parent := got[1].(item)
	child := got[2].(item)
	if parent.session.Name != "impl" || !parent.hasChildren {
		t.Fatalf("parent %+v", parent)
	}
	if child.session.Name != "reviewer" || child.treeDepth != 1 {
		t.Fatalf("child %+v depth %d", child, child.treeDepth)
	}
}

func TestCollapsedParentHidesChildren(t *testing.T) {
	flat := []item{
		{session: domain.Session{ID: "p", Name: "impl", Group: "WTB-1"}, remoteName: "box"},
		{session: domain.Session{ID: "c", Name: "reviewer", Group: "WTB-1", ParentID: "p"}, remoteName: "box"},
	}
	got := groupedSessionItems(flat, nil, map[string]bool{"p": true})
	if len(got) != 2 {
		t.Fatalf("len=%d want 2 (header + collapsed parent)", len(got))
	}
	parent := got[1].(item)
	if !parent.collapsed || parent.session.Name != "impl" {
		t.Fatalf("want collapsed parent, got %+v", parent)
	}
}

func TestMissingParentRendersChildAsRoot(t *testing.T) {
	flat := []item{
		{session: domain.Session{ID: "c", Name: "reviewer", Group: "WTB-1", ParentID: "gone"}, remoteName: "box"},
	}
	got := groupedSessionItems(flat, nil, nil)
	if len(got) != 2 {
		t.Fatalf("len=%d want 2", len(got))
	}
	child := got[1].(item)
	if child.treeDepth != 0 || child.session.Name != "reviewer" {
		t.Fatalf("orphan child should be a root, got %+v", child)
	}
}

func TestToggleSessionCollapsedHidesChildren(t *testing.T) {
	cfg := twoRemoteCfg()
	sessions := []domain.Session{
		{ID: "p", Name: "impl", Group: "WTB-1", RemoteHost: "10.0.1.5"},
		{ID: "c", Name: "reviewer", Group: "WTB-1", ParentID: "p", RemoteHost: "10.0.1.5"},
	}
	m := NewModel(cfg, nil, sessions, &mockSessionRepo{}, nil, nil, nil)
	m.applyRemoteFilter()
	// Skip group header (index 0); parent is 1.
	m.list.Select(1)
	_, _, handled := m.toggleSelectedSessionCollapsed()
	if !handled {
		t.Fatal("toggle should handle a parent session")
	}
	var names []string
	for _, it := range m.list.Items() {
		row := it.(item)
		if row.header {
			continue
		}
		names = append(names, row.session.Name)
	}
	if strings.Join(names, ",") != "impl" {
		t.Fatalf("collapsed parent should hide child, got %v", names)
	}
}

func TestChildFollowsParentGroup(t *testing.T) {
	flat := []item{
		{session: domain.Session{ID: "p", Name: "impl", Group: "WTB-1"}, remoteName: "box"},
		{session: domain.Session{ID: "c", Name: "reviewer", Group: "quick", ParentID: "p"}, remoteName: "box"},
	}
	got := groupedSessionItems(flat, nil, nil)
	var names []string
	for _, it := range got {
		row := it.(item)
		if row.header {
			names = append(names, "H:"+row.session.Group)
			continue
		}
		names = append(names, row.session.Name)
	}
	joined := strings.Join(names, ",")
	if joined != "H:WTB-1,impl,reviewer" {
		t.Fatalf("child must follow parent group, got %q", joined)
	}
}
