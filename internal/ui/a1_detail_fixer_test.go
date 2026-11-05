package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/bouwerp/aiman/internal/domain"
	"github.com/charmbracelet/x/ansi"
)

func TestMetaLineShowsAFixersPR(t *testing.T) {
	s := domain.Session{Role: domain.SessionRoleFixer, FixPR: "o/r#7"}
	meta := ansi.Strip(renderMetaLine(s, detailMetaExtras{}, time.Now(), 200))
	if !strings.Contains(meta, "fix #7") || strings.Contains(meta, "review #7") {
		t.Fatalf("meta %q, want fix #7", meta)
	}
}
