package ui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/list"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/bouwerp/aiman/internal/server"
	"github.com/charmbracelet/x/ansi"
)

func TestTodoRefGlyph(t *testing.T) {
	snap := newTodoSnapshot(domain.TodoList{
		SessionID: "p", Rev: 1,
		Items: []domain.TodoItem{
			{ID: "t1", Text: "Linked work", State: domain.TodoPending, Ref: "child:c1"},
			{ID: "t2", Text: "Other work", State: domain.TodoPending, Ref: "check:lint"},
		},
	}, server.SteerStatus{})
	glyph := func(ref string) (string, bool) {
		if ref == "c1" {
			return "◐", true
		}
		return "", false
	}
	lines := renderTodoLines(todoView{Snap: snap, RefGlyph: glyph}, 80)
	if len(lines) < 2 || !strings.HasPrefix(ansi.Strip(lines[0]), "◐") {
		t.Fatalf("linked row = %q", lines)
	}
	if !strings.HasPrefix(ansi.Strip(lines[1]), "○") {
		t.Fatalf("unlinked row = %q", lines[1])
	}
}

func TestPreferredCollapsedChildGlyph(t *testing.T) {
	parent := domain.Session{ID: "p", Branch: "feat/x", RemoteHost: "regent0"}
	m, _ := promptModel(parent)
	child := domain.Session{ID: "c1", ParentID: "p", Branch: "feat/x--t1", RemoteHost: "regent0"}
	m.allSessions = []domain.Session{parent, child}
	m.filteredSessions = []item{{session: parent}, {session: child}}
	m.list.SetItems([]list.Item{item{session: parent}})
	if glyph, ok := m.todoRefGlyph("c1"); !ok || glyph == "" {
		t.Fatalf("glyph = %q ok=%v", glyph, ok)
	}
}
