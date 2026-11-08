package notify

import (
	"context"
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

func TestFormatHostLevelDetailIsWholeText(t *testing.T) {
	for _, kind := range []domain.NotifyKind{domain.NotifyHostUpdate, domain.NotifyAgentUpdate} {
		const want = "serve updated v0.34.0 → v0.35.0 on remote1"
		if got := Format("x", "h", kind, want); got != want {
			t.Errorf("Format(%s) = %q, want %q", kind, got, want)
		}
	}
}

func TestEmitPublishesFormattedText(t *testing.T) {
	s := &fakeStore{}
	var got []domain.Notification
	r := NewRecorder(s, Settings{Events: map[domain.NotifyKind]bool{domain.NotifyHostUpdate: true}, Host: "h"},
		func(n domain.Notification) { got = append(got, n) })
	if err := r.Emit(context.Background(), "", "", domain.NotifyHostUpdate, "serve updated v1 → v2 on h"); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "serve updated v1 → v2 on h" || got[0].Kind != domain.NotifyHostUpdate {
		t.Fatalf("published %+v", got)
	}
}
