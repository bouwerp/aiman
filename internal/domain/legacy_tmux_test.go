package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestIsLegacyTmux(t *testing.T) {
	tests := []struct {
		backend string
		want    bool
	}{
		{BackendPTY, false},
		{"", false},
		{BackendLegacyTmux, true},
	}
	for _, tt := range tests {
		if got := (Session{Backend: tt.backend}).IsLegacyTmux(); got != tt.want {
			t.Errorf("IsLegacyTmux(%q) = %v, want %v", tt.backend, got, tt.want)
		}
	}
}

func TestLegacyTmuxErrorHintNamesHostAndCommand(t *testing.T) {
	s := Session{ID: "abc", RemoteHost: "regent0", TmuxSession: "WTB-1", Backend: BackendLegacyTmux}
	err := NewLegacyTmuxError(s)

	if !errors.Is(err, ErrLegacyTmuxSession) {
		t.Fatalf("errors.Is(err, ErrLegacyTmuxSession) = false for %v", err)
	}
	var lt *LegacyTmuxError
	if !errors.As(err, &lt) {
		t.Fatalf("errors.As(*LegacyTmuxError) failed for %T", err)
	}
	want := "tmux kill-session -t '=WTB-1'"
	if got := lt.StopCommand(); got != want {
		t.Errorf("StopCommand() = %q, want %q", got, want)
	}
	for _, part := range []string{"regent0", want} {
		if !strings.Contains(lt.Hint(), part) {
			t.Errorf("Hint() %q does not contain %q", lt.Hint(), part)
		}
		if !strings.Contains(err.Error(), part) {
			t.Errorf("Error() %q does not contain %q", err.Error(), part)
		}
	}
}

func TestLegacyTmuxErrorQuotesSingleQuoteInName(t *testing.T) {
	err := NewLegacyTmuxError(Session{RemoteHost: "h", TmuxSession: "it's"})
	var lt *LegacyTmuxError
	if !errors.As(err, &lt) {
		t.Fatal("errors.As failed")
	}
	want := `tmux kill-session -t '=it'\''s'`
	if got := lt.StopCommand(); got != want {
		t.Errorf("StopCommand() = %q, want %q", got, want)
	}
}
