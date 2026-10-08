package server

import (
	"testing"

	"github.com/bouwerp/aiman/internal/pane"
)

func TestPreviewReplyPatchesARememberedScreen(t *testing.T) {
	s := &Server{}
	first := s.previewReply("s", "a\nb", "")
	if first.Unchanged || first.Text != "a\nb" {
		t.Fatalf("first=%+v", first)
	}
	next := s.previewReply("s", "a\nB", first.Hash)
	if next.Unchanged || next.Text != "" || len(next.Rows) != 1 || next.Rows[0].Line != "B" {
		t.Fatalf("next=%+v", next)
	}
	if next.Hash != pane.ScreenHash("a\nB") {
		t.Fatalf("hash=%s", next.Hash)
	}
}

func TestDropPaneMemoryForgetsTheScreen(t *testing.T) {
	s := &Server{}
	first := s.previewReply("s", "a\nb", "")
	s.dropPaneMemory("s")
	again := s.previewReply("s", "a\nB", first.Hash)
	if again.Text != "a\nB" || again.Rows != nil {
		t.Fatalf("after forget=%+v", again)
	}
}
