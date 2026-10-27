package notify

import (
	"strings"
	"testing"

	"github.com/bouwerp/aiman/internal/domain"
)

func TestFormatCollapsesAndTruncates(t *testing.T) {
	got := Format("PROJ-1", "box", domain.NotifyError, "panic:\n\n  boom <x> & "+strings.Repeat("a", 300))
	if strings.Contains(got, "\n") {
		t.Fatal("newline kept")
	}
	if !strings.HasPrefix(got, "PROJ-1 on box: error · panic: boom <x> & ") {
		t.Fatalf("got %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatal("not truncated")
	}
	if Format("q1", "box", domain.NotifyEnded, "  ") != "q1 on box: ended" {
		t.Fatal("empty detail keeps no separator")
	}
}

func TestLabelPrefersIssueThenNameThenShortID(t *testing.T) {
	if Label("PROJ-1", "foo", "12345678") != "PROJ-1" {
		t.Fatal("issue preferred")
	}
	if Label("", "foo", "12345678") != "foo" {
		t.Fatal("name preferred")
	}
	if Label("", "", "12345678") != "1234567" {
		t.Fatal("short id")
	}
}
