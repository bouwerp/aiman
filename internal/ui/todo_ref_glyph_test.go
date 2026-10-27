package ui

import (
	"strings"
	"testing"

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
